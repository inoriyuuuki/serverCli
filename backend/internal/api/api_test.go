package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/service"
	"servercli/internal/store"
)

type testEnv struct {
	handler  http.Handler
	srv      *Server
	st       *store.Store
	nodes    *service.NodeService
	adminID  string
	csrf     string
	session  string
	privKey  ed25519.PrivateKey
	pubB64   string
	enrollID string
	nodeID   string
	cred     string
}

func setupAPI(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AgentStateDir = dir
	cfg.DatabaseURL = filepath.Join(dir, "test.db")
	cfg.LogLevel = "error"
	cfg.InstanceName = "test-primary"
	cfg.NodeRole = "primary"
	log := logger.New(io.Discard, "error")
	ctx := context.Background()
	database, err := db.Open(ctx, "sqlite", cfg.DatabaseURL, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	st := store.New(database, log)
	settings := service.NewSettingsService(st, cfg)
	if err := settings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	hash, _ := security.HashPassword("adminpass123")
	now := time.Now().UTC()
	admin := &model.AdminUser{ID: model.NewUUID(), Username: "admin", PasswordHash: hash, PasswordChangedAt: &now}
	if err := st.CreateAdmin(ctx, admin); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Config: cfg, Log: log, Store: st, Version: "test", Build: "test", Commit: "test"})
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{handler: srv.Handler(), srv: srv, st: st, nodes: srv.NodeService(), adminID: admin.ID}
	env.login(t)
	env.enroll(t)
	return env
}

// serve runs a request through the handler without binding a socket.
func (e *testEnv) serve(method, path string, body any, headers map[string]string) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (e *testEnv) post(path string, body any, headers map[string]string) (int, []byte) {
	return e.serve("POST", path, body, headers)
}

