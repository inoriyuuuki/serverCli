package api

import (
	"net/http"
	"strconv"

	"servercli/internal/model"
	"servercli/internal/service"
)

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	scope := s.scope()
	q := r.URL.Query()
	enabled := parseOptionalBool(q.Get("enabled"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	nodes, err := s.nodes.ListNodes(r.Context(), scope, q.Get("role"), q.Get("status"), enabled, q.Get("keyword"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.nodes.Node(r.Context(), s.scope(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	addrs, _ := s.store.NodeAddresses(r.Context(), n.ID)
	writeJSON(w, http.StatusOK, map[string]any{"node": n, "addresses": addrs})
}

func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	var patch service.NodePatch
	if !decodeJSON(w, r, s.log, &patch) {
		return
	}
	admin := adminFrom(r.Context())
	n, err := s.nodes.PatchNode(r.Context(), s.scope(), r.PathValue("id"), admin.ID, patch)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": n})
}

func (s *Server) handleListEnrollments(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "enrollments are managed by the primary node", nil)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	enrollments, err := s.store.ListEnrollments(r.Context(), q.Get("status"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrollments": enrollments})
}

func (s *Server) handleApproveEnrollment(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "enrollments are managed by the primary node", nil)
		return
	}
	var req struct {
		ReviewNote string `json:"review_note"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	admin := adminFrom(r.Context())
	e, err := s.nodes.ApproveEnrollment(r.Context(), r.PathValue("id"), admin.ID, req.ReviewNote)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrollment": e})
}

func (s *Server) handleRejectEnrollment(w http.ResponseWriter, r *http.Request) {
	if s.scope() != "" {
		writeError(w, r, s.log, http.StatusForbidden, "FORBIDDEN", "enrollments are managed by the primary node", nil)
		return
	}
	var req struct {
		ReviewNote string `json:"review_note"`
	}
	_ = decodeJSON(w, r, s.log, &req)
	admin := adminFrom(r.Context())
	if err := s.nodes.RejectEnrollment(r.Context(), r.PathValue("id"), admin.ID, req.ReviewNote); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rejected"})
}

func (s *Server) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	metrics, err := s.nodes.Metrics(r.Context(), s.scope(), r.PathValue("id"), limit)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) handleNodeCommands(w http.ResponseWriter, r *http.Request) {
	cmds, err := s.nodes.NodeCommands(r.Context(), s.scope(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	nodeID := q.Get("node_id")
	if s.scope() != "" && nodeID != "" && nodeID != s.scope() {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "node not found", nil)
		return
	}
	if s.scope() != "" {
		nodeID = s.scope()
	}
	cmds, err := s.store.SearchCommands(r.Context(), nodeID, q.Get("category"), q.Get("keyword"), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}

func parseOptionalBool(v string) *bool {
	switch v {
	case "true", "1":
		b := true
		return &b
	case "false", "0":
		b := false
		return &b
	}
	return nil
}

var _ = model.NodeStatusOnline
