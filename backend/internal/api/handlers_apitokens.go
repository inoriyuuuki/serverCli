package api

import (
	"net/http"
	"strconv"

	"servercli/internal/model"
	"servercli/internal/service"
)

func (s *Server) primaryOnly(w http.ResponseWriter, r *http.Request) bool {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "managed by the primary node", nil)
		return true
	}
	return false
}

// handleCreateAPIToken creates an access token; the plaintext is returned once.
func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	var in service.CreateTokenInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	admin := adminFrom(r.Context())
	res, err := s.tokens.Create(r.Context(), in, admin.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"api_token": res.Token,
		"token":     res.Plaintext,
	})
}

// handleListAPITokens lists access tokens.
func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	tokens, err := s.tokens.List(r.Context(), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	type tokenWithCount struct {
		*model.APIAccessToken
		ActiveLeaseCount int64 `json:"active_lease_count"`
	}
	out := make([]tokenWithCount, 0, len(tokens))
	for _, tok := range tokens {
		n, err := s.store.ActiveLeaseCountByAccessToken(r.Context(), tok.ID)
		if err != nil {
			s.log.Warn("active lease count unavailable", "error", err, "token_id", tok.ID)
			n = 0
		}
		out = append(out, tokenWithCount{APIAccessToken: tok, ActiveLeaseCount: n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_tokens": out})
}

// handleGetAPIToken returns one token (never the hash or plaintext).
func (s *Server) handleGetAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	tok, err := s.tokens.Token(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	n, _ := s.store.ActiveLeaseCountByAccessToken(r.Context(), tok.ID)
	writeJSON(w, http.StatusOK, map[string]any{"api_token": tok, "active_lease_count": n})
}

// handleRevokeAPIToken revokes a token and cascades to its active leases in
// one atomic transaction. A cascade failure surfaces as 5xx so the admin never
// mistakes a partially-revoked state for success.
func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	admin := adminFrom(r.Context())
	id := r.PathValue("id")
	tok, err := s.tokens.Token(r.Context(), id)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "access token revoked by admin"
	}
	affected, err := s.leases.RevokeTokenCascade(r.Context(), id, admin.ID, reason)
	if err != nil {
		s.log.Error("access token revoke cascade failed", "error", err, "token_id", id)
		writeError(w, r, s.log, http.StatusInternalServerError, "REVOKE_INCOMPLETE",
			"token revoked but lease cascade failed; retry the revoke to complete it", nil)
		return
	}
	s.auditor.OK(r.Context(), service.AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "api_token.revoke",
		ResourceType: "api_access_token", ResourceID: tok.ID,
		Summary: "access token revoked", Details: map[string]any{"name": tok.Name, "token_prefix": tok.TokenPrefix},
		RiskLevel: service.RiskHigh,
	})
	for _, l := range affected {
		s.publishLeaseKeys(l)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_token":           tok,
		"revoked_lease_count": len(affected),
	})
}

// handleListTokenUsageLogs lists usage logs for a token.
func (s *Server) handleListTokenUsageLogs(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	logs, err := s.tokens.UsageLogs(r.Context(), r.PathValue("id"), q.Get("outcome"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usage_logs": logs})
}

var _ = model.TokenUsageSuccess
