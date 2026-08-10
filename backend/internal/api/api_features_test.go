package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"servercli/internal/model"
	"servercli/internal/store"
)

func mustDecode[T any](t *testing.T, out []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	return v
}

func leaseRequestBody(nodeSelector, agentID, profile string) map[string]any {
	return map[string]any{
		"node_selector":              nodeSelector,
		"public_key":                 "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKmzTestFakeKey device-test",
		"permission_profile":         profile,
		"requested_duration_seconds": 1800,
		"purpose":                    "api feature test",
		"client_request_id":          fmt.Sprintf("req-%d", time.Now().UnixNano()),
		"ai_agent_id":                agentID,
		"ai_agent_name":              agentID,
	}
}

func createAPIToken(t *testing.T, env *testEnv, name, ttl string) (tokenID, plaintext string) {
	t.Helper()
	status, out := env.post("/api/v1/api-tokens", map[string]any{"name": name, "ttl": ttl}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create api token status %d: %s", status, out)
	}
	var resp struct {
		APIToken struct {
			ID string `json:"id"`
		} `json:"api_token"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode token response %s: %v", out, err)
	}
	if resp.APIToken.ID == "" || resp.Token == "" || !strings.HasPrefix(resp.Token, "sct_") {
		t.Fatalf("token response incomplete: %s", out)
	}
	return resp.APIToken.ID, resp.Token
}

func tokenHeaders(plaintext string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + plaintext}
}

// putPermissions assigns a grant set to a token via the admin permissions API
// under an optimistic lock and returns the new permission revision.
func putPermissions(t *testing.T, env *testEnv, tokenID string, revision int, grants []map[string]any) int {
	t.Helper()
	status, out := env.serve("PUT", "/api/v1/api-tokens/"+tokenID+"/permissions", map[string]any{
		"permission_version": revision,
		"permissions":        map[string]any{"version": 1, "grants": grants},
	}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("grant permissions status %d: %s", status, out)
	}
	var resp struct {
		APIToken struct {
			PermissionVersion int `json:"permission_version"`
		} `json:"api_token"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode grant response %s: %v", out, err)
	}
	if resp.APIToken.PermissionVersion != revision+1 {
		t.Fatalf("expected permission revision %d, got %d: %s", revision+1, resp.APIToken.PermissionVersion, out)
	}
	return resp.APIToken.PermissionVersion
}

// grantAIPermissions assigns the full AI credential surface (nodes:read,
// ai.lease_requests:create/read, ai.leases:renew/heartbeat/disconnect) to a
// token and returns the new permission revision. New tokens start with zero
// permissions, so AI API calls require this grant.
func grantAIPermissions(t *testing.T, env *testEnv, tokenID string, revision int) int {
	t.Helper()
	return putPermissions(t, env, tokenID, revision, []map[string]any{
		{"resource": "nodes", "actions": []string{"read"}},
		{"resource": "ai.lease_requests", "actions": []string{"create", "read"}},
		{"resource": "ai.leases", "actions": []string{"renew", "heartbeat", "disconnect"}},
	})
}

// grantNotificationsPermission grants only notifications:send to a token and
// returns the new permission revision.
func grantNotificationsPermission(t *testing.T, env *testEnv, tokenID string, revision int) int {
	t.Helper()
	return putPermissions(t, env, tokenID, revision, []map[string]any{
		{"resource": "notifications", "actions": []string{"send"}},
	})
}

