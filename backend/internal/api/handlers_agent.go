package api

import (
	"net/http"
	"time"

	"servercli/internal/service"
)

func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	var in service.EnrollmentInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	e, err := s.nodes.CreateEnrollment(r.Context(), in, remoteIP(r))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"enrollment": e})
}

func (s *Server) handleAgentEnrollmentStatus(w http.ResponseWriter, r *http.Request) {
	e, claimToken, err := s.nodes.Enrollment(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	resp := map[string]any{
		"enrollment": map[string]any{
			"id":                  e.ID,
			"instance_request_id": e.InstanceRequestID,
			"requested_role":      e.RequestedRole,
			"status":              e.Status,
			"created_at":          e.CreatedAt,
			"review_note":         e.ReviewNote,
			"claimed_at":          e.ClaimedAt,
		},
	}
	if claimToken != "" {
		body := resp["enrollment"].(map[string]any)
		body["claim_token"] = claimToken
		body["claim_expires_at"] = e.ClaimExpiresAt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAgentClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnrollmentID   string `json:"enrollment_id"`
		ProofSignature string `json:"proof_signature"`
		ProofTimestamp string `json:"proof_timestamp"`
		PublicKey      string `json:"public_key"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	id := r.PathValue("id")
	if req.EnrollmentID != "" && req.EnrollmentID != id {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "enrollment_id mismatch", nil)
		return
	}
	claimToken := ""
	if authz := r.Header.Get("Authorization"); len(authz) > 7 && authz[:7] == "Bearer " {
		claimToken = authz[7:]
	}
	res, err := s.nodes.ClaimEnrollment(r.Context(), id, claimToken, req.ProofSignature, req.ProofTimestamp, req.PublicKey)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":         res.NodeID,
		"node_credential": res.NodeCredential,
		"instance_name":   res.InstanceName,
	})
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in service.HeartbeatInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	node := nodeFrom(r.Context())
	res, err := s.nodes.Heartbeat(r.Context(), node.ID, in, remoteIP(r))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	// 节点状态/地址变化实时推送给管理端。
	s.events.publishEvent("", EventNodesChanged)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAgentCommandsSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Commands []service.CommandsSnapshotInput `json:"commands"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	node := nodeFrom(r.Context())
	added, err := s.nodes.CommandsSnapshot(r.Context(), node.ID, req.Commands)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "upserted": added})
}

func (s *Server) handleAgentTaskPoll(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	timeout := s.cfg.TaskPollMaxWaitSeconds
	if timeout <= 0 {
		timeout = 25
	}
	// Immediate attempt.
	payload, err := s.tasks.PollTask(r.Context(), node.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	if payload == nil {
		s.tasks.Dispatcher().Wait(node.ID, time.Duration(timeout)*time.Second)
		payload, err = s.tasks.PollTask(r.Context(), node.ID)
		if err != nil {
			writeServiceError(w, r, s.log, err)
			return
		}
	}
	cancelled, _ := s.tasks.CancelledTasks(r.Context(), node.ID)
	resp := map[string]any{"task": payload, "cancelled_tasks": cancelled}
	if payload == nil && len(cancelled) == 0 {
		resp = map[string]any{"task": nil}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAgentTaskEvent(w http.ResponseWriter, r *http.Request) {
	var in service.EventInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	node := nodeFrom(r.Context())
	if err := s.tasks.RecordEvent(r.Context(), node.ID, r.PathValue("id"), in); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.events.publishEvent("", EventTasksChanged)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleAgentTaskResult(w http.ResponseWriter, r *http.Request) {
	var in service.ResultInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	node := nodeFrom(r.Context())
	t, err := s.tasks.RecordResult(r.Context(), node.ID, r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.events.publishEvent("", EventTasksChanged)
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

func (s *Server) handleAgentLeaseEvent(w http.ResponseWriter, r *http.Request) {
	var in service.AgentLeaseEventInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	node := nodeFrom(r.Context())
	if err := s.leases.AgentLeaseEvent(r.Context(), node.ID, r.PathValue("id"), in); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
