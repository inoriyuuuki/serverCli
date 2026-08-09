package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"servercli/internal/config"
	"servercli/internal/secret"
	"servercli/internal/service"
	"servercli/internal/store"
)

// Server holds the control plane's HTTP dependencies.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	store    *store.Store
	redactor *secret.Redactor

	auth     *service.AuthService
	nodes    *service.NodeService
	tasks    *service.TaskService
	leases   *service.LeaseService
	settings *service.SettingsService
	auditor  *service.Auditor
	cleanup  *service.CleanupService

	version    string
	build      string
	commit     string
	childScope atomic.Value // string node_id when NODE_ROLE=child
	childProxy *childProxy  // forwards child self-view requests to the primary

	events *eventBroker // SSE push channel for node agents (lease key refresh)
}

// scope returns the bound node_id for child control planes ("" for primary).
func (s *Server) scope() string {
	if v := s.childScope.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetChildScope updates the node scope for a child control plane (thread-safe).
// The scope may appear after startup once the local agent claims its identity.
func (s *Server) SetChildScope(id string) { s.childScope.Store(id) }

// SetChildProxy configures the child proxy with the local node credential so
// self-view requests can be forwarded to the primary. Safe to call repeatedly;
// it is a no-op until both nodeID and credential are non-empty.
func (s *Server) SetChildProxy(nodeID, credential string) {
	if s.childProxy != nil {
		s.childProxy.set(nodeID, credential)
	}
}

// childProxyWrap routes matched self-view paths through the child proxy. The
// proxied paths are admin routes, so they require the child's own admin
// session (and CSRF for writes) exactly like the local admin handlers, and
// return 503 until the local agent has claimed its identity.
func (s *Server) childProxyWrap(next http.Handler) http.Handler {
	if s.childProxy == nil || s.cfg.NodeRole != "child" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := s.childProxy.matchPath(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if !s.childProxy.ready() {
			writeError(w, r, s.log, http.StatusServiceUnavailable, "UNAVAILABLE", "child identity not claimed yet; retry shortly", nil)
			return
		}
		s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
			s.childProxy.forward(w, r, target)
		})(w, r)
	})
}

// Options for Server construction.
type Options struct {
	Config      *config.Config
	Log         *slog.Logger
	Store       *store.Store
	Version     string
	Build       string
	Commit      string
	Redactor    *secret.Redactor
	ChildNodeID string
}

// New builds a Server with all services wired.
func New(opts Options) (*Server, error) {
	if opts.Redactor == nil {
		opts.Redactor = secret.NewRedactor()
	}
	auditor := service.NewAuditor(opts.Store, opts.Log, opts.Config.InstanceName+"-env", opts.Config.InstanceName)
	settings := service.NewSettingsService(opts.Store, opts.Config)
	master, err := service.MasterKey(opts.Config)
	if err != nil {
		return nil, err
	}
	nodes, err := service.NewNodeService(opts.Store, opts.Config, opts.Log, auditor, settings)
	if err != nil {
		return nil, err
	}
	auth := service.NewAuthService(opts.Store, opts.Log, auditor, master)
	tasks := service.NewTaskService(opts.Store, opts.Config, opts.Log, auditor, nodes)
	leases := service.NewLeaseService(opts.Store, opts.Config, opts.Log, auditor, nodes, settings)
	cleanup := service.NewCleanupService(opts.Store, opts.Config, opts.Log, auditor, settings)
	srv := &Server{
		cfg:      opts.Config,
		log:      opts.Log,
		store:    opts.Store,
		redactor: opts.Redactor,
		auth:     auth,
		nodes:    nodes,
		tasks:    tasks,
		leases:   leases,
		settings: settings,
		auditor:  auditor,
		cleanup:  cleanup,
		version:  opts.Version,
		build:    opts.Build,
		commit:   opts.Commit,
		events:   newEventBroker(),
	}
	if opts.ChildNodeID != "" {
		srv.childScope.Store(opts.ChildNodeID)
	}
	srv.childProxy = newChildProxy(opts.Log, opts.Config.PrimaryBackendURL, opts.Config.HTTPInsecureSkipVerify)
	return srv, nil
}

// SettingsService exposes settings for the cleaner/scheduler wiring.
func (s *Server) SettingsService() *service.SettingsService { return s.settings }

// CleanupService exposes cleanup for the scheduler.
func (s *Server) CleanupService() *service.CleanupService { return s.cleanup }

// NodeService exposes nodes for the scheduler.
func (s *Server) NodeService() *service.NodeService { return s.nodes }

// LeaseService exposes leases for the scheduler.
func (s *Server) LeaseService() *service.LeaseService { return s.leases }

// Store exposes the DB for the scheduler.
func (s *Server) Store() *store.Store { return s.store }

