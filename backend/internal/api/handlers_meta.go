package api

import (
	"net/http"
	"strconv"
	"time"

	"servercli/internal/model"
	"servercli/internal/service"
	"servercli/internal/store"
)

func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{
		NodeID:       q.Get("node_id"),
		ActorType:    q.Get("actor_type"),
		ActorID:      q.Get("actor_id"),
		Action:       q.Get("action"),
		Result:       q.Get("result"),
		RiskLevel:    q.Get("risk_level"),
		ResourceType: q.Get("resource_type"),
		ResourceID:   q.Get("resource_id"),
		TaskID:       q.Get("task_id"),
		LeaseID:      q.Get("lease_id"),
		SessionID:    q.Get("session_id"),
		RequestID:    q.Get("request_id"),
		IsProtected:  parseOptionalBool(q.Get("is_protected")),
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = &t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = &t
		}
	}
	if s.scope() != "" {
		if f.NodeID != "" && f.NodeID != s.scope() {
			writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "node not found", nil)
			return
		}
		f.NodeID = s.scope()
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	events, err := s.store.ListAuditEvents(r.Context(), f)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": events})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.settings.All(r.Context())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": all})
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if !decodeJSON(w, r, s.log, &updates) {
		return
	}
	admin := adminFrom(r.Context())
	all, err := s.settings.Patch(r.Context(), updates)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	s.auditor.OK(r.Context(), service.AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "settings.update",
		ResourceType: "system_setting", Summary: "system settings updated",
		Details: map[string]any{"keys": settingsKeys(updates)}, RiskLevel: service.RiskMedium,
	})
	writeJSON(w, http.StatusOK, map[string]any{"settings": all})
}

func settingsKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Server) handleCleanupRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun    bool     `json:"dry_run"`
		DataTypes []string `json:"data_types"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	admin := adminFrom(r.Context())
	run, err := s.cleanup.Run(r.Context(), service.CleanupOptions{
		DryRun:      req.DryRun,
		DataTypes:   req.DataTypes,
		RequestedBy: admin.ID,
		Trigger:     "manual",
	})
	if err != nil && run == nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	status := http.StatusOK
	if run.Status == "failed" {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]any{"cleanup_run": run})
}

func (s *Server) handleCleanupRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	runs, err := s.cleanup.ListRuns(r.Context(), limit, offset)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleanup_runs": runs})
}

// handleOpenAPI returns the unified route directory generated from the same
// specs used to register the routes, so docs cannot drift from the mux.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "ServerCLI Control Plane API",
			"version":     s.version,
			"description": "统一接口目录：管理员 Session、Access Token 与 Agent/HMAC 接口。外部 AI 自助接口使用 Authorization: Bearer sct_* Access Token。",
		},
		"paths": s.apiRoutes(),
	})
}
