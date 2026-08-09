package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

func TestAutoApprovalFlow(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	// Create a second enabled node for the "different node" case.
	otherNode := &model.Node{
		ID:            model.NewUUID(),
		EnvironmentID: env.nodes.EnvID(),
		InstanceName:  "second-node",
		Role:          "child",
		Status:        model.NodeStatusOnline,
		Enabled:       true,
	}
	if err := env.st.CreateNode(ctx, otherNode); err != nil {
		t.Fatal(err)
	}

	// Initial pending request (admin profile stays pending under manual mode).
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "admin"), nil)
	if status != http.StatusCreated {
		t.Fatalf("create lease request status %d: %s", status, out)
	}
	req1 := mustDecode[struct {
		LeaseRequest struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"lease_request"`
	}](t, out)
	if req1.LeaseRequest.Status != model.LeaseRequestPending {
		t.Fatalf("expected pending, got %s", req1.LeaseRequest.Status)
	}

	// Invalid duration days are rejected.
	for _, days := range []int{0, 16, -1} {
		status, out = env.post("/api/v1/ai/lease-requests/"+req1.LeaseRequest.ID+"/auto-approval",
			map[string]any{"duration_days": days}, env.adminHeaders())
		if status != http.StatusBadRequest {
			t.Fatalf("duration_days=%d should be 400, got %d: %s", days, status, out)
		}
	}

	// Admin approves with a 5-day device-node rule.
	status, out = env.post("/api/v1/ai/lease-requests/"+req1.LeaseRequest.ID+"/auto-approval",
		map[string]any{"duration_days": 5}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("auto-approval status %d: %s", status, out)
	}
	created := mustDecode[struct {
		AutoApproval struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
			NodeID    string `json:"node_id"`
		} `json:"auto_approval"`
		LeaseRequest struct {
			Status string `json:"status"`
		} `json:"lease_request"`
		Lease struct {
			ID string `json:"id"`
		} `json:"lease"`
	}](t, out)
	if created.LeaseRequest.Status != model.LeaseRequestApproved || created.Lease.ID == "" {
		t.Fatalf("request not approved with lease: %s", out)
	}
	if created.AutoApproval.NodeID != env.nodeID {
		t.Fatalf("rule node mismatch: %s", out)
	}

	// Same device + node: admin profile now auto-approves immediately.
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "admin"), nil)
	if status != http.StatusCreated {
		t.Fatalf("second request status %d: %s", status, out)
	}
	req2 := mustDecode[struct {
		LeaseRequest struct {
			Status string `json:"status"`
		} `json:"lease_request"`
		Lease struct {
			ID string `json:"id"`
		} `json:"lease"`
	}](t, out)
	if req2.LeaseRequest.Status != model.LeaseRequestApproved || req2.Lease.ID == "" {
		t.Fatalf("rule did not auto-approve admin request: %s", out)
	}

	// Different node: rule does not match, stays pending.
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(otherNode.ID, "device-A", "operator"), nil)
	if status != http.StatusCreated {
		t.Fatalf("other node request status %d: %s", status, out)
	}
	req3 := mustDecode[struct {
		LeaseRequest struct {
			Status string `json:"status"`
		} `json:"lease_request"`
	}](t, out)
	if req3.LeaseRequest.Status != model.LeaseRequestPending {
		t.Fatalf("rule matched wrong node: %s", out)
	}

	// Different device, same node: no match.
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-B", "operator"), nil)
	if status != http.StatusCreated {
		t.Fatalf("other device request status %d: %s", status, out)
	}
	req4 := mustDecode[struct {
		LeaseRequest struct {
			Status string `json:"status"`
		} `json:"lease_request"`
	}](t, out)
	if req4.LeaseRequest.Status != model.LeaseRequestPending {
		t.Fatalf("rule matched wrong device: %s", out)
	}

	// List rules.
	status, out = env.serve("GET", "/api/v1/ai/auto-approvals", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list auto-approvals status %d: %s", status, out)
	}
	list := mustDecode[struct {
		AutoApprovals []map[string]any `json:"auto_approvals"`
	}](t, out)
	if len(list.AutoApprovals) != 1 {
		t.Fatalf("expected 1 rule, got %d: %s", len(list.AutoApprovals), out)
	}

	// Extend: +3 days from current expiry.
	exp1, _ := time.Parse(time.RFC3339Nano, created.AutoApproval.ExpiresAt)
	status, out = env.post("/api/v1/ai/auto-approvals/"+created.AutoApproval.ID+"/extend",
		map[string]any{"duration_days": 3}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("extend status %d: %s", status, out)
	}
	ext := mustDecode[struct {
		AutoApproval struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"auto_approval"`
	}](t, out)
	exp2, _ := time.Parse(time.RFC3339Nano, ext.AutoApproval.ExpiresAt)
	if !exp2.After(exp1) {
		t.Fatalf("extend did not move expiry: %s -> %s", exp1, exp2)
	}

	// Cap: extend far past the 15-day ceiling.
	status, out = env.post("/api/v1/ai/auto-approvals/"+created.AutoApproval.ID+"/extend",
		map[string]any{"duration_days": 15}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("cap extend status %d: %s", status, out)
	}
	capExt := mustDecode[struct {
		AutoApproval struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"auto_approval"`
	}](t, out)
	exp3, _ := time.Parse(time.RFC3339Nano, capExt.AutoApproval.ExpiresAt)
	ceiling := time.Now().UTC().Add(15 * 24 * time.Hour).Add(2 * time.Second)
	if exp3.After(ceiling) {
		t.Fatalf("rule exceeds 15-day ceiling: %s", exp3)
	}

	// Disabled mode cannot be bypassed by the rule.
	status, out = env.serve("PATCH", "/api/v1/settings", map[string]any{"ai_approval_mode": "disabled"}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("disable settings status %d: %s", status, out)
	}
	status, out = env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", "read-only"), nil)
	if status != http.StatusCreated {
		t.Fatalf("request under disabled mode status %d: %s", status, out)
	}
	req5 := mustDecode[struct {
		LeaseRequest struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"lease_request"`
	}](t, out)
	if req5.LeaseRequest.Status != model.LeaseRequestRejected {
		t.Fatalf("rule bypassed disabled mode: %s", out)
	}
	// The rejection must be persisted (not just echoed in the response) and a
	// denied audit recorded, so the request cannot be revived later.
	persisted, err := env.st.LeaseRequestByID(ctx, req5.LeaseRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.LeaseRequestRejected {
		t.Fatalf("disabled rejection not persisted: %s", persisted.Status)
	}
	denied, err := env.st.ListAuditEvents(ctx, store.AuditFilter{Action: "ai.lease_denied"})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) == 0 {
		t.Fatalf("no ai.lease_denied audit recorded")
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

func TestAutoApprovalNoShrinkDoubleApproveAndExpiredExtend(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	// Manual mode keeps requests pending so we can exercise the
	// "existing rule" branch of auto-approval.
	status, out := env.serve("PATCH", "/api/v1/settings", map[string]any{"ai_approval_mode": "manual"}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("set manual mode status %d: %s", status, out)
	}

	mkPending := func(profile string) string {
		status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-A", profile), nil)
		if status != http.StatusCreated {
			t.Fatalf("create request status %d: %s", status, out)
		}
		req := mustDecode[struct {
			LeaseRequest struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"lease_request"`
		}](t, out)
		if req.LeaseRequest.Status != model.LeaseRequestPending {
			t.Fatalf("request not pending under manual mode: %s", req.LeaseRequest.Status)
		}
		return req.LeaseRequest.ID
	}

	// Two pending requests exist before any rule; approve the second one first
	// with 15 days (creates the rule), then approve the older one with 1 day.
	older := mkPending("admin")
	newer := mkPending("operator")

	status, out = env.post("/api/v1/ai/lease-requests/"+newer+"/auto-approval",
		map[string]any{"duration_days": 15}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("first auto-approval status %d: %s", status, out)
	}
	first := mustDecode[struct {
		AutoApproval struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
		} `json:"auto_approval"`
	}](t, out)
	firstExpiry, _ := time.Parse(time.RFC3339Nano, first.AutoApproval.ExpiresAt)

	// Approving the older pending request with a shorter duration must NOT
	// shrink the existing exemption: it extends from the current expiry.
	status, out = env.post("/api/v1/ai/lease-requests/"+older+"/auto-approval",
		map[string]any{"duration_days": 1}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("second auto-approval status %d: %s", status, out)
	}
	second := mustDecode[struct {
		AutoApproval struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"auto_approval"`
	}](t, out)
	secondExpiry, _ := time.Parse(time.RFC3339Nano, second.AutoApproval.ExpiresAt)
	if secondExpiry.Before(firstExpiry) {
		t.Fatalf("auto-approval silently shrank exemption: %s -> %s", firstExpiry, secondExpiry)
	}

	// Double-approving the same request is rejected (terminal state).
	status, out = env.post("/api/v1/ai/lease-requests/"+newer+"/auto-approval",
		map[string]any{"duration_days": 5}, env.adminHeaders())
	if status != http.StatusConflict {
		t.Fatalf("double approve should be 409, got %d: %s", status, out)
	}

	// Expire the rule in the DB, then extend: it must reactivate from now.
	rule, err := env.st.AutoApprovalByID(ctx, first.AutoApproval.ID)
	if err != nil {
		t.Fatal(err)
	}
	rule.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	if _, err := env.st.UpsertAutoApproval(ctx, rule); err != nil {
		t.Fatal(err)
	}
	status, out = env.post("/api/v1/ai/auto-approvals/"+first.AutoApproval.ID+"/extend",
		map[string]any{"duration_days": 3}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("extend expired rule status %d: %s", status, out)
	}
	ext := mustDecode[struct {
		AutoApproval struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"auto_approval"`
	}](t, out)
	extExpiry, _ := time.Parse(time.RFC3339Nano, ext.AutoApproval.ExpiresAt)
	if !extExpiry.After(time.Now().UTC().Add(2 * 24 * time.Hour)) {
		t.Fatalf("expired rule not reactivated with +3d: %s", extExpiry)
	}
}

func TestAutoApprovalChildScopeForbidden(t *testing.T) {
	env := setupAPI(t)

	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "device-C", "operator"), nil)
	if status != http.StatusCreated {
		t.Fatalf("create request status %d: %s", status, out)
	}
	req := mustDecode[struct {
		LeaseRequest struct {
			ID string `json:"id"`
		} `json:"lease_request"`
	}](t, out)

	env.srv.SetChildScope(env.nodeID)
	for path, body := range map[string]any{
		"/api/v1/ai/lease-requests/" + req.LeaseRequest.ID + "/auto-approval": map[string]any{"duration_days": 3},
		"/api/v1/ai/auto-approvals/" + req.LeaseRequest.ID + "/extend":        map[string]any{"duration_days": 3},
	} {
		status, out = env.post(path, body, env.adminHeaders())
		if status != http.StatusForbidden {
			t.Fatalf("%s under child scope should be 403, got %d: %s", path, status, out)
		}
	}
	status, out = env.serve("GET", "/api/v1/ai/auto-approvals", nil, env.adminHeaders())
	if status != http.StatusForbidden {
		t.Fatalf("GET auto-approvals under child scope should be 403, got %d: %s", status, out)
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