// serveRaw sends an already-serialized body (used for signed agent requests
// where the signature is computed over the exact bytes).
func (e *testEnv) serveRaw(method, path string, body []byte, headers map[string]string) (int, []byte) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (e *testEnv) login(t *testing.T) {
	t.Helper()
	body := map[string]any{"username": "admin", "password": "adminpass123"}
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(mustJSON(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	e.csrf = resp.CSRF
	if e.csrf == "" {
		t.Fatal("no csrf token in login response")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "servercli_session" {
			e.session = c.Value
		}
	}
	if e.session == "" {
		t.Fatal("login did not set session cookie")
	}
}

func (e *testEnv) enroll(t *testing.T) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e.privKey = priv
	e.pubB64 = base64.StdEncoding.EncodeToString(pub)
	body := map[string]any{
		"instance_request_id": "api-test-inst-1",
		"hostname":            "api-node",
		"requested_role":      "child",
		"agent_version":       "test",
		"reported_addresses":  []map[string]any{{"address": "10.1.1.1", "address_type": "reported", "service_port": 9047}},
		"instance_public_key": e.pubB64,
	}
	var resp struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
	}
	status, out := e.serve("POST", "/api/v1/agent/enrollments", body, nil)
	if status != http.StatusCreated {
		t.Fatalf("enroll status %d: %s", status, out)
	}
	_ = json.Unmarshal(out, &resp)
	e.enrollID = resp.Enrollment.ID
	status, out = e.post("/api/v1/node-enrollments/"+e.enrollID+"/approve", map[string]any{"review_note": "ok"}, e.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("approve status %d: %s", status, out)
	}
	status, out = e.serve("GET", "/api/v1/agent/enrollments/"+e.enrollID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status fetch %d: %s", status, out)
	}
	var stResp struct {
		Enrollment map[string]any `json:"enrollment"`
	}
	_ = json.Unmarshal(out, &stResp)
	claimToken, _ := stResp.Enrollment["claim_token"].(string)
	if claimToken == "" {
		t.Fatalf("no claim token: %s", out)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := ed25519.Sign(priv, []byte(ts+"|"+e.enrollID))
	claimBody := map[string]any{
		"enrollment_id":   e.enrollID,
		"proof_signature": base64.StdEncoding.EncodeToString(sig),
		"proof_timestamp": ts,
		"public_key":      e.pubB64,
	}
	var claimResp struct {
		NodeID         string `json:"node_id"`
		NodeCredential string `json:"node_credential"`
	}
	status, out = e.serve("POST", "/api/v1/agent/enrollments/"+e.enrollID+"/claim", claimBody,
		map[string]string{"Authorization": "Bearer " + claimToken})
	if status != http.StatusOK {
		t.Fatalf("claim status %d: %s", status, out)
	}
	_ = json.Unmarshal(out, &claimResp)
	e.nodeID = claimResp.NodeID
	e.cred = claimResp.NodeCredential
	if e.nodeID == "" || e.cred == "" {
		t.Fatal("claim incomplete")
	}
}

func (e *testEnv) agentHeaders(method, path string, body []byte) map[string]string {
	sum := sha256.Sum256(body)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := agentSignature(e.cred, ts, method, path, hex.EncodeToString(sum[:]))
	return map[string]string{
		"Authorization":     "Bearer " + e.cred,
		"X-Agent-Timestamp": ts,
		"X-Agent-Signature": sig,
	}
}

func (e *testEnv) adminHeaders() map[string]string {
	return map[string]string{
		"X-CSRF-Token": e.csrf,
		"Cookie":       "servercli_session=" + e.session,
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestAPIFullFlow(t *testing.T) {
	env := setupAPI(t)

	// Session endpoint returns CSRF for the logged-in admin.
	status, out := env.serve("GET", "/api/v1/auth/session", nil, map[string]string{"Cookie": "servercli_session=" + env.session})
	if status != http.StatusOK {
		t.Fatalf("session status %d", status)
	}
	var sessResp struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf_token"`
	}
	_ = json.Unmarshal(out, &sessResp)
	if !sessResp.Authenticated || sessResp.CSRF == "" {
		t.Fatalf("session: %+v", sessResp)
	}

	// Heartbeat (signed).
	hbBody := mustJSON(map[string]any{
		"hostname": "api-node", "agent_version": "test",
		"addresses": []map[string]any{{"address": "10.1.1.1", "address_type": "reported", "service_port": 9047}},
	})
	status, out = env.serveRaw("POST", "/api/v1/agent/heartbeat", hbBody, env.agentHeaders("POST", "/api/v1/agent/heartbeat", hbBody))
	if status != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", status, out)
	}
	var hbResp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(out, &hbResp)
	if hbResp.Status != model.NodeStatusOnline {
		t.Fatalf("node not online: %s", out)
	}
	// Bad signature rejected.
	badHeaders := env.agentHeaders("POST", "/api/v1/agent/heartbeat", hbBody)
	badHeaders["X-Agent-Signature"] = strings.Repeat("0", 64)
	status, _ = env.serveRaw("POST", "/api/v1/agent/heartbeat", hbBody, badHeaders)
	if status != http.StatusUnauthorized {
		t.Fatalf("bad signature should be 401, got %d", status)
	}

	// Commands snapshot (signed).
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "system.echo", "command_version": "1.0.0", "category": "system",
		"title": "echo", "permission_profile": "read-only", "timeout_seconds": 30,
		"max_output_bytes": 4096, "enabled": true,
		"parameter_schema_json": `{"type":"object","properties":{"message":{"type":"string"}}}`,
	}}})
	status, out = env.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, env.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatalf("snapshot status %d: %s", status, out)
	}

	// Create task (admin + CSRF + idempotency).
	createBody := map[string]any{"command_id": "system.echo", "command_version": "1.0.0", "arguments": map[string]any{"message": "hi"}}
	h := env.adminHeaders()
	h["Idempotency-Key"] = "task-key-1"
	status, out = env.post("/api/v1/nodes/"+env.nodeID+"/tasks", createBody, h)
	if status != http.StatusCreated {
		t.Fatalf("create task status %d: %s", status, out)
	}
	var taskResp struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	_ = json.Unmarshal(out, &taskResp)
	if taskResp.Task.Status != "queued" {
		t.Fatalf("task not queued: %s", out)
	}

	// Poll (signed) gets the task.
	status, out = env.serve("GET", "/api/v1/agent/tasks/poll", nil, env.agentHeaders("GET", "/api/v1/agent/tasks/poll", nil))
	if status != http.StatusOK {
		t.Fatalf("poll status %d: %s", status, out)
	}
	var pollResp struct {
		Task *service.TaskPayload `json:"task"`
	}
	_ = json.Unmarshal(out, &pollResp)
	if pollResp.Task == nil || pollResp.Task.TaskID != taskResp.Task.ID {
		t.Fatalf("poll did not return task: %s", out)
	}

	// Events + result (signed).
	evPath := "/api/v1/agent/tasks/" + taskResp.Task.ID + "/events"
	evBody := mustJSON(map[string]any{"event_type": "started", "message": "running"})
	status, _ = env.serveRaw("POST", evPath, evBody, env.agentHeaders("POST", evPath, evBody))
	if status != http.StatusOK {
		t.Fatalf("event status %d", status)
	}
	resPath := "/api/v1/agent/tasks/" + taskResp.Task.ID + "/result"
	resBody := mustJSON(map[string]any{"status": "succeeded", "stdout_text": "hi", "exit_code": 0})
	status, out = env.serveRaw("POST", resPath, resBody, env.agentHeaders("POST", resPath, resBody))
	if status != http.StatusOK {
		t.Fatalf("result status %d: %s", status, out)
	}

	// Admin views the task.
	status, out = env.serve("GET", "/api/v1/tasks/"+taskResp.Task.ID, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("get task status %d: %s", status, out)
	}
	var getResp struct {
		Task   map[string]any `json:"task"`
		Events []any          `json:"events"`
	}
	_ = json.Unmarshal(out, &getResp)
	if getResp.Task["status"] != "succeeded" {
		t.Fatalf("task not succeeded: %s", out)
	}
	if len(getResp.Events) == 0 {
		t.Fatal("no task events")
	}

	// Write without CSRF must be rejected.
	status, _ = env.post("/api/v1/nodes/"+env.nodeID+"/tasks", createBody,
		map[string]string{"Cookie": "servercli_session=" + env.session})
	if status != http.StatusForbidden {
		t.Fatalf("write without CSRF should be 403, got %d", status)
	}

	// Audit endpoint lists events.
	status, out = env.serve("GET", "/api/v1/audit-events?limit=10", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("audit status %d", status)
	}
	if !strings.Contains(string(out), "task.create") {
		t.Fatalf("audit missing task.create: %s", out)
	}

	// System info + health.
	status, _ = env.serve("GET", "/api/v1/system/info", nil, nil)
	if status != http.StatusOK {
		t.Fatal("system info failed")
	}
	status, _ = env.serve("GET", "/health/ready", nil, nil)
	if status != http.StatusOK {
		t.Fatal("health ready failed")
	}

	// Lease request flow via API (policy auto-approve).
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", map[string]any{
		"node_selector": env.nodeID, "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 test",
		"permission_profile": "read-only", "requested_duration_seconds": 3600, "purpose": "test", "client_request_id": "lr-1",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("lease request status %d: %s", status, out)
	}
	var lrResp struct {
		Lease        map[string]any `json:"lease"`
		RenewalToken string         `json:"renewal_token"`
	}
	_ = json.Unmarshal(out, &lrResp)
	if lrResp.Lease == nil || lrResp.RenewalToken == "" {
		t.Fatalf("lease not auto-approved: %s", out)
	}
	leaseID := lrResp.Lease["id"].(string)
	// Renew with the AI bearer token.
	status, out = env.serve("POST", "/api/v1/ai/leases/"+leaseID+"/renew", map[string]any{"requested_duration_seconds": 3600},
		map[string]string{"Authorization": "Bearer " + lrResp.RenewalToken})
	if status != http.StatusOK {
		t.Fatalf("renew status %d: %s", status, out)
	}
	// Revoke as admin.
	status, _ = env.post("/api/v1/ai/leases/"+leaseID+"/revoke", map[string]any{"reason": "test", "terminate_sessions": false}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("revoke status %d", status)
	}
}

