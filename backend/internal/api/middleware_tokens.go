package api

import (
	"context"
	"net/http"
	"strings"

	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/service"
)

type tokenCtxKey struct{}

func tokenPrincipalFrom(ctx context.Context) *service.TokenPrincipal {
	v, _ := ctx.Value(tokenCtxKey{}).(*service.TokenPrincipal)
	return v
}

// tokenAuthHookState carries per-request state shared between the token auth
// middleware, the optional afterResolve hook and the handler itself. The
// middleware stores a pointer in the request context so handlers can mutate
// it (e.g. force a usage outcome) via setForcedOutcome.
type tokenAuthHookState struct {
	rateAcquired  bool   // notification rate-limit quota was acquired
	forcedOutcome string // handler-forced usage outcome ("" = none)
	route         string // normalized route template for usage logging
}

type tokenAuthHookStateKey struct{}

// tokenAuthHookStateFrom returns the hook state attached by tokenAuthWith, or
// nil when the request did not pass through it.
func tokenAuthHookStateFrom(ctx context.Context) *tokenAuthHookState {
	v, _ := ctx.Value(tokenAuthHookStateKey{}).(*tokenAuthHookState)
	return v
}

// setForcedOutcome marks the usage outcome the middleware must record for the
// current request, overriding the status-code based default (used by GET
// /notice which returns HTTP 200 while the attempt is denied/failed).
func setForcedOutcome(ctx context.Context, outcome string) {
	if h := tokenAuthHookStateFrom(ctx); h != nil {
		h.forcedOutcome = outcome
	}
}

// tokenAuthOptions configures the tokenAuthWith middleware.
type tokenAuthOptions struct {
	// afterResolve runs after Resolve succeeds and before Authorize; it may
	// consume side quotas (e.g. notification rate limits). Returning true
	// means the hook already wrote the response plus usage/audit records and
	// the middleware returns immediately.
	afterResolve func(w http.ResponseWriter, r *http.Request, p *service.TokenPrincipal, tok *model.APIAccessToken) bool
	// outcomeOverride forces the usage outcome after the handler returns
	// (for handlers that return HTTP 200 while the attempt is denied/failed).
	outcomeOverride func(r *http.Request, p *service.TokenPrincipal, tok *model.APIAccessToken) (outcome string, ok bool)
}

// tokenAuth authenticates external AI self-service requests with an access
// token, authorizes the resource/action pair and records a usage log row for
// every recognized token (valid, expired or revoked). route is the normalized
// path template (e.g. /api/v1/ai/lease-requests/{id}).
func (s *Server) tokenAuth(resource, action, route string) func(http.HandlerFunc) http.HandlerFunc {
	return s.tokenAuthWith(resource, action, route, tokenAuthOptions{})
}

