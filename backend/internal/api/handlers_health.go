package api

import (
	"context"
	"net/http"
	"runtime"
	"time"
)

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.DB().PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "database": "error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"database":       "ok",
		"schema_version": s.store.SchemaVersion(ctx),
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.version,
		"build":          s.build,
		"commit":         s.commit,
		"go_version":     runtime.Version(),
		"schema_version": s.store.SchemaVersion(r.Context()),
	})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	auth := false
	if cookie, err := r.Cookie("servercli_session"); err == nil {
		if _, _, err := s.auth.Authenticate(r.Context(), cookie.Value); err == nil {
			auth = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_name":       s.cfg.InstanceName,
		"node_role":           s.cfg.NodeRole,
		"environment":         s.cfg.AppEnv,
		"version":             s.version,
		"build":               s.build,
		"commit":              s.commit,
		"primary_backend_url": s.cfg.PrimaryBackendURL,
		"frontend_addr":       s.cfg.FrontendAddr,
		"backend_addr":        s.cfg.BackendAddr,
		"authenticated":       auth,
		"server_time":         time.Now().UTC(),
	})
}