// TestCommandSchemaAndBadRequestMapping verifies that command parameter
// schemas reach the admin API (so the UI can render the execute form) and
// that schema validation failures map to 400 BAD_REQUEST instead of 500.
func TestCommandSchemaAndBadRequestMapping(t *testing.T) {
	env := setupAPI(t)

	schema := `{"type":"object","additionalProperties":false,"required":["service"],"properties":{"service":{"type":"string","minLength":1,"maxLength":128}}}`
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "service.status", "command_version": "1.0.0", "category": "service",
		"title": "service status", "permission_profile": "read-only", "timeout_seconds": 20,
		"max_output_bytes": 4096, "enabled": true,
		"parameter_schema_json": schema,
	}}})
	status, out := env.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, env.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatalf("snapshot status %d: %s", status, out)
	}

	// Both admin command endpoints must expose parameter_schema_json.
	for _, path := range []string{"/api/v1/commands", "/api/v1/nodes/" + env.nodeID + "/commands"} {
		status, out = env.serve("GET", path, nil, env.adminHeaders())
		if status != http.StatusOK {
			t.Fatalf("GET %s status %d: %s", path, status, out)
		}
		var resp struct {
			Commands []map[string]any `json:"commands"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if len(resp.Commands) == 0 {
			t.Fatalf("GET %s returned no commands", path)
		}
		got, ok := resp.Commands[0]["parameter_schema_json"].(string)
		if !ok || got == "" {
			t.Fatalf("GET %s missing parameter_schema_json: %s", path, out)
		}
	}

	// Missing required argument -> 400 BAD_REQUEST (not 500 INTERNAL_ERROR).
	createBody := map[string]any{"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{}}
	h := env.adminHeaders()
	h["Idempotency-Key"] = "schema-bad-key-1"
	status, out = env.post("/api/v1/nodes/"+env.nodeID+"/tasks", createBody, h)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing required arg, got %d: %s", status, out)
	}
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &errResp)
	if errResp.Error.Code != "BAD_REQUEST" {
		t.Fatalf("expected BAD_REQUEST code, got %q: %s", errResp.Error.Code, out)
	}
	if !strings.Contains(errResp.Error.Message, `missing required property "service"`) {
		t.Fatalf("unexpected error message: %s", out)
	}

	// Valid argument -> task created.
	h["Idempotency-Key"] = "schema-ok-key-1"
	status, out = env.post("/api/v1/nodes/"+env.nodeID+"/tasks",
		map[string]any{"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{"service": "sshd"}}, h)
	if status != http.StatusCreated {
		t.Fatalf("expected 201 for valid args, got %d: %s", status, out)
	}
}

// TestNodeListIncludesHeartbeat verifies node list/detail responses attach the
// latest heartbeat so the UI can render the resource summary.
func TestNodeListIncludesHeartbeat(t *testing.T) {
	env := setupAPI(t)

	hbBody := mustJSON(map[string]any{
		"hostname": "api-node", "agent_version": "test",
		"cpu_usage_percent": 12.5, "memory_total_bytes": 1000, "memory_used_bytes": 400,
		"disk_total_bytes": 5000, "disk_used_bytes": 1000, "load_1": 0.5, "load_5": 0.3, "load_15": 0.2,
		"uptime_seconds": 3600, "time_offset_ms": 0,
		"addresses": []map[string]any{{"address": "10.1.1.1", "address_type": "reported", "service_port": 9047}},
	})
	status, out := env.serveRaw("POST", "/api/v1/agent/heartbeat", hbBody, env.agentHeaders("POST", "/api/v1/agent/heartbeat", hbBody))
	if status != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", status, out)
	}

	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/" + env.nodeID} {
		status, out = env.serve("GET", path, nil, env.adminHeaders())
		if status != http.StatusOK {
			t.Fatalf("GET %s status %d", path, status)
		}
		var resp struct {
			Nodes []map[string]any `json:"nodes"`
			Node  map[string]any   `json:"node"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		node := resp.Node
		if len(resp.Nodes) > 0 {
			node = resp.Nodes[0]
		}
		hb, _ := node["heartbeat"].(map[string]any)
		if hb == nil {
			t.Fatalf("GET %s missing heartbeat: %s", path, out)
		}
		if hb["cpu_usage_percent"] != 12.5 {
			t.Fatalf("GET %s unexpected cpu in heartbeat: %s", path, out)
		}
	}
}

