package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/service"
)

// notificationSource is the fixed source identifier for notifications sent
// through the external token-authenticated API. External callers can never
// override it (decodeJSON rejects unknown fields such as "source").
const notificationSource = "api.token"

// notificationRateHook acquires the per-token + global notification quota
// right after token resolution and before authorization, so a valid token
// that is later denied (403) still consumed its quota. On quota exhaustion it
// writes a standard 429 with Retry-After, records a denied usage row and a
// high-risk audit event, and tells the middleware to stop (returns true).
func (s *Server) notificationRateHook() func(w http.ResponseWriter, r *http.Request, p *service.TokenPrincipal, tok *model.APIAccessToken) bool {
	return func(w http.ResponseWriter, r *http.Request, p *service.TokenPrincipal, tok *model.APIAccessToken) bool {
		retryAfter, ok := s.notifyLimiter.TryAcquire(p.TokenID)
		if ok {
			if h := tokenAuthHookStateFrom(r.Context()); h != nil {
				h.rateAcquired = true
			}
			return false
		}
		// Rate limited: nothing was debited, so no quota was consumed. The
		// response never echoes request content or the token.
		sw := &statusWriter{ResponseWriter: w, status: http.StatusTooManyRequests}
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeError(w, r, s.log, http.StatusTooManyRequests, "RATE_LIMITED",
			"notification rate limit exceeded, retry later", nil)
		route := "/api/v1/notifications/send"
		if h := tokenAuthHookStateFrom(r.Context()); h != nil && h.route != "" {
			route = h.route
		}
		s.recordTokenUsage(r, sw, tok, model.TokenStateValid, model.TokenUsageDenied,
			service.ResourceNotifications, service.ActionSend, route)
		s.auditor.Denied(r.Context(), service.AuditInput{
			ActorType: model.ActorAI, ActorID: p.TokenID, Action: "notification.ratelimited",
			ResourceType: "api_access_token", ResourceID: p.TokenID,
			Summary:   "notification rate limit exceeded",
			Details:   notificationAuditDetails(notificationSource, "default", "", 0, 0, service.ResultDenied, r),
			RiskLevel: service.RiskHigh,
		})
		return true
	}
}

// notificationOutcomeOverride reads the outcome the /notice handler forced for
// requests that return HTTP 200 while the attempt was denied/failed.
func notificationOutcomeOverride(r *http.Request, _ *service.TokenPrincipal, _ *model.APIAccessToken) (string, bool) {
	if h := tokenAuthHookStateFrom(r.Context()); h != nil && h.forcedOutcome != "" {
		return h.forcedOutcome, true
	}
	return "", false
}

// notificationAuditDetails builds the whitelisted details map for notification
// audit events. It never contains titles, message bodies, webhook URLs, tokens
// or upstream responses.
func notificationAuditDetails(source, channel, level string, titleLength, messageLength int, outcome string, r *http.Request) map[string]any {
	details := map[string]any{
		"source":         source,
		"channel":        channel,
		"level":          level,
		"title_length":   titleLength,
		"message_length": messageLength,
		"outcome":        outcome,
	}
	if rid := logger.RequestID(r.Context()); rid != "" {
		details["request_id"] = rid
	}
	return details
}

// handleNotificationSend is POST /api/v1/notifications/send. The request body
// may only contain title/message/level/channel: DisallowUnknownFields rejects
// any extra field, so an external caller can never submit source or url.
func (s *Server) handleNotificationSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Message string `json:"message"`
		Level   string `json:"level"`
		Channel string `json:"channel"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	principal := tokenPrincipalFrom(r.Context())
	req := service.NotificationRequest{
		Title:   in.Title,
		Message: in.Message,
		Level:   in.Level,
		Channel: in.Channel,
		Source:  notificationSource,
	}
	ctx := service.WithNotificationTokenID(r.Context(), principal.TokenID)
	res, err := s.notifications.SendAuthorized(ctx, req)
	if err != nil {
		// ErrBadRequest→400, ErrNotConfigured→503, ErrUpstream→502,
		// ErrRateLimited→429 (mapped by writeServiceError).
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notification": res})
}

// normalizeNoticeLevel maps the legacy /notice logLevel parameter to the
// service level vocabulary. Unknown values, empty and "debug" fall back to
// "info"; "warn"/"warning" and "error"/"fatal" keep their meanings.
func normalizeNoticeLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warn", "warning":
		return "warning"
	case "error", "fatal":
		return "error"
	default:
		return "info"
	}
}

// handleNotice is the legacy GET /notice compatibility endpoint. It always
// returns HTTP 200 with {"ret":0|1,"msg":...} for non-auth outcomes; auth
// failures stay standard 401/403 and rate limiting stays standard 429. The
// token usage outcome is forced by the middleware via setForcedOutcome so
// 200 responses still record denied/failure usage rows.
func (s *Server) handleNotice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	method := strings.TrimSpace(q.Get("method"))
	message := strings.TrimSpace(q.Get("message"))
	level := normalizeNoticeLevel(q.Get("logLevel"))
	principal := tokenPrincipalFrom(r.Context())

	deny := func(reason string, writeAudit bool) {
		setForcedOutcome(r.Context(), model.TokenUsageDenied)
		if writeAudit {
			s.auditor.Denied(r.Context(), service.AuditInput{
				ActorType: model.ActorAI, ActorID: principal.TokenID, Action: "notification.send",
				ResourceType: "api_access_token", ResourceID: principal.TokenID,
				Summary:   "notification denied",
				Details:   notificationAuditDetails(notificationSource, "default", level, utf8.RuneCountInString(method), len([]byte(message)), service.ResultDenied, r),
				RiskLevel: service.RiskHigh,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": reason})
	}

	if method == "" || message == "" {
		// Empty title/message: do not deliver. Call SendAuthorized so the
		// service writes its denied audit for the failed validation; the
		// handler still responds 200/ret=0 with a safe, generic reason.
		setForcedOutcome(r.Context(), model.TokenUsageDenied)
		ctx := service.WithNotificationTokenID(r.Context(), principal.TokenID)
		req := service.NotificationRequest{Title: method, Message: message, Level: level, Channel: "default", Source: notificationSource}
		_, _ = s.notifications.SendAuthorized(ctx, req)
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": "method or message is empty"})
		return
	}
	if utf8.RuneCountInString(method) > 200 || len([]byte(message)) > 4096 {
		// Too long: never deliver (the service's message cap is larger, so
		// SendAuthorized could actually send — hence the explicit audit here).
		deny("method or message too long", true)
		return
	}

	req := service.NotificationRequest{
		Title:   method,
		Message: message,
		Level:   level,
		Channel: "default",
		Source:  notificationSource,
	}
	ctx := service.WithNotificationTokenID(r.Context(), principal.TokenID)
	_, err := s.notifications.SendAuthorized(ctx, req)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ret": 1, "msg": "处理成功"})
		return
	}
	switch {
	case errors.Is(err, service.ErrRateLimited):
		// Should not happen: the auth hook already acquired the quota.
		writeServiceError(w, r, s.log, err)
	case errors.Is(err, service.ErrBadRequest):
		// Service-level validation failure (e.g. level/channel): denied.
		setForcedOutcome(r.Context(), model.TokenUsageDenied)
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": "invalid request parameters"})
	default:
		// Provider not configured or upstream failure: the service already
		// wrote the failure audit; surface a safe 200/ret=0.
		setForcedOutcome(r.Context(), model.TokenUsageFailure)
		writeJSON(w, http.StatusOK, map[string]any{"ret": 0, "msg": "notification send failed"})
	}
}