func TestAccessTokenLeaseFlow(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	// Missing or invalid tokens are rejected with 401 and no token leaks.
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "read-only"), nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d: %s", status, out)
	}
	if strings.Contains(string(out), "sct_") {
		t.Fatalf("error response leaked token material: %s", out)
	}
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "read-only"),
		tokenHeaders("sct_"+strings.Repeat("0", 64)))
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid token should be 401, got %d: %s", status, out)
	}

	// Token management requires the admin session.
	status, _ = env.serve("GET", "/api/v1/api-tokens", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("api-tokens without session should be 401, got %d", status)
	}

	// Create tokens: short-lived (15m), medium (1h) and permanent.
	shortID, shortTok := createAPIToken(t, env, "short-lived", "15m")
	midID, midTok := createAPIToken(t, env, "one-hour", "1h")
	permID, permTok := createAPIToken(t, env, "permanent", "never")
	// New tokens start with zero permissions: grant the AI credential surface
	// before any AI API call.
	grantAIPermissions(t, env, shortID, 1)
	grantAIPermissions(t, env, midID, 1)
	grantAIPermissions(t, env, permID, 1)

	// Lease request with a valid token is auto-approved and bound to the token.
	h := tokenHeaders(shortTok)
	h["Idempotency-Key"] = "token-flow-1"
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "read-only"), h)
	if status != http.StatusCreated {
		t.Fatalf("create lease request status %d: %s", status, out)
	}
	reqResp := mustDecode[struct {
		LeaseRequest struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			AccessTokenID   string `json:"access_token_id"`
			AccessTokenName string `json:"access_token_name"`
		} `json:"lease_request"`
		Lease struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			ExpiresAt     string `json:"expires_at"`
			AccessTokenID string `json:"access_token_id"`
		} `json:"lease"`
	}](t, out)
	if reqResp.LeaseRequest.Status != model.LeaseRequestApproved {
		t.Fatalf("request not auto-approved: %s", out)
	}
	if reqResp.Lease.Status != model.LeaseActive || reqResp.Lease.ID == "" {
		t.Fatalf("lease not issued: %s", out)
	}
	if reqResp.LeaseRequest.AccessTokenID != shortID || reqResp.Lease.AccessTokenID != shortID {
		t.Fatalf("lease not bound to token: %s", out)
	}
	if reqResp.LeaseRequest.AccessTokenName != "short-lived" {
		t.Fatalf("token name not surfaced: %s", out)
	}
	// Short token (15m) bounds the lease expiry even though 30min was requested.
	expires, _ := time.Parse(time.RFC3339Nano, reqResp.Lease.ExpiresAt)
	if remaining := time.Until(expires); remaining > 16*time.Minute || remaining < 13*time.Minute {
		t.Fatalf("lease expiry not bounded by token TTL: %v", remaining)
	}

	// Ownership isolation: a different token cannot read or renew this lease.
	status, out = env.serve("GET", "/api/v1/ai/lease-requests/"+reqResp.LeaseRequest.ID, nil, tokenHeaders(permTok))
	if status != http.StatusNotFound {
		t.Fatalf("foreign read should be 404, got %d: %s", status, out)
	}
	status, out = env.serve("POST", "/api/v1/ai/leases/"+reqResp.Lease.ID+"/renew",
		map[string]any{"requested_duration_seconds": 600}, tokenHeaders(permTok))
	if status != http.StatusNotFound {
		t.Fatalf("foreign renew should be 404, got %d: %s", status, out)
	}

	// The owner can renew, heartbeat and disconnect with the access token
	// (medium token so the lease has headroom below the token expiry).
	hMid := tokenHeaders(midTok)
	hMid["Idempotency-Key"] = "token-flow-mid"
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "read-only"), hMid)
	if status != http.StatusCreated {
		t.Fatalf("mid lease request status %d: %s", status, out)
	}
	mid := mustDecode[struct {
		Lease struct {
			ID string `json:"id"`
		} `json:"lease"`
	}](t, out)
	status, out = env.serve("POST", "/api/v1/ai/leases/"+mid.Lease.ID+"/renew",
		map[string]any{"requested_duration_seconds": 3600}, tokenHeaders(midTok))
	if status != http.StatusOK {
		t.Fatalf("renew status %d: %s", status, out)
	}
	status, out = env.serve("POST", "/api/v1/ai/leases/"+mid.Lease.ID+"/heartbeat", map[string]any{}, tokenHeaders(midTok))
	if status != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", status, out)
	}
	status, out = env.serve("POST", "/api/v1/ai/leases/"+mid.Lease.ID+"/disconnect", map[string]any{}, tokenHeaders(midTok))
	if status != http.StatusOK {
		t.Fatalf("disconnect status %d: %s", status, out)
	}

	// Every recognized-token request produced a usage log row.
	status, out = env.serve("GET", "/api/v1/api-tokens/"+shortID+"/usage-logs?limit=100", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("usage logs status %d: %s", status, out)
	}
	logs := mustDecode[struct {
		UsageLogs []struct {
			Action  string `json:"action"`
			Outcome string `json:"outcome"`
			TokenID string `json:"token_id"`
			LeaseID string `json:"lease_id"`
		} `json:"usage_logs"`
	}](t, out)
	if len(logs.UsageLogs) == 0 {
		t.Fatalf("expected usage logs, got 0: %s", out)
	}
	if logs.UsageLogs[0].TokenID != shortID {
		t.Fatalf("usage log token mismatch: %s", out)
	}

	// Revoking the token revokes its active leases (the short-token lease is
	// still active) and blocks further use.
	status, out = env.post("/api/v1/api-tokens/"+shortID+"/revoke",
		map[string]any{"reason": "rotation"}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("revoke token status %d: %s", status, out)
	}
	rev := mustDecode[struct {
		RevokedLeaseCount int `json:"revoked_lease_count"`
	}](t, out)
	if rev.RevokedLeaseCount != 1 {
		t.Fatalf("expected 1 cascaded lease revoke, got %d: %s", rev.RevokedLeaseCount, out)
	}
	fromDB, err := env.st.LeaseByID(ctx, reqResp.Lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fromDB.Status != model.LeaseRevoked {
		t.Fatalf("lease not revoked after token revoke: %s", fromDB.Status)
	}
	// The revoked token no longer authenticates.
	status, _ = env.serve("POST", "/api/v1/ai/leases/"+reqResp.Lease.ID+"/heartbeat", map[string]any{}, tokenHeaders(shortTok))
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token should be 401, got %d", status)
	}

	// Token list shows revocation state and no plaintext/hash.
	status, out = env.serve("GET", "/api/v1/api-tokens", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("token list status %d: %s", status, out)
	}
	list := mustDecode[struct {
		APITokens []map[string]any `json:"api_tokens"`
	}](t, out)
	for _, tok := range list.APITokens {
		if _, has := tok["token_hash"]; has {
			t.Fatalf("token list leaked token_hash")
		}
		if _, has := tok["token"]; has {
			t.Fatalf("token list leaked plaintext")
		}
	}
}