// TestAgentSelfService verifies the primary's agent self-service endpoints
// (scoped to the calling node) used by child control planes to mirror data.
func TestAgentSelfService(t *testing.T) {
	env := setupAPI(t)

	schema := `{"type":"object","required":["service"],"properties":{"service":{"type":"string","minLength":1}}}`
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "service.status", "command_version": "1.0.0", "category": "service",
		"title": "service status", "permission_profile": "read-only", "timeout_seconds": 20,
		"max_output_bytes": 4096, "enabled": true, "parameter_schema_json": schema,
	}}})
	status, out := env.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, env.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatalf("snapshot status %d: %s", status, out)
	}

	// Own commands, including the parameter schema.
	status, out = env.serve("GET", "/api/v1/agent/commands", nil, env.agentHeaders("GET", "/api/v1/agent/commands", nil))
	if status != http.StatusOK {
		t.Fatalf("agent commands status %d: %s", status, out)
	}
	var cmdsResp struct {
		Commands []map[string]any `json:"commands"`
	}
	_ = json.Unmarshal(out, &cmdsResp)
	if len(cmdsResp.Commands) != 1 || cmdsResp.Commands[0]["command_id"] != "service.status" {
		t.Fatalf("agent commands: %s", out)
	}
	if _, ok := cmdsResp.Commands[0]["parameter_schema_json"].(string); !ok {
		t.Fatalf("agent commands missing parameter_schema_json: %s", out)
	}

	// Self-execute: a node may queue a task against itself (Idempotency-Key required).
	createBody := map[string]any{"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{"service": "sshd"}}
	createHeaders := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(createBody))
	createHeaders["Idempotency-Key"] = "agent-idem-1"
	status, out = env.serve("POST", "/api/v1/agent/tasks", createBody, createHeaders)
	if status != http.StatusCreated {
		t.Fatalf("agent create task status %d: %s", status, out)
	}
	var created struct {
		Task struct {
			ID     string `json:"id"`
			NodeID string `json:"node_id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	_ = json.Unmarshal(out, &created)
	if created.Task.ID == "" || created.Task.NodeID != env.nodeID || created.Task.Status != "queued" {
		t.Fatalf("agent create task: %s", out)
	}

	// Missing Idempotency-Key is rejected.
	noIdem := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(createBody))
	status, out = env.serve("POST", "/api/v1/agent/tasks", createBody, noIdem)
	if status != http.StatusBadRequest {
		t.Fatalf("agent create without idempotency should be 400, got %d: %s", status, out)
	}

	// Unregistered command is rejected (scope + registry enforced).
	badBody := map[string]any{"command_id": "nope.missing", "command_version": "1.0.0", "arguments": map[string]any{}}
	badHeaders := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(badBody))
	badHeaders["Idempotency-Key"] = "agent-idem-2"
	status, out = env.serve("POST", "/api/v1/agent/tasks", badBody, badHeaders)
	if status != http.StatusNotFound {
		t.Fatalf("agent create unregistered command should be 404, got %d: %s", status, out)
	}

	// Bad signature is rejected on the new endpoints too.
	badSig := env.agentHeaders("GET", "/api/v1/agent/tasks", nil)
	badSig["X-Agent-Signature"] = strings.Repeat("0", 64)
	status, _ = env.serve("GET", "/api/v1/agent/tasks", nil, badSig)
	if status != http.StatusUnauthorized {
		t.Fatalf("agent tasks bad signature should be 401, got %d", status)
	}

	// Own tasks list + detail.
	status, out = env.serve("GET", "/api/v1/agent/tasks", nil, env.agentHeaders("GET", "/api/v1/agent/tasks", nil))
	if status != http.StatusOK {
		t.Fatalf("agent tasks status %d: %s", status, out)
	}
	var tasksResp struct {
		Tasks []map[string]any `json:"tasks"`
	}
	_ = json.Unmarshal(out, &tasksResp)
	if len(tasksResp.Tasks) != 1 {
		t.Fatalf("agent tasks: %s", out)
	}
	detailPath := "/api/v1/agent/tasks/" + created.Task.ID
	status, out = env.serve("GET", detailPath, nil, env.agentHeaders("GET", detailPath, nil))
	if status != http.StatusOK {
		t.Fatalf("agent get task status %d: %s", status, out)
	}
	var detail struct {
		Task map[string]any `json:"task"`
	}
	_ = json.Unmarshal(out, &detail)
	if detail.Task["id"] != created.Task.ID {
		t.Fatalf("agent get task: %s", out)
	}

	// Self-cancel.
	cancelPath := "/api/v1/agent/tasks/" + created.Task.ID + "/cancel"
	status, out = env.serve("POST", cancelPath, nil, env.agentHeaders("POST", cancelPath, nil))
	if status != http.StatusOK {
		t.Fatalf("agent cancel task status %d: %s", status, out)
	}

	// Leases + lease requests (empty is fine).
	status, out = env.serve("GET", "/api/v1/agent/leases", nil, env.agentHeaders("GET", "/api/v1/agent/leases", nil))
	if status != http.StatusOK {
		t.Fatalf("agent leases status %d: %s", status, out)
	}
	status, out = env.serve("GET", "/api/v1/agent/lease-requests", nil, env.agentHeaders("GET", "/api/v1/agent/lease-requests", nil))
	if status != http.StatusOK {
		t.Fatalf("agent lease requests status %d: %s", status, out)
	}

	// Audit is scoped to the calling node.
	status, out = env.serve("GET", "/api/v1/agent/audit-events", nil, env.agentHeaders("GET", "/api/v1/agent/audit-events", nil))
	if status != http.StatusOK {
		t.Fatalf("agent audit status %d: %s", status, out)
	}
	var auditResp struct {
		Events []map[string]any `json:"audit_events"`
	}
	_ = json.Unmarshal(out, &auditResp)
	if len(auditResp.Events) == 0 {
		t.Fatalf("agent audit empty: %s", out)
	}
	for _, e := range auditResp.Events {
		if e["node_id"] != env.nodeID {
			t.Fatalf("audit event not scoped to node: %s", out)
		}
	}
}

// TestChildProxyForwardsSelfView verifies a child control plane proxies its
// scoped self-view requests (commands/tasks/leases) to the primary via the
// node credential, while non-proxied paths stay local.
func TestChildProxyForwardsSelfView(t *testing.T) {
	up := setupAPI(t)
	schema := `{"type":"object","required":["service"],"properties":{"service":{"type":"string","minLength":1}}}`
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "service.status", "command_version": "1.0.0", "category": "service",
		"title": "service status", "permission_profile": "read-only", "timeout_seconds": 20,
		"max_output_bytes": 4096, "enabled": true, "parameter_schema_json": schema,
	}}})
	status, _ := up.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, up.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatal("upstream snapshot failed")
	}
	// In-process upstream: route the proxy's HTTP transport to the primary
	// handler directly (no listening socket, so it works in sandboxed tests).

	// Child control plane with its own DB and an admin.
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AgentStateDir = dir
	cfg.DatabaseURL = filepath.Join(dir, "child.db")
	cfg.LogLevel = "error"
	cfg.InstanceName = "test-child-1"
	cfg.NodeRole = "child"
	cfg.PrimaryBackendURL = "http://upstream.test"
	log := logger.New(io.Discard, "error")
	ctx := context.Background()
	database, err := db.Open(ctx, "sqlite", cfg.DatabaseURL, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	st := store.New(database, log)
	settings := service.NewSettingsService(st, cfg)
	if err := settings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	hash, _ := security.HashPassword("adminpass123")
	now := time.Now().UTC()
	admin := &model.AdminUser{ID: model.NewUUID(), Username: "admin", PasswordHash: hash, PasswordChangedAt: &now}
	if err := st.CreateAdmin(ctx, admin); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Config: cfg, Log: log, Store: st, Version: "test", Build: "test", Commit: "test", ChildNodeID: up.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	srv.childProxy.setTransport(handlerTransport{h: up.handler})
	// Note: SetChildProxy is intentionally NOT called here yet; the test first
	// verifies the proxy returns 503 before the identity is claimed.
	handler := srv.Handler()

	serve := func(method, path string, body any, headers map[string]string) (int, []byte) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	session, csrf := loginChildAdmin(t, handler)
	status, out := serve("GET", "/api/v1/commands", nil, adminHeaders(session, csrf))

	// Proxy not ready (identity not claimed yet) -> 503 instead of silently
	// falling back to the empty local DB.
	status, out = serve("GET", "/api/v1/commands", nil, adminHeaders(session, csrf))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("child commands before proxy ready should be 503, got %d: %s", status, out)
	}

	// Claim the identity: proxy becomes ready.
	srv.SetChildProxy(up.nodeID, up.cred)

	// Proxied paths require the child's own admin session.
	status, out = serve("GET", "/api/v1/commands", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("child commands without session should be 401, got %d: %s", status, out)
	}

	// Authenticated GET /commands is proxied to the upstream.
	status, out = serve("GET", "/api/v1/commands", nil, adminHeaders(session, csrf))
	if status != http.StatusOK {
		t.Fatalf("child commands status %d: %s", status, out)
	}
	var cmds struct {
		Commands []map[string]any `json:"commands"`
	}
	_ = json.Unmarshal(out, &cmds)
	if len(cmds.Commands) != 1 || cmds.Commands[0]["command_id"] != "service.status" {
		t.Fatalf("child commands not proxied: %s", out)
	}

	// Writes need CSRF + Idempotency-Key; self-execution lands on the upstream.
	writeHeaders := adminHeaders(session, csrf)
	writeHeaders["Idempotency-Key"] = "child-idem-1"
	status, out = serve("POST", "/api/v1/nodes/"+up.nodeID+"/tasks", map[string]any{
		"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{"service": "sshd"},
	}, writeHeaders)
	if status != http.StatusCreated {
		t.Fatalf("child task create status %d: %s", status, out)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	_ = json.Unmarshal(out, &created)
	if created.Task.ID == "" {
		t.Fatalf("child task create: %s", out)
	}

	// Foreign node id is rejected locally (not silently executed on self).
	status, out = serve("POST", "/api/v1/nodes/some-other-node/tasks", map[string]any{
		"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{"service": "sshd"},
	}, writeHeaders)
	if status != http.StatusNotFound {
		t.Fatalf("child task create for foreign node should be 404, got %d: %s", status, out)
	}

	// Authenticated GET /tasks/{id} is proxied too.
	status, out = serve("GET", "/api/v1/tasks/"+created.Task.ID, nil, adminHeaders(session, csrf))
	if status != http.StatusOK {
		t.Fatalf("child get task status %d: %s", status, out)
	}
	var detail struct {
		Task map[string]any `json:"task"`
	}
	_ = json.Unmarshal(out, &detail)
	if detail.Task["id"] != created.Task.ID {
		t.Fatalf("child get task: %s", out)
	}

	// Non-proxied path stays local: /nodes requires the child's own admin.
	status, out = serve("GET", "/api/v1/nodes", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("child /nodes should be local (401 without admin session), got %d: %s", status, out)
	}

	// Upstream 401 must not be passed through as the child's own session 401
	// (the frontend would log the user out); it becomes 502 UPSTREAM_AUTH_FAILED.
	srv.childProxy.setTransport(statusTransport{status: http.StatusUnauthorized})
	defer srv.childProxy.setTransport(handlerTransport{h: up.handler})
	status, out = serve("GET", "/api/v1/commands", nil, adminHeaders(session, csrf))
	if status != http.StatusBadGateway {
		t.Fatalf("upstream 401 should become 502, got %d: %s", status, out)
	}
	var upErr struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &upErr)
	if upErr.Error.Code != "UPSTREAM_AUTH_FAILED" {
		t.Fatalf("unexpected upstream error code: %s", out)
	}
}

// loginChildAdmin logs into a control plane handler and returns the session
// cookie and CSRF token.
func loginChildAdmin(t *testing.T, handler http.Handler) (session, csrf string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(mustJSON(map[string]any{"username": "admin", "password": "adminpass123"})))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("child login status %d: %s", rec.Code, rec.Body.String())
	}
	var lr struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "servercli_session" {
			session = c.Value
		}
	}
	if session == "" || lr.CSRF == "" {
		t.Fatalf("child login did not return session/csrf: %s", rec.Body.String())
	}
	return session, lr.CSRF
}

func adminHeaders(session, csrf string) map[string]string {
	return map[string]string{
		"Cookie":       "servercli_session=" + session,
		"X-CSRF-Token": csrf,
	}

}

// handlerTransport routes HTTP requests to an in-memory handler, letting the
// child proxy test avoid binding a real listener.
type handlerTransport struct {
	h http.Handler
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// statusTransport returns a fixed-status JSON response, for testing upstream
// error passthrough (e.g. 401 -> 502 UPSTREAM_AUTH_FAILED).
type statusTransport struct {
	status int
}

func (t statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.status,
		Status:     strconv.Itoa(t.status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"upstream"}}`))),
		Request:    req,
	}, nil
}
