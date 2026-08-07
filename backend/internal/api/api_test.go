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
	env := &testEnv{handler: srv.Handler(), st: st, nodes: srv.NodeService(), adminID: admin.ID}
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
