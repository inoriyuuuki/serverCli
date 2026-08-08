package api

import (
	"net/http"
	"strconv"

	"servercli/internal/model"
	"servercli/internal/service"
)

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var in service.CreateTaskInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	nodeID := r.PathValue("node_id")
	if s.scope() != "" && nodeID != s.scope() {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "node not found", nil)
		return
	}
	admin := adminFrom(r.Context())
	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "Idempotency-Key header required", nil)
		return
	}
	t, err := s.tasks.CreateTask(r.Context(), nodeID, admin.ID, model.ActorAdmin, idem, in)
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

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	reqNode := q.Get("node_id")
	if s.scope() != "" && reqNode != "" && reqNode != s.scope() {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "node not found", nil)
		return
	}
	tasks, err := s.tasks.ListTasks(r.Context(), s.scope(), reqNode, q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, events, output, err := s.tasks.GetTask(r.Context(), s.scope(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t, "events": events, "output": output})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	t, err := s.tasks.CancelTask(r.Context(), s.scope(), r.PathValue("id"), model.ActorAdmin, admin.ID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}
