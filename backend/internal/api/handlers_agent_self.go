package api

import (
	"net/http"
	"strconv"
	"strings"

	"servercli/internal/model"
	"servercli/internal/service"
	"servercli/internal/store"
)

// Agent self-service endpoints. These are signature-authenticated (agentAuth)
// and always scoped to the calling node, so a child control plane can mirror
// its own commands/tasks/leases/audit from the primary without admin access.

func (s *Server) handleAgentListCommands(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	// Only enabled commands, mirroring the admin /commands discovery (the
	// child UI must not see or execute disabled commands).
	cmds, err := s.store.SearchCommands(r.Context(), node.ID, q.Get("category"), q.Get("keyword"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}

func (s *Server) handleAgentListTasks(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	tasks, err := s.tasks.ListTasks(r.Context(), node.ID, "", q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleAgentGetTask(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	t, events, output, err := s.tasks.GetTask(r.Context(), node.ID, r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t, "events": events, "output": output})
}

// handleAgentCreateTask lets a node queue a task against itself (self-execute).
// Scope is enforced by CreateTask (the node id is forced to the caller) and the
// command must be registered on that same node. Idempotency-Key is required,
// matching the admin task-creation endpoint.
//
// Deployment commands ("deployment.*") are deliberately rejected here: they
// carry release/secret material and may only be created by the control plane
// (DeploymentService), never self-executed by a node. The denial is audited.
func (s *Server) handleAgentCreateTask(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	var in service.CreateTaskInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.HasPrefix(in.CommandID, "deployment.") {
		s.auditor.Denied(r.Context(), service.AuditInput{
			ActorType:    model.ActorNode,
			ActorID:      node.ID,
			NodeID:       node.ID,
			Action:       "agent.self-execute.denied",
			ResourceType: "task",
			Summary:      "agent self-execute of deployment command denied",
			Details:      map[string]any{"command_id": in.CommandID, "node_id": node.ID},
		})
		writeServiceError(w, r, s.log, service.ErrForbidden)
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "Idempotency-Key header required", nil)
		return
	}
	t, err := s.tasks.CreateTask(r.Context(), node.ID, node.ID, model.ActorNode, idem, in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": map[string]any{
		"id":         t.ID,
		"status":     t.Status,
		"node_id":    t.NodeID,
		"created_at": t.QueuedAt,
	}})
}

func (s *Server) handleAgentCancelTask(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	t, err := s.tasks.CancelTask(r.Context(), node.ID, r.PathValue("id"), model.ActorNode, node.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

func (s *Server) handleAgentListLeases(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	leases, err := s.leases.ListLeases(r.Context(), node.ID, "", q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"leases": leases})
}

func (s *Server) handleAgentListLeaseRequests(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	reqs, err := s.leases.ListLeaseRequests(r.Context(), node.ID, "", q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lease_requests": reqs})
}

func (s *Server) handleAgentListAuditEvents(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	f := store.AuditFilter{
		NodeID:    node.ID,
		ActorType: q.Get("actor_type"),
		ActorID:   q.Get("actor_id"),
		Action:    q.Get("action"),
		Result:    q.Get("result"),
		RiskLevel: q.Get("risk_level"),
		TaskID:    q.Get("task_id"),
		LeaseID:   q.Get("lease_id"),
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	events, err := s.store.ListAuditEvents(r.Context(), f)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.enrichAuditNames(r.Context(), events...)
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": events})
}

func (s *Server) handleAgentListTaskParameterHistories(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	histories, err := s.tasks.ListParameterHistories(r.Context(), node.ID, nil, q.Get("command_id"), q.Get("command_version"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"histories": histories})
}

func (s *Server) handleAgentDeleteTaskParameterHistory(w http.ResponseWriter, r *http.Request) {
	node := nodeFrom(r.Context())
	if err := s.tasks.DeleteParameterHistory(r.Context(), node.ID, r.PathValue("id"), node.ID); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
