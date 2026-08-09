package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"servercli/internal/config"
)

// AgentVersion is reported in enrollments and heartbeats.
const AgentVersion = "0.1.0"

// Agent orchestrates registration, heartbeat, tasks and lease keys.
type Agent struct {
	cfg      *config.Config
	log      *slog.Logger
	client   *Client
	identity *Identity
	executor *Executor
	keys     *LeaseKeyManager

	mu          sync.Mutex
	commands    []CommandEntry
	commandsSig string
	doneCache   map[string]time.Time
}

// absConfigPath resolves a possibly-relative config path against the process
// working directory (used for authorized_keys and lease-shell paths).
func absConfigPath(p string) string {
	if p == "" {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// NewAgent builds the agent.
func NewAgent(cfg *config.Config, log *slog.Logger) *Agent {
	client := NewClient(cfg.PrimaryBackendURL, cfg.HTTPInsecureSkipVerify, time.Duration(cfg.TaskPollTimeoutSeconds+20)*time.Second)
	return &Agent{
		cfg:      cfg,
		log:      log,
		client:   client,
		executor: NewExecutor(client, log),
		// Resolve config paths to absolute values so SSH forced-command and
		// authorized_keys entries work no matter the process working directory.
		keys:      NewLeaseKeyManager(absConfigPath(cfg.AuthorizedKeysFile), absConfigPath(cfg.LeaseShellBin), log),
		doneCache: map[string]time.Time{},
	}
}

// Run is the agent's main loop.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.AgentStateDir, 0o700); err != nil {
		return err
	}
	if err := a.ensureIdentity(ctx); err != nil {
		return fmt.Errorf("registration: %w", err)
	}
	a.client.SetCredential(a.identity.NodeCredential)
	a.log.Info("agent identity ready", "node_id", a.identity.NodeID, "instance_name", a.identity.InstanceName)

	// Initial commands snapshot.
	a.reloadCommands(ctx)

	// Startup lease key sweep + initial ops fetch.
	a.sweepLeaseKeys()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); a.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); a.taskPollLoop(ctx) }()
	go func() { defer wg.Done(); a.commandWatchLoop(ctx) }()
	go func() { defer wg.Done(); a.leaseSweepLoop(ctx) }()
	go func() { defer wg.Done(); a.eventStreamLoop(ctx) }()
	wg.Wait()
	return nil
}

// ---- registration ----

func (a *Agent) ensureIdentity(ctx context.Context) error {
	backoff := 2 * time.Second
	for {
		ident, err := LoadIdentity(a.cfg.AgentStateDir)
		if err == nil && ident.NodeID != "" && ident.NodeCredential != "" {
			a.identity = ident
			return nil
		}
		if err := a.registerAndClaim(ctx); err != nil {
			a.log.Error("registration failed", "error", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
			continue
		}
		return nil
	}
}

func (a *Agent) registerAndClaim(ctx context.Context) error {
	ident, err := LoadIdentity(a.cfg.AgentStateDir)
	if err != nil || ident.InstanceRequestID == "" {
		ident, err = NewPendingIdentity(a.cfg.AgentStateDir)
		if err != nil {
			return err
		}
		// Persist the pending identity so instance_request_id survives restarts.
		if err := ident.SaveIdentity(a.cfg.AgentStateDir); err != nil {
			return err
		}
	}
	pub, err := ident.PublicKeyB64()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	role := a.cfg.NodeRole
	req := map[string]any{
		"instance_request_id": ident.InstanceRequestID,
		"hostname":            hostname,
		"instance_name":       a.cfg.InstanceName,
		"requested_role":      role,
		"agent_version":       AgentVersion,
		"os_name":             runtime.GOOS,
		"os_version":          runtime.GOARCH,
		"arch":                runtime.GOARCH,
		"reported_addresses":  a.reportedAddresses(),
		"frontend_port":       a.portOf(a.cfg.FrontendAddr),
		"backend_port":        a.portOf(a.cfg.BackendAddr),
		"instance_public_key": pub,
	}
	var enrollResp struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
	}
	resp, err := a.client.Unsigned("POST", "/api/v1/agent/enrollments", req, &enrollResp)
	if err != nil {
		return err
	}
	_ = resp
	enrollmentID := enrollResp.Enrollment.ID
	if enrollmentID == "" {
		return fmt.Errorf("no enrollment id returned")
	}

	// Poll until approved.
	deadline := time.Now().Add(30 * time.Minute)
	var claimToken string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		var statusResp struct {
			Enrollment map[string]any `json:"enrollment"`
		}
		_, err := a.client.Unsigned("GET", "/api/v1/agent/enrollments/"+enrollmentID, nil, &statusResp)
		if err != nil {
			a.log.Debug("enrollment status fetch failed", "error", err)
			continue
		}
		status, _ := statusResp.Enrollment["status"].(string)
		switch status {
		case "approved":
			claimToken, _ = statusResp.Enrollment["claim_token"].(string)
			if claimToken == "" {
				a.log.Warn("approved enrollment returned no claim token")
				continue
			}
		case "rejected":
			return fmt.Errorf("enrollment rejected by admin")
		case "expired":
			return fmt.Errorf("enrollment expired")
		}
		if claimToken != "" {
			break
		}
	}
	if claimToken == "" {
		return fmt.Errorf("enrollment not approved within timeout")
	}

	// Claim.
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig, err := ident.SignProof(ts, enrollmentID)
	if err != nil {
		return err
	}
	claimReq := map[string]any{
		"enrollment_id":   enrollmentID,
		"proof_signature": sig,
		"proof_timestamp": ts,
		"public_key":      pub,
	}
	var claimResp struct {
		NodeID         string `json:"node_id"`
		NodeCredential string `json:"node_credential"`
		InstanceName   string `json:"instance_name"`
	}
	_, err = a.client.UnsignedWithBearer("POST", "/api/v1/agent/enrollments/"+enrollmentID+"/claim", claimReq, claimToken, &claimResp)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if claimResp.NodeID == "" || claimResp.NodeCredential == "" {
		return fmt.Errorf("claim returned incomplete identity")
	}
	ident.NodeID = claimResp.NodeID
	ident.NodeCredential = claimResp.NodeCredential
	ident.InstanceName = claimResp.InstanceName
	ident.InstanceRole = role
	if err := ident.SaveIdentity(a.cfg.AgentStateDir); err != nil {
		return err
	}
	a.identity = ident
	a.log.Info("identity claimed", "node_id", ident.NodeID, "instance_name", ident.InstanceName)
	return nil
}

