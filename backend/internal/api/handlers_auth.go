package api

import (
	"net/http"
	"time"

	"servercli/internal/service"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "username and password required", nil)
		return
	}
	res, err := s.auth.Login(r.Context(), req.Username, req.Password, remoteIP(r), r.UserAgent())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "servercli_session",
		Value:    res.SessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(service.SessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"csrf_token": res.CSRF,
		"session": map[string]any{
			"id":         res.SessionID,
			"expires_at": time.Now().UTC().Add(service.SessionTTL),
		},
		"user": map[string]any{
			"id":       res.Admin.ID,
			"username": res.Admin.Username,
		},
		"environment": map[string]any{
			"instance_name": s.cfg.InstanceName,
			"node_role":     s.cfg.NodeRole,
			"app_env":       s.cfg.AppEnv,
		},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	_ = s.auth.Logout(r.Context(), sess, remoteIP(r))
	http.SetCookie(w, &http.Cookie{
		Name:     "servercli_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_out"})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	env := map[string]any{
		"instance_name": s.cfg.InstanceName,
		"node_role":     s.cfg.NodeRole,
		"app_env":       s.cfg.AppEnv,
		"version":       s.version,
	}
	cookie, err := r.Cookie("servercli_session")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "environment": env})
		return
	}
	sess, admin, err := s.auth.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "environment": env})
		return
	}
	nodeInfo := map[string]any{"id": "", "instance_name": s.cfg.InstanceName, "alias": ""}
	if sc := s.scope(); sc != "" {
		if n, err := s.store.NodeByID(r.Context(), sc); err == nil {
			nodeInfo["id"] = n.ID
			nodeInfo["instance_name"] = n.InstanceName
			nodeInfo["alias"] = n.Alias
		}
	} else if n, err := s.store.NodeByInstanceName(r.Context(), s.envID, s.cfg.InstanceName); err == nil {
		nodeInfo["id"] = n.ID
		nodeInfo["instance_name"] = n.InstanceName
		nodeInfo["alias"] = n.Alias
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf_token":    s.auth.CSRFFor(sess),
		"user": map[string]any{
			"id":       admin.ID,
			"username": admin.Username,
		},
		"session": map[string]any{
			"id":         sess.ID,
			"expires_at": sess.ExpiresAt,
			"last_seen":  sess.LastSeenAt,
		},
		"environment": env,
		"node":        nodeInfo,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, s.log, &req) {
		return
	}
	if req.NewPassword == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "new_password required", nil)
		return
	}
	admin := adminFrom(r.Context())
	if err := s.auth.ChangePassword(r.Context(), admin, req.OldPassword, req.NewPassword, remoteIP(r)); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "password_changed"})
}
