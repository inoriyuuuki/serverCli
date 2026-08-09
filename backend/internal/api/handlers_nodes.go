package api

import (
	"encoding/json"
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
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	hbs, _ := s.store.LatestHeartbeats(r.Context(), ids)
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		m := nodeWithHeartbeat(n, hbs[n.ID])
		if addrs, err := s.store.NodeAddresses(r.Context(), n.ID); err == nil && len(addrs) > 0 {
			m["addresses"] = addrs
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// nodeWithHeartbeat renders a node with its latest heartbeat attached so the
// UI can show the resource summary without an extra round trip.
func nodeWithHeartbeat(n *model.Node, hb *model.NodeHeartbeat) map[string]any {
	raw, err := json.Marshal(n)
	if err != nil {
		return map[string]any{"id": n.ID, "instance_name": n.InstanceName, "status": n.Status}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"id": n.ID, "instance_name": n.InstanceName, "status": n.Status}
	}
	if hb != nil {
		m["heartbeat"] = map[string]any{
			"cpu_usage_percent":  hb.CPUUsagePercent,
			"memory_total_bytes": hb.MemoryTotalBytes,
			"memory_used_bytes":  hb.MemoryUsedBytes,
			"disk_total_bytes":   hb.DiskTotalBytes,
			"disk_used_bytes":    hb.DiskUsedBytes,
			"load_1":             hb.Load1,
			"load_5":             hb.Load5,
			"load_15":            hb.Load15,
			"uptime_seconds":     hb.UptimeSeconds,
			"time_offset_ms":     hb.TimeOffsetMS,
			"recorded_at":        hb.RecordedAt,
		}
	}
	return m
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.nodes.Node(r.Context(), s.scope(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	addrs, _ := s.store.NodeAddresses(r.Context(), n.ID)
	hb, _ := s.store.LatestHeartbeat(r.Context(), n.ID)
	m := nodeWithHeartbeat(n, hb)
	m["addresses"] = addrs
	writeJSON(w, http.StatusOK, map[string]any{"node": m, "addresses": addrs})
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
	s.events.publish("", EventNodesChanged)
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
	s.events.publish("", EventNodesChanged)
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

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfirmInstanceName string `json:"confirm_instance_name"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	admin := adminFrom(r.Context())
	if err := s.nodes.DeleteNode(r.Context(), s.scope(), r.PathValue("id"), admin.ID, req.ConfirmInstanceName); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.events.publish("", EventNodesChanged)
	w.WriteHeader(http.StatusNoContent)
}