func (a *Agent) reportedAddresses() []map[string]any {
	addrs := []map[string]any{}
	ip := a.cfg.PrimaryServerIP
	if ip != "" && ip != "127.0.0.1" {
		addrs = append(addrs, map[string]any{
			"address":      ip,
			"address_type": "source",
			"service_port": a.portOf(a.cfg.BackendAddr),
		})
	}
	for _, local := range localIPs() {
		addrs = append(addrs, map[string]any{
			"address":      local,
			"address_type": "reported",
			"service_port": a.portOf(a.cfg.BackendAddr),
		})
	}
	return addrs
}

func localIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch t := addr.(type) {
			case *net.IPNet:
				ip = t.IP
			case *net.IPAddr:
				ip = t.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	return out
}

func (a *Agent) portOf(addr string) int {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil {
			return p
		}
	}
	return 0
}

// ---- heartbeat ----

func (a *Agent) heartbeatLoop(ctx context.Context) {
	interval := time.Duration(a.cfg.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.sendHeartbeat(ctx); err != nil {
				a.log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context) error {
	sys := CollectSystem(a.log)
	hostname, _ := os.Hostname()
	body := map[string]any{
		"hostname":           hostname,
		"agent_version":      AgentVersion,
		"os_name":            runtime.GOOS,
		"os_version":         runtime.GOARCH,
		"arch":               runtime.GOARCH,
		"addresses":          a.reportedAddresses(),
		"cpu_usage_percent":  sys.CPUUsagePercent,
		"memory_total_bytes": sys.MemoryTotalBytes,
		"memory_used_bytes":  sys.MemoryUsedBytes,
		"disk_total_bytes":   sys.DiskTotalBytes,
		"disk_used_bytes":    sys.DiskUsedBytes,
		"load_1":             sys.Load1,
		"load_5":             sys.Load5,
		"load_15":            sys.Load15,
		"uptime_seconds":     sys.UptimeSeconds,
		"time_offset_ms":     0,
		"summary":            sys.Extra,
		"commands_hash":      a.commandsHash(),
	}
	var resp struct {
		NodeID  string         `json:"node_id"`
		Status  string         `json:"status"`
		Install []LeaseInstall `json:"install"`
		Remove  []LeaseRemove  `json:"remove"`
	}
	if _, err := a.client.Do("POST", "/api/v1/agent/heartbeat", body, &resp); err != nil {
		return err
	}
	if len(resp.Install) > 0 || len(resp.Remove) > 0 {
		if err := a.keys.Apply(resp.Install, resp.Remove); err != nil {
			a.log.Warn("lease keys apply failed", "error", err)
			for _, inst := range resp.Install {
				a.reportLeaseEvent(inst.LeaseID, "install_failed", err.Error(), "")
			}
		} else {
			for _, inst := range resp.Install {
				a.reportLeaseEvent(inst.LeaseID, "installed", "", "")
			}
			for _, rm := range resp.Remove {
				a.reportLeaseEvent(rm.LeaseID, "removed", rm.Reason, "")
			}
		}
	}
	return nil
}

// ---- lease key event stream (immediate refresh) ----

// eventStreamLoop keeps a WebSocket connection to the control plane open and,
// on a lease_keys_changed notification, refreshes lease keys right away
// instead of waiting for the next heartbeat (up to HeartbeatIntervalSeconds).
func (a *Agent) eventStreamLoop(ctx context.Context) {
	backoff := 2 * time.Second
	for {
		if err := a.watchEvents(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("lease event stream interrupted", "error", err, "reconnect_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Agent) watchEvents(ctx context.Context) error {
	headers := a.client.StreamHeaders("GET", "/api/v1/agent/ws")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(a.cfg.PrimaryBackendURL, "/api/v1/agent/ws"), headers)
	if err != nil {
		return err
	}
	defer conn.Close()
	if resp != nil && resp.StatusCode >= 300 {
		return fmt.Errorf("event stream returned %d", resp.StatusCode)
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Event {
		case "lease_keys_changed":
			if err := a.sendHeartbeat(ctx); err != nil {
				a.log.Warn("lease key refresh heartbeat failed", "error", err)
			}
		case "task_cancelled":
			if id, ok := msg.Data["task_id"].(string); ok && id != "" {
				a.executor.MarkCancelled(id)
				a.log.Info("task cancelled via push", "task_id", id)
			}
		}
	}
}

// wsURL converts a control-plane http(s) base URL into the matching ws(s) URL.
func wsURL(base, path string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + path
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + path
	}
	return base + path
}

// reportLeaseEvent reports a lease lifecycle event to the control plane.
func (a *Agent) reportLeaseEvent(leaseID, eventType, message, sessionID string) {
	body := map[string]any{"event_type": eventType, "message": message, "session_id": sessionID}
	if _, err := a.client.Do("POST", "/api/v1/agent/leases/"+leaseID+"/events", body, nil); err != nil {
		a.log.Warn("lease event report failed", "lease_id", leaseID, "event", eventType, "error", err)
	}
}

func (a *Agent) commandsHash() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commandsSig
}

// ---- commands ----

func (a *Agent) reloadCommands(ctx context.Context) {
	cmds, err := LoadCommands(a.cfg.CommandsDir, a.log)
	if err != nil {
		a.log.Warn("command load failed", "error", err)
		return
	}
	hash := hashCommands(cmds)
	a.mu.Lock()
	same := hash == a.commandsSig
	a.commands = cmds
	a.commandsSig = hash
	a.mu.Unlock()
	if same {
		return
	}
	a.log.Info("command registry changed", "count", len(cmds), "hash", hash)
	var resp struct {
		Status string `json:"status"`
	}
	payload := map[string]any{"commands": SnapshotPayload(cmds)}
	if _, err := a.client.Do("POST", "/api/v1/agent/commands/snapshot", payload, &resp); err != nil {
		a.log.Warn("commands snapshot upload failed", "error", err)
	}
}

func hashCommands(cmds []CommandEntry) string {
	h := sha256.New()
	for _, c := range cmds {
		_, _ = h.Write([]byte(c.CommandID + "\x00" + c.CommandVersion + "\x00" + c.ManifestHash + "\x00" + c.ExecutableHash + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (a *Agent) commandWatchLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reloadCommands(ctx)
		}
	}
}

// ---- tasks ----

func (a *Agent) taskPollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var resp struct {
			Task           *TaskPayload `json:"task"`
			CancelledTasks []string     `json:"cancelled_tasks"`
		}
		_, err := a.client.Do("GET", "/api/v1/agent/tasks/poll", nil, &resp)
		if err != nil {
			a.log.Warn("task poll failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, id := range resp.CancelledTasks {
			a.executor.MarkCancelled(id)
		}
		if resp.Task != nil {
			a.handleTask(ctx, resp.Task)
		}
	}
}

func (a *Agent) handleTask(ctx context.Context, payload *TaskPayload) {
	if a.alreadyDone(payload.TaskID) {
		a.log.Debug("task already executed, skipping", "task_id", payload.TaskID)
		return
	}
	if err := VerifyPayload(payload, a.identity.NodeCredential); err != nil {
		a.log.Warn("task payload verification failed", "task_id", payload.TaskID, "error", err)
		return
	}
	a.mu.Lock()
	var cmd *CommandEntry
	for i := range a.commands {
		if a.commands[i].CommandID == payload.CommandID && a.commands[i].CommandVersion == payload.CommandVersion {
			cmd = &a.commands[i]
			break
		}
	}
	a.mu.Unlock()
	if cmd == nil {
		a.log.Warn("task references unknown command", "task_id", payload.TaskID, "command", payload.CommandID)
		code := "COMMAND_NOT_FOUND"
		_ = a.client.SendResult(payload.TaskID, Result{
			Status: "failed", ErrorCode: code, ErrorMessage: "command not registered on this node",
			FinishedAt: time.Now().UTC(),
		})
		return
	}
	a.markDone(payload.TaskID)
	a.executor.Execute(ctx, payload, *cmd)
}

func (a *Agent) alreadyDone(taskID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.doneCache[taskID]
	return ok
}

func (a *Agent) markDone(taskID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.doneCache[taskID] = time.Now()
	// prune entries older than 1 hour
	for k, v := range a.doneCache {
		if time.Since(v) > time.Hour {
			delete(a.doneCache, k)
		}
	}
}

// ---- lease key sweep ----

func (a *Agent) leaseSweepLoop(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepLeaseKeys()
		}
	}
}

func (a *Agent) sweepLeaseKeys() {
	if n, err := a.keys.SweepExpired(time.Now()); err != nil {
		a.log.Warn("lease key sweep failed", "error", err)
	} else if n > 0 {
		a.log.Info("locally removed expired lease keys", "count", n)
	}
}