// TaskService exposes task operations for startup maintenance.
func (s *Server) TaskService() *service.TaskService { return s.tasks }

// Handler builds the full HTTP handler (API + static hosting).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health and system (no auth).
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.handleReady)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/system/info", s.handleSystemInfo)

	// Auth.
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAdmin(s.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/session", s.handleSession)
	mux.HandleFunc("POST /api/v1/auth/password", s.handleChangePassword)

	// Nodes.
	mux.HandleFunc("GET /api/v1/nodes", s.requireAdmin(s.handleListNodes))
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.requireAdmin(s.handleGetNode))
	mux.HandleFunc("PATCH /api/v1/nodes/{id}", s.requireAdmin(s.handlePatchNode))
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.requireAdmin(s.handleDeleteNode))
	mux.HandleFunc("GET /api/v1/node-enrollments", s.requireAdmin(s.handleListEnrollments))
	mux.HandleFunc("POST /api/v1/node-enrollments/{id}/approve", s.requireAdmin(s.handleApproveEnrollment))
	mux.HandleFunc("POST /api/v1/node-enrollments/{id}/reject", s.requireAdmin(s.handleRejectEnrollment))
	mux.HandleFunc("GET /api/v1/nodes/{id}/metrics", s.requireAdmin(s.handleNodeMetrics))
	mux.HandleFunc("GET /api/v1/nodes/{id}/commands", s.requireAdmin(s.handleNodeCommands))

	// Agent (signature-authenticated, except enrollment).
	mux.HandleFunc("POST /api/v1/agent/enrollments", s.handleAgentEnroll)
	mux.HandleFunc("GET /api/v1/agent/enrollments/{id}", s.handleAgentEnrollmentStatus)
	mux.HandleFunc("POST /api/v1/agent/enrollments/{id}/claim", s.handleAgentClaim)
	mux.HandleFunc("POST /api/v1/agent/heartbeat", s.agentAuth(s.handleAgentHeartbeat))
	mux.HandleFunc("POST /api/v1/agent/commands/snapshot", s.agentAuth(s.handleAgentCommandsSnapshot))
	mux.HandleFunc("GET /api/v1/agent/tasks/poll", s.agentAuth(s.handleAgentTaskPoll))
	mux.HandleFunc("POST /api/v1/agent/tasks/{id}/events", s.agentAuth(s.handleAgentTaskEvent))
	mux.HandleFunc("POST /api/v1/agent/tasks/{id}/result", s.agentAuth(s.handleAgentTaskResult))
	mux.HandleFunc("POST /api/v1/agent/leases/{id}/events", s.agentAuth(s.handleAgentLeaseEvent))
	mux.HandleFunc("GET /api/v1/agent/events", s.agentAuth(s.handleAgentEvents))
	// Agent self-service (scoped to the calling node): lets a child control
	// plane mirror its own commands/tasks/leases/audit from the primary.
	mux.HandleFunc("GET /api/v1/agent/commands", s.agentAuth(s.handleAgentListCommands))
	mux.HandleFunc("GET /api/v1/agent/tasks", s.agentAuth(s.handleAgentListTasks))
	mux.HandleFunc("GET /api/v1/agent/tasks/{id}", s.agentAuth(s.handleAgentGetTask))
	mux.HandleFunc("POST /api/v1/agent/tasks", s.agentAuth(s.handleAgentCreateTask))
	mux.HandleFunc("POST /api/v1/agent/tasks/{id}/cancel", s.agentAuth(s.handleAgentCancelTask))
	mux.HandleFunc("GET /api/v1/agent/leases", s.agentAuth(s.handleAgentListLeases))
	mux.HandleFunc("GET /api/v1/agent/lease-requests", s.agentAuth(s.handleAgentListLeaseRequests))
	mux.HandleFunc("GET /api/v1/agent/audit-events", s.agentAuth(s.handleAgentListAuditEvents))
	mux.HandleFunc("GET /api/v1/agent/task-parameter-histories", s.agentAuth(s.handleAgentListTaskParameterHistories))
	mux.HandleFunc("DELETE /api/v1/agent/task-parameter-histories/{id}", s.agentAuth(s.handleAgentDeleteTaskParameterHistory))

	// Commands discovery.
	mux.HandleFunc("GET /api/v1/commands", s.requireAdmin(s.handleListCommands))

	// Tasks.
	mux.HandleFunc("POST /api/v1/nodes/{node_id}/tasks", s.requireAdmin(s.handleCreateTask))
	mux.HandleFunc("GET /api/v1/tasks", s.requireAdmin(s.handleListTasks))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.requireAdmin(s.handleGetTask))
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.requireAdmin(s.handleCancelTask))
	mux.HandleFunc("GET /api/v1/task-parameter-histories", s.requireAdmin(s.handleListTaskParameterHistories))
	mux.HandleFunc("DELETE /api/v1/task-parameter-histories/{id}", s.requireAdmin(s.handleDeleteTaskParameterHistory))

	// AI leases.
	mux.HandleFunc("POST /api/v1/ai/lease-requests", s.handleCreateLeaseRequest)
	mux.HandleFunc("GET /api/v1/ai/lease-requests/{id}", s.handleGetLeaseRequest)
	mux.HandleFunc("GET /api/v1/ai/leases/{id}/status", s.handleGetLeaseStatus)
	mux.HandleFunc("GET /api/v1/ai/lease-requests", s.requireAdmin(s.handleListLeaseRequests))
	mux.HandleFunc("POST /api/v1/ai/lease-requests/{id}/approve", s.requireAdmin(s.handleApproveLeaseRequest))
	mux.HandleFunc("POST /api/v1/ai/lease-requests/{id}/auto-approval", s.requireAdmin(s.handleCreateAutoApproval))
	mux.HandleFunc("GET /api/v1/ai/auto-approvals", s.requireAdmin(s.handleListAutoApprovals))
	mux.HandleFunc("POST /api/v1/ai/auto-approvals/{id}/extend", s.requireAdmin(s.handleExtendAutoApproval))
	mux.HandleFunc("POST /api/v1/ai/lease-requests/{id}/reject", s.requireAdmin(s.handleRejectLeaseRequest))
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/renew", s.handleLeaseRenew)
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/heartbeat", s.handleLeaseHeartbeat)
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/disconnect", s.handleLeaseDisconnect)
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/revoke", s.requireAdmin(s.handleLeaseRevoke))
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/disable-renewal", s.requireAdmin(s.handleLeaseDisableRenewal))
	mux.HandleFunc("POST /api/v1/ai/leases/{id}/protect", s.requireAdmin(s.handleLeaseProtect))
	mux.HandleFunc("POST /api/v1/ai/leases/revoke-all", s.requireAdmin(s.handleLeaseRevokeAll))
	mux.HandleFunc("GET /api/v1/ai/leases", s.requireAdmin(s.handleListLeases))
	mux.HandleFunc("PATCH /api/v1/settings/ai-access", s.requireAdmin(s.handleAIAccess))

	// Audit / settings / cleanup.
	mux.HandleFunc("GET /api/v1/audit-events", s.requireAdmin(s.handleListAuditEvents))
	mux.HandleFunc("GET /api/v1/settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("PATCH /api/v1/settings", s.requireAdmin(s.handlePatchSettings))
	mux.HandleFunc("POST /api/v1/cleanup/run", s.requireAdmin(s.handleCleanupRun))
	mux.HandleFunc("GET /api/v1/cleanup/runs", s.requireAdmin(s.handleCleanupRuns))

	// Static frontend + SPA fallback. On child control planes the scoped
	// self-view requests are proxied to the primary before reaching the mux.
	return s.wrap(s.withFrontend(s.childProxyWrap(mux)))
}