// tokenAuthWith is the full token-auth pipeline: authenticate, run the
// optional afterResolve hook (e.g. rate-limit acquisition), authorize, run the
// handler, then record a usage row honoring an optional outcome override.
func (s *Server) tokenAuthWith(resource, action, route string, opts tokenAuthOptions) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			plaintext := bearerToken(r)
			if plaintext == "" {
				s.auditor.Denied(r.Context(), service.AuditInput{
					ActorType: model.ActorAI, Action: "api_token.auth", SourceIP: remoteIP(r),
					Summary: "access token required", RiskLevel: service.RiskHigh,
				})
				writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "missing Bearer access token", nil)
				return
			}
			principal, tok, state, err := s.tokens.Resolve(r.Context(), plaintext)
			if err != nil {
				// A recognized but expired/revoked token still yields a usage log.
				if tok != nil {
					sw := &statusWriter{ResponseWriter: w, status: http.StatusUnauthorized}
					s.recordTokenUsage(r, sw, tok, state, model.TokenUsageDenied, resource, action, route)
				}
				s.auditor.Denied(r.Context(), service.AuditInput{
					ActorType: model.ActorAI, Action: "api_token.auth", SourceIP: remoteIP(r),
					Summary: "access token authentication failed", RiskLevel: service.RiskHigh,
				})
				writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid or expired access token", nil)
				return
			}
			hookState := &tokenAuthHookState{route: route}
			authCtx := context.WithValue(r.Context(), tokenCtxKey{}, principal)
			authCtx = context.WithValue(authCtx, tokenAuthHookStateKey{}, hookState)
			if opts.afterResolve != nil {
				if opts.afterResolve(w, r.WithContext(authCtx), principal, tok) {
					return
				}
			}
			if authErr := s.tokens.Authorize(principal, resource, action, nil); authErr != nil {
				sw := &statusWriter{ResponseWriter: w, status: http.StatusForbidden}
				s.recordTokenUsage(r, sw, tok, model.TokenStateValid, model.TokenUsageDenied, resource, action, route)
				s.auditor.Denied(r.Context(), service.AuditInput{
					ActorType: model.ActorAI, ActorID: principal.Name, Action: "api_token.denied",
					SourceIP: remoteIP(r), Summary: "access token lacks permission: " + resource + ":" + action,
					RiskLevel: service.RiskHigh,
				})
				writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "access token lacks permission for this operation", nil)
				return
			}
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			recorded := false
			defer func() {
				// If the handler panics (recovered by the outer middleware), still
				// record a failure usage log so 5xx never loses the token trail.
				if !recorded {
					if sw.status < 500 {
						sw.status = http.StatusInternalServerError
					}
					s.recordTokenUsage(r, sw, tok, model.TokenStateValid, model.TokenUsageFailure, resource, action, route)
				}
			}()
			next(sw, r.WithContext(authCtx))
			recorded = true
			outcome := model.TokenUsageSuccess
			if sw.status >= 400 && sw.status < 500 {
				outcome = model.TokenUsageDenied
			} else if sw.status >= 500 {
				outcome = model.TokenUsageFailure
			}
			if opts.outcomeOverride != nil {
				if o, ok := opts.outcomeOverride(r.WithContext(authCtx), principal, tok); ok {
					outcome = o
				}
			}
			s.recordTokenUsage(r, sw, tok, model.TokenStateValid, outcome, resource, action, route)
			if err := s.store.TouchAccessToken(r.Context(), principal.TokenID, remoteIP(r)); err != nil {
				s.log.Warn("token usage touch failed", "error", err)
			}
		}
	}
}

// recordTokenUsage persists one api_token_usage_log row. Write failures are
// logged but never change the already-produced HTTP response.
func (s *Server) recordTokenUsage(r *http.Request, sw *statusWriter, tok *model.APIAccessToken, state, outcome, resource, action, route string) {
	lrID, leaseID := idsFromPath(r.URL.Path)
	entry := &model.APITokenUsageLog{
		TokenID:        tok.ID,
		EnvironmentID:  s.envID,
		RequestID:      logger.RequestID(r.Context()),
		Method:         r.Method,
		Route:          route,
		Resource:       resource,
		Action:         action,
		SourceIP:       remoteIP(r),
		UserAgent:      r.UserAgent(),
		StatusCode:     sw.status,
		Outcome:        outcome,
		LeaseRequestID: lrID,
		LeaseID:        leaseID,
		TokenState:     state,
	}
	if err := s.store.CreateTokenUsageLog(r.Context(), entry); err != nil {
		s.log.Warn("token usage log write failed", "error", err)
	}
}

// idsFromPath extracts a lease request id / lease id from an AI API path.
func idsFromPath(path string) (leaseRequestID, leaseID string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "lease-requests":
			leaseRequestID = parts[i+1]
		case "leases":
			leaseID = parts[i+1]
		}
	}
	return leaseRequestID, leaseID
}

// adminOrToken accepts either an admin session (cookie) or a valid access
// token for read-only endpoints (e.g. node discovery). Token requests go
// through the normal tokenAuth path, including usage logging and the
// resource/action permission check. Write methods keep requireAdmin only.
func (s *Server) adminOrToken(resource, action, route string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if cookie, err := r.Cookie("servercli_session"); err == nil {
				if sess, admin, aerr := s.auth.Authenticate(r.Context(), cookie.Value); aerr == nil {
					_ = s.store.TouchSession(r.Context(), sess.ID, remoteIP(r), r.UserAgent())
					ctx := context.WithValue(r.Context(), adminCtxKey{}, admin)
					ctx = context.WithValue(ctx, sessionCtxKey{}, sess)
					next(w, r.WithContext(ctx))
					return
				}
			}
			// No valid session: fall back to Access Token (401 when absent).
			s.tokenAuth(resource, action, route)(next)(w, r)
		}
	}
}
