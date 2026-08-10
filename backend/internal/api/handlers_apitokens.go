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

// tokenView augments an access token with the structured permissions parsed
// from its stored JSON, so list/detail/create/revoke/update responses expose a
// stable "permissions" object instead of raw JSON.
type tokenView struct {
	*model.APIAccessToken
	Permissions service.PermissionSet `json:"permissions"`
}

// newTokenView builds a tokenView. Unparseable permission JSON fails closed to
// an empty zero-grant set (never leaks the raw JSON).
func newTokenView(t *model.APIAccessToken) tokenView {
	perms, err := service.ParsePermissions(t.PermissionsJSON)
	if err != nil {
		perms = service.PermissionSet{Version: 1}
	}
	return tokenView{APIAccessToken: t, Permissions: perms}
}

// handleCreateAPIToken creates an access token; the plaintext is returned once.
// New tokens always start with zero permissions; grants are assigned later via
// handleUpdateAPITokenPermissions. The create body intentionally accepts no
// permissions field (decodeJSON rejects unknown fields).
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
		"api_token": newTokenView(res.Token),
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
		tokenView
		ActiveLeaseCount int64 `json:"active_lease_count"`
	}
	out := make([]tokenWithCount, 0, len(tokens))
	for _, tok := range tokens {
		n, err := s.store.ActiveLeaseCountByAccessToken(r.Context(), tok.ID)
		if err != nil {
			s.log.Warn("active lease count unavailable", "error", err, "token_id", tok.ID)
			n = 0
		}
		out = append(out, tokenWithCount{tokenView: newTokenView(tok), ActiveLeaseCount: n})
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
	writeJSON(w, http.StatusOK, map[string]any{"api_token": newTokenView(tok), "active_lease_count": n})
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
		"api_token":           newTokenView(tok),
		"revoked_lease_count": len(affected),
	})
}

// handlePermissionCatalog returns the static permission catalog and its
// top-level categories, driving the admin UI permission editor.
func (s *Server) handlePermissionCatalog(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"categories":  service.PermissionCategories(),
		"permissions": service.PermissionCatalog(),
	})
}

// handleUpdateAPITokenPermissions replaces a token's permission set under an
// optimistic lock: the request must carry the current permission_version,
// otherwise the update conflicts (409) and the caller must re-read the token.
func (s *Server) handleUpdateAPITokenPermissions(w http.ResponseWriter, r *http.Request) {
	if s.primaryOnly(w, r) {
		return
	}
	var in struct {
		PermissionVersion int                   `json:"permission_version"`
		Permissions       service.PermissionSet `json:"permissions"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	admin := adminFrom(r.Context())
	tok, err := s.tokens.UpdatePermissions(r.Context(), r.PathValue("id"), in.PermissionVersion, in.Permissions, admin.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_token": newTokenView(tok)})
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