// wrap applies global middleware around the handler.
func (s *Server) wrap(next http.Handler) http.Handler {
	h := s.securityHeaders(next)
	h = s.recoverPanic(h)
	h = s.logRequests(h)
	h = s.requestID(h)
	return h
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = "req_" + randomID()
		}
		w.Header().Set("X-Request-ID", rid)
		ctx := withRequestID(r.Context(), rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		before := s.redactor.Count()
		next.ServeHTTP(sw, r)
		redacted := s.redactor.Count() - before
		rid := requestIDFrom(r.Context())
		s.log.Info("http",
			"request_id", rid,
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", remoteIP(r),
			"redaction_count", redacted,
		)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "panic", fmt.Sprint(rec), "request_id", requestIDFrom(r.Context()))
				writeError(w, r, s.log, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		if s.cfg.AppEnv == "production" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// withFrontend serves the static frontend when present, with SPA fallback.
// The handler may be a wrapped mux (e.g. the child proxy) so API paths still
// reach it before falling through to the SPA.
func (s *Server) withFrontend(mux http.Handler) http.Handler {
	dist := s.cfg.FrontendDistDir
	info, err := os.Stat(dist)
	if err != nil || !info.IsDir() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// API routes were already matched by the mux; unknown routes get a hint.
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health") || r.URL.Path == "/version" {
				mux.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ServerCLI control plane API is running. Frontend build not found at " + dist + ". Build frontend/ to serve the UI."))
		})
	}
	fileServer := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health") || r.URL.Path == "/version" {
			mux.ServeHTTP(w, r)
			return
		}
		path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if path == "." || path == "/" {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		full := filepath.Join(dist, path)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback.
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

// statusWriter captures the response status for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so SSE endpoints work through the
// request-logging wrapper (statusWriter must implement http.Flusher).
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// context helpers
type ctxKey struct{}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func requestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}
