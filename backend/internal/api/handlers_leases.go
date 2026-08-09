package api

import (
	"net/http"
	"strconv"
	"strings"

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
	res, err := s.leases.CreateLeaseRequest(r.Context(), in, remoteIP(r))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	status := http.StatusCreated
	out := map[string]any{"lease_request": res.LeaseRequest}
	s.publishLeaseKeys(res.Lease)
	if res.Lease != nil {
		host, port := s.sshTarget(r, res.Lease.NodeID)
		out["lease"] = res.Lease
		out["renewal_token"] = res.RenewalToken
		out["host"] = host
		out["ssh_port"] = port
		out["user"] = "servercli-ai"
	}
	writeJSON(w, status, out)
}

func (s *Server) handleGetLeaseRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.leases.LeaseRequest(r.Context(), r.PathValue("id"))
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

func (s *Server) handleApproveLeaseRequest(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	res, err := s.leases.ApproveLeaseRequest(r.Context(), r.PathValue("id"), admin.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	out := map[string]any{"lease_request": res.LeaseRequest}
	s.publishLeaseKeys(res.Lease)
	if res.Lease != nil {
		out["lease"] = res.Lease
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRejectLeaseRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	admin := adminFrom(r.Context())
	if err := s.leases.RejectLeaseRequest(r.Context(), r.PathValue("id"), admin.ID, req.Reason); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
}

func (s *Server) handleLeaseRenew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestedDurationSeconds int `json:"requested_duration_seconds"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	lease, err := s.leases.Renew(r.Context(), r.PathValue("id"), bearerToken(r), req.RequestedDurationSeconds)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseHeartbeat(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Heartbeat(r.Context(), r.PathValue("id"), bearerToken(r))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func (s *Server) handleLeaseDisconnect(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Disconnect(r.Context(), r.PathValue("id"), bearerToken(r))
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
	count, err := s.leases.RevokeAll(r.Context(), nodeID, admin.ID, req.Reason, req.TerminateSessions)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "revoked_count": count})
}

// handleGetLeaseStatus returns minimal public lease status for the
// servercli-lease-shell wrapper to validate an active lease by lease ID.
func (s *Server) handleGetLeaseStatus(w http.ResponseWriter, r *http.Request) {
	lease, err := s.leases.Lease(r.Context(), "", r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
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

// handleCreateAutoApproval approves a pending request and creates (or
// extends) the device-node auto-approval rule in one atomic operation.
func (s *Server) handleCreateAutoApproval(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "auto-approval rules are managed by the primary node", nil)
		return
	}
	var req struct {
		DurationDays int `json:"duration_days"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	admin := adminFrom(r.Context())
	res, err := s.leases.AutoApproveWithDuration(r.Context(), r.PathValue("id"), admin.ID, req.DurationDays)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	out := map[string]any{
		"auto_approval": res.AutoApproval,
		"lease_request": res.LeaseRequest,
	}
	s.publishLeaseKeys(res.Lease)
	if res.Lease != nil {
		out["lease"] = res.Lease
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListAutoApprovals(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "auto-approval rules are managed by the primary node", nil)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rules, err := s.leases.ListAutoApprovals(r.Context(), "", q.Get("node_id"), q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto_approvals": rules})
}

func (s *Server) handleExtendAutoApproval(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "auto-approval rules are managed by the primary node", nil)
		return
	}
	var req struct {
		DurationDays int `json:"duration_days"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	admin := adminFrom(r.Context())
	rule, err := s.leases.ExtendAutoApproval(r.Context(), r.PathValue("id"), admin.ID, req.DurationDays)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto_approval": rule})
}
