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

// tokenAuth authenticates external AI self-service requests with an access
// token, authorizes the resource/action pair and records a usage log row for
// every recognized token (valid, expired or revoked). route is the normalized
// path template (e.g. /api/v1/ai/lease-requests/{id}).
func (s *Server) tokenAuth(resource, action, route string) func(http.HandlerFunc) http.HandlerFunc {
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
			ctx := context.WithValue(r.Context(), tokenCtxKey{}, principal)
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
			next(sw, r.WithContext(ctx))
			recorded = true
			outcome := model.TokenUsageSuccess
			if sw.status >= 400 && sw.status < 500 {
				outcome = model.TokenUsageDenied
			} else if sw.status >= 500 {
				outcome = model.TokenUsageFailure
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