func TestAccessTokenTTLBoundsLease(t *testing.T) {
	env := setupAPI(t)

	permID, permTok := createAPIToken(t, env, "perm", "never")
	grantAIPermissions(t, env, permID, 1)

	// A permanent token cannot exceed the system absolute lease cap (24h).
	h := tokenHeaders(permTok)
	h["Idempotency-Key"] = "ttl-perm-1"
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-P", "read-only"), h)
	if status != http.StatusCreated {
		t.Fatalf("permanent token request status %d: %s", status, out)
	}
	perm := mustDecode[struct {
		Lease struct {
			ExpiresAt         string `json:"expires_at"`
			AbsoluteExpiresAt string `json:"absolute_expires_at"`
		} `json:"lease"`
	}](t, out)
	abs, _ := time.Parse(time.RFC3339Nano, perm.Lease.AbsoluteExpiresAt)
	if time.Until(abs) > 25*time.Hour {
		t.Fatalf("absolute cap violated: %s", perm.Lease.AbsoluteExpiresAt)
	}

	// 1h token with a 6h request: lease expires at the token, not 6h.
	hID, hTok := createAPIToken(t, env, "one-hour", "1h")
	grantAIPermissions(t, env, hID, 1)
	h2 := tokenHeaders(hTok)
	h2["Idempotency-Key"] = "ttl-1h-1"
	body := leaseRequestBody(env.nodeID, "device-P", "read-only")
	body["requested_duration_seconds"] = 6 * 3600
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", body, h2)
	if status != http.StatusCreated {
		t.Fatalf("1h token request status %d: %s", status, out)
	}
	one := mustDecode[struct {
		Lease struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"lease"`
	}](t, out)
	exp, _ := time.Parse(time.RFC3339Nano, one.Lease.ExpiresAt)
	if remaining := time.Until(exp); remaining > 61*time.Minute || remaining < 55*time.Minute {
		t.Fatalf("lease expiry not bounded by 1h token: %v", remaining)
	}
}

func TestTokenManagementChildScopeForbidden(t *testing.T) {
	env := setupAPI(t)
	env.srv.SetChildScope(env.nodeID)
	status, out := env.post("/api/v1/api-tokens", map[string]any{"name": "child", "ttl": "1h"}, env.adminHeaders())
	if status != http.StatusForbidden {
		t.Fatalf("token creation under child scope should be 403, got %d: %s", status, out)
	}
	status, out = env.serve("GET", "/api/v1/api-tokens", nil, env.adminHeaders())
	if status != http.StatusForbidden {
		t.Fatalf("token list under child scope should be 403, got %d: %s", status, out)
	}
}

func TestNodeDeleteGuardrailsAndCascade(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	mkNode := func(role, status string, enabled bool, name string) string {
		id := model.NewUUID()
		n := &model.Node{
			ID:            id,
			EnvironmentID: env.nodes.EnvID(),
			InstanceName:  name,
			Role:          role,
			Status:        status,
			Enabled:       enabled,
		}
		if err := env.st.CreateNode(ctx, n); err != nil {
			t.Fatal(err)
		}
		return id
	}
	offlineID := mkNode("child", model.NodeStatusOffline, true, "offline-node")
	onlineID := mkNode("child", model.NodeStatusOnline, true, "online-node")
	primaryID := mkNode("primary", model.NodeStatusOnline, true, "primary-node")

	del := func(id, confirm string) (int, []byte) {
		return env.serve("DELETE", "/api/v1/nodes/"+id, map[string]any{"confirm_instance_name": confirm}, env.adminHeaders())
	}

	// Online node cannot be deleted.
	if status, _ := del(onlineID, "online-node"); status != http.StatusConflict {
		t.Fatalf("online delete should be 409, got %d", status)
	}
	// Primary can never be deleted.
	if status, _ := del(primaryID, "primary-node"); status != http.StatusForbidden {
		t.Fatalf("primary delete should be 403, got %d", status)
	}
	// Wrong confirmation text.
	if status, _ := del(offlineID, "wrong-name"); status != http.StatusBadRequest {
		t.Fatalf("wrong confirm should be 400, got %d", status)
	}
	// Missing confirmation.
	if status, _ := del(offlineID, ""); status != http.StatusBadRequest {
		t.Fatalf("empty confirm should be 400, got %d", status)
	}

	// Seed associated data for the offline node.
	if err := env.st.CreateTask(ctx, &model.Task{
		ID: model.NewUUID(), NodeID: offlineID, CommandID: "x.cmd", CommandVersion: "1",
		RequestedBy: "admin", IdempotencyKey: "del-k", ArgumentsJSON: `{"a":1}`,
		TimeoutSeconds: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.st.RecordTaskParameterUsage(ctx, offlineID, "x.cmd", "1", `{"a":1}`, "", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.UpsertAutoApproval(ctx, &model.AIAutoApproval{
		EnvironmentID: env.nodes.EnvID(), AIAgentID: "device-X", NodeID: offlineID, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Legit delete succeeds.
	if status, out := del(offlineID, "offline-node"); status != http.StatusNoContent {
		t.Fatalf("offline delete should be 204, got %d: %s", status, out)
	}

	// Node and all associated data are gone.
	if _, err := env.st.NodeByID(ctx, offlineID); err != store.ErrNotFound {
		t.Fatalf("node still present: %v", err)
	}
	histAfter, err := env.st.ListTaskParameterHistories(ctx, []string{offlineID}, "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(histAfter) != 0 {
		t.Fatalf("parameter history rows not cascaded: %d rows remain", len(histAfter))
	}
	rows, err := env.st.ListAutoApprovals(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.NodeID == offlineID {
			t.Fatalf("auto-approval rule not cascaded")
		}
	}
	// The delete audit survives (node_id intentionally empty).
	audits, err := env.st.ListAuditEvents(ctx, store.AuditFilter{Action: "node.delete"})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 {
		t.Fatalf("expected 1 node.delete audit, got %d", len(audits))
	}
	if audits[0].NodeID != "" {
		t.Fatalf("delete audit should not bind a node_id, got %q", audits[0].NodeID)
	}
}

func TestTaskParameterHistory(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	schema := `{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}}}`
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "calc.add", "command_version": "1.0.0", "category": "calc",
		"title": "add", "permission_profile": "read-only", "timeout_seconds": 20,
		"max_output_bytes": 4096, "enabled": true, "parameter_schema_json": schema,
	}}})
	status, out := env.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, env.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatalf("snapshot status %d: %s", status, out)
	}

	createTask := func(args map[string]any, key string) {
		h := env.adminHeaders()
		h["Idempotency-Key"] = key
		status, out := env.post("/api/v1/nodes/"+env.nodeID+"/tasks",
			map[string]any{"command_id": "calc.add", "command_version": "1.0.0", "arguments": args}, h)
		if status != http.StatusCreated {
			t.Fatalf("create task status %d: %s", status, out)
		}
	}

	// Same arguments with different JSON key order must dedupe to one entry.
	createTask(map[string]any{"a": 1, "b": 2}, "hist-key-1")
	createTask(map[string]any{"b": 2, "a": 1}, "hist-key-2")
	createTask(map[string]any{"a": 9, "b": 2}, "hist-key-3")

	status, out = env.serve("GET", "/api/v1/task-parameter-histories?node_id="+env.nodeID+"&command_id=calc.add&command_version=1.0.0", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list histories status %d: %s", status, out)
	}
	hist := mustDecode[struct {
		Histories []struct {
			ID        string         `json:"id"`
			UseCount  int            `json:"use_count"`
			Arguments map[string]any `json:"arguments"`
			NodeID    string         `json:"node_id"`
		} `json:"histories"`
	}](t, out)
	if len(hist.Histories) != 2 {
		t.Fatalf("expected 2 histories, got %d: %s", len(hist.Histories), out)
	}
	byArgs := map[string]int{}
	var targetID string
	for _, h := range hist.Histories {
		byArgs[formatArgs(h.Arguments)] = h.UseCount
		if h.NodeID != env.nodeID {
			t.Fatalf("history node mismatch: %s", out)
		}
		if h.UseCount == 2 {
			targetID = h.ID
		}
	}
	if byArgs["a=1 b=2"] != 2 || byArgs["a=9 b=2"] != 1 {
		t.Fatalf("unexpected use counts: %+v", byArgs)
	}

	// Delete one entry.
	status, out = env.serve("DELETE", "/api/v1/task-parameter-histories/"+targetID, nil, env.adminHeaders())
	if status != http.StatusNoContent {
		t.Fatalf("delete history status %d: %s", status, out)
	}
	status, out = env.serve("GET", "/api/v1/task-parameter-histories?node_id="+env.nodeID+"&command_id=calc.add", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list after delete status %d: %s", status, out)
	}
	after := mustDecode[struct {
		Histories []map[string]any `json:"histories"`
	}](t, out)
	if len(after.Histories) != 1 {
		t.Fatalf("expected 1 history after delete, got %d: %s", len(after.Histories), out)
	}

	// Backfill is idempotent and derives counts from the task table.
	if err := env.srv.TaskService().BackfillParameterHistories(ctx); err != nil {
		t.Fatal(err)
	}
	status, out = env.serve("GET", "/api/v1/task-parameter-histories?node_id="+env.nodeID+"&command_id=calc.add", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list after backfill status %d: %s", status, out)
	}
	backfilled := mustDecode[struct {
		Histories []struct {
			UseCount int `json:"use_count"`
		} `json:"histories"`
	}](t, out)
	if len(backfilled.Histories) != 2 {
		t.Fatalf("backfill should restore 2 histories, got %d: %s", len(backfilled.Histories), out)
	}
}

func formatArgs(m map[string]any) string {
	out := ""
	for _, k := range []string{"a", "b"} {
		if v, ok := m[k]; ok {
			out += k + "=" + fmt.Sprint(v) + " "
		}
	}
	return strings.TrimSpace(out)
}

func TestOpenAPIDirectoryCoversRegisteredRoutes(t *testing.T) {
	env := setupAPI(t)
	status, out := env.serve("GET", "/api/v1/meta/openapi", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("openapi status %d: %s", status, out)
	}
	doc := mustDecode[struct {
		Paths []map[string]any `json:"paths"`
	}](t, out)
	if len(doc.Paths) == 0 {
		t.Fatalf("openapi directory empty")
	}
	// Every route the mux knows about must appear in the directory.
	specs := env.srv.apiRoutes()
	if len(doc.Paths) != len(specs) {
		t.Fatalf("directory has %d routes but mux registered %d", len(doc.Paths), len(specs))
	}
	// Spot-check that the token and AI routes are present.
	seen := map[string]bool{}
	for _, p := range doc.Paths {
		seen[p["method"].(string)+" "+p["path"].(string)] = true
	}
	for _, want := range []string{
		"POST /api/v1/api-tokens",
		"POST /api/v1/ai/lease-requests",
		"POST /api/v1/ai/leases/{id}/renew",
		"GET /api/v1/ai/leases/{id}/status",
		"GET /api/v1/meta/openapi",
	} {
		if !seen[want] {
			t.Fatalf("directory missing route %s", want)
		}
	}
}

func TestLegacyAutoApprovalRoutesRetired(t *testing.T) {
	env := setupAPI(t)

	// The old manual-approval and auto-approval routes are gone (404).
	for path, method := range map[string]string{
		"/api/v1/ai/auto-approvals":                       "GET",
		"/api/v1/ai/lease-requests/some-id/approve":       "POST",
		"/api/v1/ai/lease-requests/some-id/reject":        "POST",
		"/api/v1/ai/lease-requests/some-id/auto-approval": "POST",
		"/api/v1/ai/auto-approvals/some-id/extend":        "POST",
	} {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range env.adminHeaders() {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s should be 404, got %d", method, path, rec.Code)
		}
	}

	// Legacy pending requests are rejected by the startup migration.
	ctx := context.Background()
	legacy := &model.AILeaseRequest{
		ID: model.NewUUID(), ClientRequestID: "legacy-1", EnvironmentID: env.nodes.EnvID(),
		NodeID: env.nodeID, RequestedProfile: "read-only", RequestedDurationSeconds: 1800,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKmzTestFakeKey legacy", Status: model.LeaseRequestPending,
	}
	if err := env.st.CreateLeaseRequest(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	// A legacy active lease without a bound token must be revoked, and one
	// bound to an already-expired token must be expired.
	now := time.Now().UTC()
	mkLegacyRequest := func(id, key string) {
		if err := env.st.CreateLeaseRequest(ctx, &model.AILeaseRequest{
			ID: id, ClientRequestID: key, EnvironmentID: env.nodes.EnvID(),
			NodeID: env.nodeID, RequestedProfile: "read-only", RequestedDurationSeconds: 1800,
			PublicKey: "ssh-ed25519 AAAA... legacy", Status: model.LeaseRequestApproved,
		}); err != nil {
			t.Fatal(err)
		}
	}
	untokReqID := model.NewUUID()
	mkLegacyRequest(untokReqID, "legacy-untok")
	untok := &model.AILease{
		ID: model.NewUUID(), RequestID: untokReqID, NodeID: env.nodeID,
		PermissionProfile: "read-only", PublicKey: "ssh-ed25519 AAAA... legacy-untok",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(23 * time.Hour),
		Status: model.LeaseActive,
	}
	if err := env.st.CreateLease(ctx, untok); err != nil {
		t.Fatal(err)
	}
	expTok := &model.APIAccessToken{
		ID: model.NewUUID(), EnvironmentID: env.nodes.EnvID(), Name: "expired-legacy",
		TokenHash: "deadbeef", TokenPrefix: "sct_deadbeef", CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: &now,
	}
	if err := env.st.CreateAccessToken(ctx, expTok); err != nil {
		t.Fatal(err)
	}
	expReqID := model.NewUUID()
	mkLegacyRequest(expReqID, "legacy-exp")
	expLease := &model.AILease{
		ID: model.NewUUID(), RequestID: expReqID, NodeID: env.nodeID, AccessTokenID: expTok.ID,
		PermissionProfile: "read-only", PublicKey: "ssh-ed25519 AAAA... legacy-exp",
		IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute), AbsoluteExpiresAt: now.Add(22 * time.Hour),
		Status: model.LeaseActive,
	}
	if err := env.st.CreateLease(ctx, expLease); err != nil {
		t.Fatal(err)
	}

	if err := env.srv.LeaseService().MigrateLegacyApprovalFlow(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := env.st.LeaseRequestByID(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.LeaseRequestRejected {
		t.Fatalf("legacy pending request not rejected: %s", after.Status)
	}
	untokAfter, err := env.st.LeaseByID(ctx, untok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if untokAfter.Status != model.LeaseRevoked {
		t.Fatalf("legacy untokenized active lease not revoked: %s", untokAfter.Status)
	}
	expAfter, err := env.st.LeaseByID(ctx, expLease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expAfter.Status != model.LeaseExpired {
		t.Fatalf("legacy expired lease not expired by migration: %s", expAfter.Status)
	}
}

func TestAgentTaskParameterHistoryEndpoints(t *testing.T) {
	env := setupAPI(t)

	schema := `{"type":"object","properties":{"a":{"type":"integer"}}}`
	snapBody := mustJSON(map[string]any{"commands": []map[string]any{{
		"command_id": "calc.one", "command_version": "1.0.0", "category": "calc",
		"title": "one", "permission_profile": "read-only", "timeout_seconds": 20,
		"max_output_bytes": 4096, "enabled": true, "parameter_schema_json": schema,
	}}})
	status, out := env.serveRaw("POST", "/api/v1/agent/commands/snapshot", snapBody, env.agentHeaders("POST", "/api/v1/agent/commands/snapshot", snapBody))
	if status != http.StatusOK {
		t.Fatalf("snapshot status %d: %s", status, out)
	}
	h := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(map[string]any{
		"command_id": "calc.one", "command_version": "1.0.0", "arguments": map[string]any{"a": 1},
	}))
	h["Idempotency-Key"] = "agent-hist-key-1"
	status, out = env.serve("POST", "/api/v1/agent/tasks", map[string]any{
		"command_id": "calc.one", "command_version": "1.0.0", "arguments": map[string]any{"a": 1},
	}, h)
	if status != http.StatusCreated {
		t.Fatalf("agent create task status %d: %s", status, out)
	}

	// Agent can read its own node's parameter history via the self-service API.
	listPath := "/api/v1/agent/task-parameter-histories"
	status, out = env.serve("GET", listPath+"?command_id=calc.one", nil,
		env.agentHeaders("GET", listPath, nil))
	if status != http.StatusOK {
		t.Fatalf("agent list histories status %d: %s", status, out)
	}
	hist := mustDecode[struct {
		Histories []struct {
			ID string `json:"id"`
		} `json:"histories"`
	}](t, out)
	if len(hist.Histories) != 1 {
		t.Fatalf("expected 1 history via agent API, got %d: %s", len(hist.Histories), out)
	}

	// Agent can delete its own history row.
	delPath := "/api/v1/agent/task-parameter-histories/" + hist.Histories[0].ID
	status, out = env.serve("DELETE", delPath, nil, env.agentHeaders("DELETE", delPath, nil))
	if status != http.StatusNoContent {
		t.Fatalf("agent delete history status %d: %s", status, out)
	}
	status, out = env.serve("GET", listPath+"?command_id=calc.one", nil,
		env.agentHeaders("GET", listPath, nil))
	if status != http.StatusOK {
		t.Fatalf("agent list after delete status %d: %s", status, out)
	}
	after := mustDecode[struct {
		Histories []map[string]any `json:"histories"`
	}](t, out)
	if len(after.Histories) != 0 {
		t.Fatalf("history not deleted via agent API: %s", out)
	}
}

// TestIdempotencyReplayOwnershipIsolation ensures a client_request_id owned by
// another token never replays that request/lease back (404, no leak).
func TestIdempotencyReplayOwnershipIsolation(t *testing.T) {
	env := setupAPI(t)

	idA, tokA := createAPIToken(t, env, "owner-A", "1h")
	idB, tokB := createAPIToken(t, env, "owner-B", "1h")
	grantAIPermissions(t, env, idA, 1)
	grantAIPermissions(t, env, idB, 1)

	// A creates a request keyed solely by the Idempotency-Key header.
	body := leaseRequestBody(env.nodeID, "device-A", "read-only")
	delete(body, "client_request_id")
	hA := tokenHeaders(tokA)
	hA["Idempotency-Key"] = "shared-key-1"
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", body, hA)
	if status != http.StatusCreated {
		t.Fatalf("owner A request status %d: %s", status, out)
	}

	// B replays the same key: must be 404 and must not leak A's data.
	hB := tokenHeaders(tokB)
	hB["Idempotency-Key"] = "shared-key-1"
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", body, hB)
	if status != http.StatusNotFound {
		t.Fatalf("cross-token replay should be 404, got %d: %s", status, out)
	}
	if strings.Contains(string(out), "device-A") || strings.Contains(string(out), "ssh-ed25519") {
		t.Fatalf("cross-token replay leaked owner A data: %s", out)
	}

	// A can still replay its own key (200/201, not a leak).
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", body, hA)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("owner A replay should succeed, got %d: %s", status, out)
	}
}

func TestExpiredAccessTokenRejected(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	tokenID, tok := createAPIToken(t, env, "expired", "1h")
	// Force the token expiry into the past.
	if _, err := env.st.DB().ExecContext(ctx,
		`UPDATE api_access_token SET expires_at=$1 WHERE id=$2`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), tokenID); err != nil {
		t.Fatal(err)
	}

	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "exp", "read-only"), tokenHeaders(tok))
	if status != http.StatusUnauthorized {
		t.Fatalf("expired token should be 401, got %d: %s", status, out)
	}
	// Usage log records token_state=expired.
	status, out = env.serve("GET", "/api/v1/api-tokens/"+tokenID+"/usage-logs?limit=10", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("usage logs status %d: %s", status, out)
	}
	logs := mustDecode[struct {
		UsageLogs []struct {
			TokenState string `json:"token_state"`
			Outcome    string `json:"outcome"`
		} `json:"usage_logs"`
	}](t, out)
	if len(logs.UsageLogs) == 0 {
		t.Fatalf("expected a usage log for the expired token request")
	}
	found := false
	for _, l := range logs.UsageLogs {
		if l.TokenState == "expired" && l.Outcome == "denied" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no expired/denied usage log recorded: %+v", logs.UsageLogs)
	}
}

// TestPublicKeyInjectionRejected ensures a public key with newlines/options that
// could inject extra authorized_keys lines is rejected at the API.
func TestPublicKeyInjectionRejected(t *testing.T) {
	env := setupAPI(t)

	id, tok := createAPIToken(t, env, "inject", "1h")
	grantAIPermissions(t, env, id, 1)
	for _, bad := range []string{
		"ssh-ed25519 AAAA...\ncommand=\"/bin/sh\" ssh-ed25519 BBBB...",
		"ssh-ed25519 AAAA,no-agent-forwarding BBBB",
		"ssh-rsa AAAA...",
		"garbage-key",
		"ssh-ed25519 AAAA... \"quoted\"",
	} {
		body := leaseRequestBody(env.nodeID, "inj", "read-only")
		body["public_key"] = bad
		status, out := env.serve("POST", "/api/v1/ai/lease-requests", body, tokenHeaders(tok))
		if status != http.StatusBadRequest {
			t.Fatalf("public_key %q should be 400, got %d: %s", bad, status, out)
		}
	}
	// A legitimate key still works.
	body := leaseRequestBody(env.nodeID, "inj", "read-only")
	body["public_key"] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKmzTestFakeKey device-test"
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", body, tokenHeaders(tok))
	if status != http.StatusCreated {
		t.Fatalf("legitimate key should be accepted, got %d: %s", status, out)
	}
}

// TestAccessTokenNodeDiscovery verifies that read-only node discovery accepts
// an Access Token (dual auth), while write operations still require an admin
// session.
func TestAccessTokenNodeDiscovery(t *testing.T) {
	env := setupAPI(t)

	id, tok := createAPIToken(t, env, "discover", "1h")
	grantAIPermissions(t, env, id, 1)
	h := tokenHeaders(tok)

	// Token can list nodes (used by the skill to resolve node_id).
	status, out := env.serve("GET", "/api/v1/nodes", nil, h)
	if status != http.StatusOK {
		t.Fatalf("token node list should be 200, got %d: %s", status, out)
	}
	nodes := mustDecode[struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}](t, out)
	if len(nodes.Nodes) == 0 {
		t.Fatalf("expected at least the enrolled node")
	}
	found := false
	for _, n := range nodes.Nodes {
		if n.ID == env.nodeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("enrolled node not in list: %s", out)
	}

	// Token can read one node.
	status, _ = env.serve("GET", "/api/v1/nodes/"+env.nodeID, nil, h)
	if status != http.StatusOK {
		t.Fatalf("token node detail should be 200, got %d", status)
	}

	// Write operations stay admin-only: token without session -> 401.
	status, _ = env.serve("PATCH", "/api/v1/nodes/"+env.nodeID, map[string]any{"alias": "x"}, h)
	if status != http.StatusUnauthorized {
		t.Fatalf("token write should be 401, got %d", status)
	}

	// No token at all -> 401.
	status, _ = env.serve("GET", "/api/v1/nodes", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous node list should be 401, got %d", status)
	}
}
