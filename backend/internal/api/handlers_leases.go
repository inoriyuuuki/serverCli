package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
	"servercli/internal/service"
)

func bearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimPrefix(authz, "Bearer ")
	}
	return ""
}

func (s *Server) handleCreateLeaseRequest(w http.ResponseWriter, r *http.Request) {
	var in service.LeaseRequestInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.ClientRequestID == "" {
		in.ClientRequestID = r.Header.Get("Idempotency-Key")
	}
	if in.AIAgentID == "" {
		in.AIAgentID = "ai-agent"
	}
	principal := tokenPrincipalFrom(r.Context())
	res, err := s.leases.CreateLeaseRequest(r.Context(), in, remoteIP(r), principal)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	out := map[string]any{"lease_request": res.LeaseRequest}
	s.publishLeaseKeys(res.Lease)
	if res.Lease != nil {
		host, port := s.sshTarget(r, res.Lease.NodeID)
		out["lease"] = res.Lease
		out["host"] = host
		out["ssh_port"] = port
		out["user"] = "servercli-ai"
	}
	writeJSON(w, status, out)
}

func (s *Server) handleGetLeaseRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.leases.LeaseRequest(r.Context(), r.PathValue("id"), tokenPrincipalFrom(r.Context()))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease_request": req})
}

func (s *Server) handleListLeaseRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	reqs, err := s.leases.ListLeaseRequests(r.Context(), s.scope(), q.Get("node_id"), q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease_requests": reqs})
}

func (s *Server) handleLeaseRenew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestedDurationSeconds int `json:"requested_duration_seconds"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	lease, err := s.leases.Renew(r.Context(), r.PathValue("id"), tokenPrincipalFrom(r.Context()), req.RequestedDurationSeconds)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	// Renewal moved the lease expiry; the node must re-install the key with a
	// freshly signed runtime token and expiry marker.
	s.publishLeaseKeys(lease)
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseHeartbeat(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Heartbeat(r.Context(), r.PathValue("id"), tokenPrincipalFrom(r.Context()))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseDisconnect(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Disconnect(r.Context(), r.PathValue("id"), tokenPrincipalFrom(r.Context()))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.publishLeaseKeys(lease)
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseRevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason            string `json:"reason"`
		TerminateSessions bool   `json:"terminate_sessions"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	admin := adminFrom(r.Context())
	lease, err := s.leases.Revoke(r.Context(), r.PathValue("id"), admin.ID, req.Reason, req.TerminateSessions)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.publishLeaseKeys(lease)
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleListLeases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	leases, err := s.leases.ListLeases(r.Context(), s.scope(), q.Get("node_id"), q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"leases": leases})
}

func (s *Server) handleAIAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewRequestsEnabled *bool  `json:"new_requests_enabled"`
		RenewalsEnabled    *bool  `json:"renewals_enabled"`
		Scope              string `json:"scope"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	if req.NewRequestsEnabled == nil && req.RenewalsEnabled == nil && req.Scope == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "no updates provided", nil)
		return
	}
	admin := adminFrom(r.Context())
	if err := s.leases.SetAIAccess(r.Context(), req.NewRequestsEnabled, req.RenewalsEnabled, req.Scope, admin.ID); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

// sshTarget returns a suggested SSH target for a lease.
// publishLeaseKeys notifies a node agent to refresh lease keys immediately
// (WebSocket push) and the admin UI that the lease list changed.
func (s *Server) publishLeaseKeys(lease *model.AILease) {
	if lease != nil {
		s.events.publishEvent(lease.NodeID, EventLeaseKeysChanged)
	}
	s.events.publishEvent("", EventLeasesChanged)
}

func (s *Server) sshTarget(r *http.Request, nodeID string) (string, int) {
	addrs, err := s.store.NodeAddresses(r.Context(), nodeID)
	if err == nil && len(addrs) > 0 {
		return addrs[0].Address, 22
	}
	return s.cfg.PrimaryServerIP, 22
}

var _ = model.LeaseActive

func (s *Server) handleLeaseDisableRenewal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	admin := adminFrom(r.Context())
	id := r.PathValue("id")
	if _, err := s.leases.Lease(r.Context(), s.scope(), id); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	lease, err := s.leases.DisableRenewal(r.Context(), id, admin.ID, req.Reason)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseProtect(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	id := r.PathValue("id")
	if _, err := s.leases.Lease(r.Context(), s.scope(), id); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	lease, err := s.leases.ProtectLease(r.Context(), id, admin.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseRevokeAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason            string `json:"reason"`
		TerminateSessions bool   `json:"terminate_sessions"`
		Scope             string `json:"scope"`
		NodeID            string `json:"node_id"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "reason is required for bulk revoke", nil)
		return
	}
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "not available on child nodes", nil)
		return
	}
	admin := adminFrom(r.Context())
	nodeID := ""
	if req.Scope == "node_id" {
		nodeID = req.NodeID
	} else if req.Scope != "" && req.Scope != "global" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "scope must be global or node_id", nil)
		return
	}
	count, revoked, err := s.leases.RevokeAll(r.Context(), nodeID, admin.ID, req.Reason, req.TerminateSessions)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	for _, l := range revoked {
		s.publishLeaseKeys(l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "revoked_count": count})
}

// handleLeaseRuntimeStatus validates a lease runtime token and returns the
// lease state for the servercli-lease-shell wrapper. The signed token binds
// lease + node + expiry, so even before the scheduler marks a lease expired,
// a past expires_at rejects the check.
func (s *Server) handleLeaseRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	master, err := service.MasterKey(s.cfg)
	if err != nil {
		writeError(w, r, s.log, http.StatusInternalServerError, "INTERNAL_ERROR", "runtime status unavailable", nil)
		return
	}
	token := r.Header.Get("X-Lease-Runtime-Token")
	leaseID, nodeID, tokenExpires, ok := service.VerifyLeaseRuntimeToken(master, token)
	if !ok || leaseID != r.PathValue("id") {
		s.auditor.Denied(r.Context(), service.AuditInput{
			ActorType: model.ActorNode, Action: "ai.lease_runtime_status", SourceIP: remoteIP(r),
			Summary: "lease runtime token invalid", RiskLevel: service.RiskHigh,
		})
		writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid lease runtime token", nil)
		return
	}
	if time.Now().UTC().After(tokenExpires) {
		writeError(w, r, s.log, http.StatusUnauthorized, "LEASE_EXPIRED", "lease runtime token expired", nil)
		return
	}
	lease, err := s.leases.Lease(r.Context(), "", leaseID)
	if err != nil {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "lease not found", nil)
		return
	}
	if lease.NodeID != nodeID || lease.Status != model.LeaseActive || !lease.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, r, s.log, http.StatusForbidden, "LEASE_UNAVAILABLE", "lease is not active", nil)
		return
	}
	// Defense in depth: even if the cascade somehow missed a lease, a revoked or
	// expired bound access token must reject new SSH connections immediately.
	if lease.AccessTokenID != "" {
		if tok, err := s.store.AccessTokenByID(r.Context(), lease.AccessTokenID); err == nil &&
			(tok.RevokedAt != nil || (tok.ExpiresAt != nil && !tok.ExpiresAt.After(time.Now().UTC()))) {
			writeError(w, r, s.log, http.StatusForbidden, "LEASE_UNAVAILABLE", "lease token revoked or expired", nil)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lease": map[string]any{
			"id":                 lease.ID,
			"status":             lease.Status,
			"node_id":            lease.NodeID,
			"permission_profile": lease.PermissionProfile,
			"expires_at":         lease.ExpiresAt,
		},
	})
}
