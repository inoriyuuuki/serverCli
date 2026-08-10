package api

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
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
	tokens   *service.TokenService
	settings *service.SettingsService
	auditor  *service.Auditor
	cleanup  *service.CleanupService

	notifications *service.NotificationService
	notifyLimiter *service.NotificationLimiter

	version    string
	build      string
	commit     string
	envID      string
	routes     []RouteSpec
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
	tokens := service.NewTokenService(opts.Store, opts.Config, opts.Log, auditor, nodes)
	cleanup := service.NewCleanupService(opts.Store, opts.Config, opts.Log, auditor, settings)
	notifyLimiter := service.NewNotificationLimiter(opts.Config.NotificationRateLimitPerTokenPerMinute, opts.Config.NotificationRateLimitGlobalPerMinute)
	notifications := service.NewNotificationService(opts.Config, opts.Log, auditor, notifyLimiter)
	srv := &Server{
		cfg:      opts.Config,
		log:      opts.Log,
		store:    opts.Store,
		redactor: opts.Redactor,
		auth:     auth,
		nodes:    nodes,
		tasks:    tasks,
		leases:   leases,
		tokens:   tokens,
		settings: settings,
		auditor:  auditor,
		cleanup:  cleanup,

		notifications: notifications,
		notifyLimiter: notifyLimiter,
		version:       opts.Version,
		build:         opts.Build,
		commit:        opts.Commit,
		envID:         opts.Config.InstanceName + "-env",
		events:        newEventBroker(),
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

// NotificationLimiter exposes the notification rate limiter for the cleanup
// ticker wiring in main.
func (s *Server) NotificationLimiter() *service.NotificationLimiter { return s.notifyLimiter }

// NotificationService exposes the notification service for internal senders.
func (s *Server) NotificationService() *service.NotificationService { return s.notifications }

// TokenService exposes access-token management for startup hygiene scans.
func (s *Server) TokenService() *service.TokenService { return s.tokens }

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
	s.register(mux, RouteSpec{Method: "GET", Path: "/health/live", Group: "健康检查", Auth: AuthNone, Summary: "存活探针", Debug: false}, s.handleLive)
	s.register(mux, RouteSpec{Method: "GET", Path: "/health/ready", Group: "健康检查", Auth: AuthNone, Summary: "就绪探针", Debug: false}, s.handleReady)
	s.register(mux, RouteSpec{Method: "GET", Path: "/version", Group: "系统", Auth: AuthNone, Summary: "版本信息", Debug: false}, s.handleVersion)
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/system/info", Group: "系统", Auth: AuthNone, Summary: "系统信息（环境/角色/实例）", Debug: false}, s.handleSystemInfo)

	// Auth.
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/auth/login", Group: "认证", Auth: AuthNone, Summary: "管理员登录", Body: `{"username":"admin","password":"..."}`, Errors: []string{"401"}, Debug: false}, s.handleLogin)
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/auth/logout", Group: "认证", Auth: AuthAdmin, Summary: "注销", Debug: false}, s.requireAdmin(s.handleLogout))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/auth/session", Group: "认证", Auth: AuthNone, Summary: "当前会话与 CSRF", Debug: false}, s.handleSession)
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/auth/password", Group: "认证", Auth: AuthAdmin, Summary: "修改密码", Debug: false}, s.handleChangePassword)

	// Nodes.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/nodes", Group: "节点", Auth: AuthAdminOrToken, Summary: "节点列表（管理员 Session 或 Access Token）", Params: []RouteParam{{Name: "limit", In: "query", Type: "integer"}}, Debug: true}, s.adminOrToken(service.ResourceNodes, service.ActionRead, "/api/v1/nodes")(s.handleListNodes))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/nodes/{id}", Group: "节点", Auth: AuthAdminOrToken, Summary: "节点详情（管理员 Session 或 Access Token）", Debug: true}, s.adminOrToken(service.ResourceNodes, service.ActionRead, "/api/v1/nodes/{id}")(s.handleGetNode))
	s.register(mux, RouteSpec{Method: "PATCH", Path: "/api/v1/nodes/{id}", Group: "节点", Auth: AuthAdmin, Summary: "更新节点（启用/停用/别名等）", Debug: true}, s.requireAdmin(s.handlePatchNode))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/nodes/{id}", Group: "节点", Auth: AuthAdmin, Summary: "删除节点（级联清理）", Errors: []string{"409"}, Debug: true}, s.requireAdmin(s.handleDeleteNode))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/node-enrollments", Group: "节点", Auth: AuthAdmin, Summary: "注册申请列表", Debug: true}, s.requireAdmin(s.handleListEnrollments))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/node-enrollments/{id}/approve", Group: "节点", Auth: AuthAdmin, Summary: "批准节点注册", Debug: true}, s.requireAdmin(s.handleApproveEnrollment))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/node-enrollments/{id}/reject", Group: "节点", Auth: AuthAdmin, Summary: "拒绝节点注册", Debug: true}, s.requireAdmin(s.handleRejectEnrollment))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/nodes/{id}/metrics", Group: "节点", Auth: AuthAdmin, Summary: "节点指标", Debug: true}, s.requireAdmin(s.handleNodeMetrics))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/nodes/{id}/commands", Group: "节点", Auth: AuthAdmin, Summary: "节点命令注册表", Debug: true}, s.requireAdmin(s.handleNodeCommands))

	// Agent (signature-authenticated, except enrollment).
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/enrollments", Group: "Agent", Auth: AuthNone, Summary: "节点注册申请", Debug: false}, s.handleAgentEnroll)
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/enrollments/{id}", Group: "Agent", Auth: AuthNone, Summary: "注册申请状态", Debug: false}, s.handleAgentEnrollmentStatus)
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/enrollments/{id}/claim", Group: "Agent", Auth: AuthNone, Summary: "认领节点身份", Debug: false}, s.handleAgentClaim)
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/heartbeat", Group: "Agent", Auth: AuthAgent, Summary: "节点心跳（含 Lease 密钥指令）", Debug: false}, s.agentAuth(s.handleAgentHeartbeat))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/commands/snapshot", Group: "Agent", Auth: AuthAgent, Summary: "命令注册表快照", Debug: false}, s.agentAuth(s.handleAgentCommandsSnapshot))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/tasks/poll", Group: "Agent", Auth: AuthAgent, Summary: "任务拉取", Debug: false}, s.agentAuth(s.handleAgentTaskPoll))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/tasks/{id}/events", Group: "Agent", Auth: AuthAgent, Summary: "任务事件上报", Debug: false}, s.agentAuth(s.handleAgentTaskEvent))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/tasks/{id}/result", Group: "Agent", Auth: AuthAgent, Summary: "任务结果上报", Debug: false}, s.agentAuth(s.handleAgentTaskResult))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/leases/{id}/events", Group: "Agent", Auth: AuthAgent, Summary: "Lease 生命周期事件上报", Debug: false}, s.agentAuth(s.handleAgentLeaseEvent))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/ws", Group: "Agent", Auth: AuthAgent, Summary: "Agent 实时通道（WebSocket）", Debug: false}, s.agentAuth(s.handleAgentWS))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/ws", Group: "Agent", Auth: AuthAdmin, Summary: "管理端实时通道（WebSocket）", Debug: false}, s.handleAdminWS)
	// Agent self-service (scoped to the calling node).
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/commands", Group: "Agent", Auth: AuthAgent, Summary: "本节点命令列表", Debug: false}, s.agentAuth(s.handleAgentListCommands))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/tasks", Group: "Agent", Auth: AuthAgent, Summary: "本节点任务列表", Debug: false}, s.agentAuth(s.handleAgentListTasks))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/tasks/{id}", Group: "Agent", Auth: AuthAgent, Summary: "本节点任务详情", Debug: false}, s.agentAuth(s.handleAgentGetTask))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/tasks", Group: "Agent", Auth: AuthAgent, Summary: "本节点创建任务", Debug: false}, s.agentAuth(s.handleAgentCreateTask))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/tasks/{id}/cancel", Group: "Agent", Auth: AuthAgent, Summary: "取消本节点任务", Debug: false}, s.agentAuth(s.handleAgentCancelTask))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/leases", Group: "Agent", Auth: AuthAgent, Summary: "本节点 Lease 列表", Debug: false}, s.agentAuth(s.handleAgentListLeases))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/lease-requests", Group: "Agent", Auth: AuthAgent, Summary: "本节点 Lease 申请列表", Debug: false}, s.agentAuth(s.handleAgentListLeaseRequests))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/audit-events", Group: "Agent", Auth: AuthAgent, Summary: "本节点审计", Debug: false}, s.agentAuth(s.handleAgentListAuditEvents))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/agent/task-parameter-histories", Group: "Agent", Auth: AuthAgent, Summary: "本节点参数历史", Debug: false}, s.agentAuth(s.handleAgentListTaskParameterHistories))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/agent/task-parameter-histories/{id}", Group: "Agent", Auth: AuthAgent, Summary: "删除参数历史", Debug: false}, s.agentAuth(s.handleAgentDeleteTaskParameterHistory))

	// Commands discovery.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/commands", Group: "命令", Auth: AuthAdmin, Summary: "命令目录", Debug: true}, s.requireAdmin(s.handleListCommands))

	// Tasks.
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/nodes/{node_id}/tasks", Group: "任务", Auth: AuthAdmin, Summary: "创建任务", Debug: true}, s.requireAdmin(s.handleCreateTask))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/tasks", Group: "任务", Auth: AuthAdmin, Summary: "任务列表", Debug: true}, s.requireAdmin(s.handleListTasks))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/tasks/{id}", Group: "任务", Auth: AuthAdmin, Summary: "任务详情", Debug: true}, s.requireAdmin(s.handleGetTask))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/tasks/{id}/cancel", Group: "任务", Auth: AuthAdmin, Summary: "取消任务", Debug: true}, s.requireAdmin(s.handleCancelTask))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/task-parameter-histories", Group: "任务", Auth: AuthAdmin, Summary: "参数历史列表", Debug: true}, s.requireAdmin(s.handleListTaskParameterHistories))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/task-parameter-histories/{id}", Group: "任务", Auth: AuthAdmin, Summary: "删除参数历史", Debug: true}, s.requireAdmin(s.handleDeleteTaskParameterHistory))

	// AI leases (external AI self-service is access-token authenticated; the
	// remaining admin routes manage leases/requests).
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/lease-requests", Group: "AI Lease", Auth: AuthToken, Summary: "申请 Lease（自动审批）", Body: `{"node_selector":"<node_id>","public_key":"ssh-ed25519 AAAA...","permission_profile":"read-only","requested_duration_seconds":3600,"purpose":"...","client_request_id":"..."}`, Errors: []string{"401", "403"}, Debug: true}, s.tokenAuth(service.ResourceLeaseRequests, service.ActionCreate, "/api/v1/ai/lease-requests")(s.handleCreateLeaseRequest))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/ai/lease-requests/{id}", Group: "AI Lease", Auth: AuthToken, Summary: "查询本人申请", Errors: []string{"401", "404"}, Debug: true}, s.tokenAuth(service.ResourceLeaseRequests, service.ActionRead, "/api/v1/ai/lease-requests/{id}")(s.handleGetLeaseRequest))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/renew", Group: "AI Lease", Auth: AuthToken, Summary: "续期本人 Lease", Body: `{"requested_duration_seconds":3600}`, Errors: []string{"401", "403", "404"}, Debug: true}, s.tokenAuth(service.ResourceLeases, service.ActionRenew, "/api/v1/ai/leases/{id}/renew")(s.handleLeaseRenew))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/heartbeat", Group: "AI Lease", Auth: AuthToken, Summary: "Lease 心跳", Errors: []string{"401", "404"}, Debug: true}, s.tokenAuth(service.ResourceLeases, service.ActionHeartbeat, "/api/v1/ai/leases/{id}/heartbeat")(s.handleLeaseHeartbeat))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/disconnect", Group: "AI Lease", Auth: AuthToken, Summary: "正常断开 Lease", Errors: []string{"401", "404"}, Debug: true}, s.tokenAuth(service.ResourceLeases, service.ActionDisconnect, "/api/v1/ai/leases/{id}/disconnect")(s.handleLeaseDisconnect))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/ai/leases/{id}/status", Group: "AI Lease", Auth: AuthRuntime, Summary: "Lease 运行时状态（签名令牌）", Debug: false}, s.handleLeaseRuntimeStatus)
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/ai/lease-requests", Group: "AI Lease", Auth: AuthAdmin, Summary: "申请列表（管理）", Debug: true}, s.requireAdmin(s.handleListLeaseRequests))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/revoke", Group: "AI Lease", Auth: AuthAdmin, Summary: "撤销 Lease", Debug: true}, s.requireAdmin(s.handleLeaseRevoke))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/disable-renewal", Group: "AI Lease", Auth: AuthAdmin, Summary: "禁止续期", Debug: true}, s.requireAdmin(s.handleLeaseDisableRenewal))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/{id}/protect", Group: "AI Lease", Auth: AuthAdmin, Summary: "标记重要", Debug: true}, s.requireAdmin(s.handleLeaseProtect))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/ai/leases/revoke-all", Group: "AI Lease", Auth: AuthAdmin, Summary: "紧急撤销（全局/节点）", Debug: true}, s.requireAdmin(s.handleLeaseRevokeAll))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/ai/leases", Group: "AI Lease", Auth: AuthAdmin, Summary: "Lease 列表（管理）", Debug: true}, s.requireAdmin(s.handleListLeases))
	s.register(mux, RouteSpec{Method: "PATCH", Path: "/api/v1/settings/ai-access", Group: "AI Lease", Auth: AuthAdmin, Summary: "AI 申请/续期开关", Debug: true}, s.requireAdmin(s.handleAIAccess))

	// Notifications (external AI self-service, token-authenticated).
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/notifications/send", Group: "通知", Auth: AuthToken, Summary: "发送通知", Body: `{"title":"...","message":"...","level":"info","channel":"default"}`, Errors: []string{"400", "401", "403", "429", "502", "503"}, Debug: true}, s.tokenAuthWith(service.ResourceNotifications, service.ActionSend, "/api/v1/notifications/send", tokenAuthOptions{
		afterResolve:    s.notificationRateHook(),
		outcomeOverride: notificationOutcomeOverride,
	})(s.handleNotificationSend))
	s.register(mux, RouteSpec{Method: "GET", Path: "/notice", Group: "通知", Auth: AuthToken, Summary: "兼容通知接口（GET 参数 method/message/logLevel）", Debug: true}, s.tokenAuthWith(service.ResourceNotifications, service.ActionSend, "/notice", tokenAuthOptions{
		afterResolve:    s.notificationRateHook(),
		outcomeOverride: notificationOutcomeOverride,
	})(s.handleNotice))

	// Access token management (primary only).
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/api-tokens", Group: "Token", Auth: AuthAdmin, Summary: "创建 Access Token（仅返回一次明文）", Body: `{"name":"my-agent","ttl":"1h"}`, Errors: []string{"400"}, Debug: true}, s.requireAdmin(s.handleCreateAPIToken))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/api-tokens", Group: "Token", Auth: AuthAdmin, Summary: "Token 列表", Debug: true}, s.requireAdmin(s.handleListAPITokens))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/api-tokens/{id}", Group: "Token", Auth: AuthAdmin, Summary: "Token 详情", Debug: true}, s.requireAdmin(s.handleGetAPIToken))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/api-tokens/{id}/revoke", Group: "Token", Auth: AuthAdmin, Summary: "撤销 Token（级联撤销 Lease）", Body: `{"reason":"..."}`, Debug: true}, s.requireAdmin(s.handleRevokeAPIToken))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/api-tokens/{id}/usage-logs", Group: "Token", Auth: AuthAdmin, Summary: "Token 使用日志", Params: []RouteParam{{Name: "outcome", In: "query", Type: "string"}}, Debug: true}, s.requireAdmin(s.handleListTokenUsageLogs))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/api-tokens/permissions/catalog", Group: "Token", Auth: AuthAdmin, Summary: "权限目录（分类 + 权限定义）", Debug: true}, s.requireAdmin(s.handlePermissionCatalog))
	s.register(mux, RouteSpec{Method: "PUT", Path: "/api/v1/api-tokens/{id}/permissions", Group: "Token", Auth: AuthAdmin, Summary: "更新 Token 权限（乐观锁）", Body: `{"permission_version":1,"permissions":{"version":1,"grants":[...]}}`, Errors: []string{"400", "404", "409"}, Debug: true}, s.requireAdmin(s.handleUpdateAPITokenPermissions))

	// Interface directory / OpenAPI.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/meta/openapi", Group: "系统", Auth: AuthAdmin, Summary: "全接口目录（OpenAPI）", Debug: true}, s.requireAdmin(s.handleOpenAPI))

	// Audit / settings / cleanup.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/audit-events", Group: "审计", Auth: AuthAdmin, Summary: "审计日志", Debug: true}, s.requireAdmin(s.handleListAuditEvents))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/settings", Group: "设置", Auth: AuthAdmin, Summary: "系统设置", Debug: true}, s.requireAdmin(s.handleGetSettings))
	s.register(mux, RouteSpec{Method: "PATCH", Path: "/api/v1/settings", Group: "设置", Auth: AuthAdmin, Summary: "更新系统设置", Debug: true}, s.requireAdmin(s.handlePatchSettings))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/cleanup/run", Group: "设置", Auth: AuthAdmin, Summary: "手动触发清理", Debug: true}, s.requireAdmin(s.handleCleanupRun))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/cleanup/runs", Group: "设置", Auth: AuthAdmin, Summary: "清理记录", Debug: true}, s.requireAdmin(s.handleCleanupRuns))

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
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health") || r.URL.Path == "/version" || r.URL.Path == "/notice" {
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
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health") || r.URL.Path == "/version" || r.URL.Path == "/notice" {
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

// Flush forwards to the underlying writer so streaming endpoints work through
// the request-logging wrapper (statusWriter must implement http.Flusher).
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer so WebSocket upgrades work through
// the request-logging wrapper (statusWriter must implement http.Hijacker).
func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := sw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
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
