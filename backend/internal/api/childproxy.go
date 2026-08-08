package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"servercli/internal/agent"
)

// maxProxyBodyBytes caps upstream responses proxied to the child UI (task
// output is capped at 256 KiB per command, 16 MiB is generous headroom).
const maxProxyBodyBytes = 16 << 20

// childProxy forwards a child control plane's scoped self-view requests to the
// primary's agent self-service endpoints using the node credential. This lets
// the child UI display its own commands/tasks/leases/audit with live,
// primary-authoritative data (no local mirror).
//
// Authentication is NOT handled here: the caller (Server.childProxyWrap)
// requires the child's own admin session and CSRF before forwarding.
type childProxy struct {
	mu        sync.RWMutex
	enabled   bool
	nodeID    string
	baseURL   string
	insecure  bool
	cred      string
	client    *agent.Client
	transport http.RoundTripper // optional test hook
	log       *slog.Logger
}

func newChildProxy(log *slog.Logger, baseURL string, insecure bool) *childProxy {
	return &childProxy{log: log, baseURL: baseURL, insecure: insecure}
}

// set (re)configures the proxy for a claimed node identity. Passing empty
// credentials disables proxying.
func (p *childProxy) set(nodeID, credential string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if nodeID == "" || credential == "" {
		p.enabled = false
		p.nodeID = ""
		p.client = nil
		return
	}
	if p.enabled && p.cred == credential {
		return
	}
	p.nodeID = nodeID
	p.cred = credential
	c := agent.NewClient(p.baseURL, p.insecure, 60*time.Second)
	if p.transport != nil {
		c.SetTransport(p.transport)
	}
	c.SetCredential(credential)
	p.client = c
	p.enabled = true
}

// setTransport overrides the upstream transport (tests only). It rebuilds the
// agent client when a credential is already configured.
func (p *childProxy) setTransport(rt http.RoundTripper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transport = rt
	if p.cred != "" {
		c := agent.NewClient(p.baseURL, p.insecure, 60*time.Second)
		c.SetTransport(rt)
		c.SetCredential(p.cred)
		p.client = c
	}
}

// ready reports whether the proxy has a claimed identity to forward with.
func (p *childProxy) ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.enabled && p.client != nil
}

// matchPath maps a child admin request to the primary agent self-service path.
// Only self-view read paths plus self task creation/cancellation are proxied;
// cluster management stays on the primary. A node_id that is not this child
// falls through to the local (scoped) handlers, which reject it with 404.
func (p *childProxy) matchPath(r *http.Request) (string, bool) {
	p.mu.RLock()
	nodeID := p.nodeID
	p.mu.RUnlock()
	seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(seg) < 3 || seg[0] != "api" || seg[1] != "v1" {
		return "", false
	}
	rest := seg[2:]
	if r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "commands" {
		return "/api/v1/agent/commands", true
	}
	if len(rest) >= 1 && rest[0] == "tasks" {
		switch {
		case r.Method == http.MethodGet && len(rest) == 1:
			return "/api/v1/agent/tasks", true
		case r.Method == http.MethodGet && len(rest) == 2:
			return "/api/v1/agent/tasks/" + rest[1], true
		case r.Method == http.MethodPost && len(rest) == 3 && rest[2] == "cancel":
			return "/api/v1/agent/tasks/" + rest[1] + "/cancel", true
		}
		return "", false
	}
	if r.Method == http.MethodGet && len(rest) == 2 && rest[0] == "ai" && rest[1] == "leases" {
		return "/api/v1/agent/leases", true
	}
	if r.Method == http.MethodGet && len(rest) == 2 && rest[0] == "ai" && rest[1] == "lease-requests" {
		return "/api/v1/agent/lease-requests", true
	}
	if r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "audit-events" {
		return "/api/v1/agent/audit-events", true
	}
	if r.Method == http.MethodPost && len(rest) == 3 && rest[0] == "nodes" && rest[2] == "tasks" {
		// Only proxy self-execution for this child's own node id. A foreign id
		// falls through so the local scoped handler returns 404.
		if nodeID != "" && rest[1] != nodeID {
			return "", false
		}
		return "/api/v1/agent/tasks", true
	}
	return "", false
}

// forward signs and forwards the request to the primary, copying status and
// body back unchanged (the primary's error envelope is already uniform).
// Upstream 401s are rewritten to 502 UPSTREAM_AUTH_FAILED so the child frontend
// does not treat them as its own session expiry.
func (p *childProxy) forward(w http.ResponseWriter, r *http.Request, target string) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		writeError(w, r, p.log, http.StatusServiceUnavailable, "UNAVAILABLE", "child identity not claimed yet; retry shortly", nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBodyBytes))
	if err != nil {
		writeError(w, r, p.log, http.StatusBadRequest, "BAD_REQUEST", "cannot read request body", nil)
		return
	}
	headers := map[string]string{}
	if idem := r.Header.Get("Idempotency-Key"); idem != "" {
		headers["Idempotency-Key"] = idem
	}
	resp, err := client.DoRaw(r.Method, target, r.URL.Query(), body, headers)
	if err != nil {
		p.log.Warn("child proxy upstream request failed", "path", r.URL.Path, "target", target, "error", err)
		writeError(w, r, p.log, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "无法连接主节点同步本机数据", nil)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		p.log.Warn("child proxy upstream authentication failed", "path", r.URL.Path, "target", target, "status", resp.StatusCode)
		writeError(w, r, p.log, http.StatusBadGateway, "UPSTREAM_AUTH_FAILED", "主节点拒绝本机凭证，请联系管理员", nil)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBodyBytes+1))
	if err != nil {
		p.log.Warn("child proxy upstream read failed", "path", r.URL.Path, "error", err)
		writeError(w, r, p.log, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "读取主节点数据失败", nil)
		return
	}
	if len(data) > maxProxyBodyBytes {
		writeError(w, r, p.log, http.StatusBadGateway, "UPSTREAM_RESPONSE_TOO_LARGE", "主节点响应过大", nil)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}
