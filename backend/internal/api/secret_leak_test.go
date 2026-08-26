package api

// P0 安全整改 API 层自动测试：
//   - Agent self-execute 禁止 deployment.*（403 + 审计 agent.self-execute.denied）
//   - Task Event 经 API 上报后落库已脱敏（Secret 不落事件）

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"servercli/internal/store"
)

const (
	apiLeakAKID = "LTAI5tApiLeakAkId123456"
	apiLeakSK   = "ApiLeakSecretValue1234567890"
)

// TestAgentSelfExecuteDeploymentDenied verifies a node cannot self-execute a
// deployment command through the agent API; the denial is audited.
func TestAgentSelfExecuteDeploymentDenied(t *testing.T) {
	env := setupAPI(t)

	body := map[string]any{"command_id": "deployment.install", "command_version": "1.0.0", "arguments": map[string]any{}}
	headers := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(body))
	headers["Idempotency-Key"] = "agent-deploy-denied-1"
	status, out := env.serve("POST", "/api/v1/agent/tasks", body, headers)
	if status != http.StatusForbidden {
		t.Fatalf("deployment.* self-execute should be 403, got %d: %s", status, out)
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &resp)
	if resp.Error.Code != "FORBIDDEN" {
		t.Fatalf("expected FORBIDDEN code, got %q", resp.Error.Code)
	}

	// The denial must be audited with safe details (command_id + node_id).
	events, err := env.st.ListAuditEvents(context.Background(), store.AuditFilter{Action: "agent.self-execute.denied", NodeID: env.nodeID})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 denied audit event, got %d", len(events))
	}
	if !strings.Contains(events[0].DetailsJSON, "\"command_id\":\"deployment.install\"") {
		t.Fatalf("denied audit missing command_id in details: %s", events[0].DetailsJSON)
	}
	if !strings.Contains(events[0].DetailsJSON, "\"node_id\":\""+env.nodeID+"\"") {
		t.Fatalf("denied audit missing node_id in details: %s", events[0].DetailsJSON)
	}
	if strings.Contains(events[0].DetailsJSON, apiLeakAKID) || strings.Contains(events[0].DetailsJSON, apiLeakSK) {
		t.Fatalf("denied audit leaked secret: %s", events[0].DetailsJSON)
	}
}

// TestAgentTaskEventRedactedBeforePersist verifies an event message carrying
// Aliyun credentials is redacted before it lands in task_event via the API.
func TestAgentTaskEventRedactedBeforePersist(t *testing.T) {
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

	createBody := map[string]any{"command_id": "service.status", "command_version": "1.0.0", "arguments": map[string]any{"service": "sshd"}}
	createHeaders := env.agentHeaders("POST", "/api/v1/agent/tasks", mustJSON(createBody))
	createHeaders["Idempotency-Key"] = "agent-secret-event-1"
	status, out = env.serve("POST", "/api/v1/agent/tasks", createBody, createHeaders)
	if status != http.StatusCreated {
		t.Fatalf("create task status %d: %s", status, out)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	_ = json.Unmarshal(out, &created)

	evBody := map[string]any{"event_type": "started", "sequence": 1,
		"message": "using accessKeyId=" + apiLeakAKID + " accessKeySecret=" + apiLeakSK}
	evPath := "/api/v1/agent/tasks/" + created.Task.ID + "/events"
	status, out = env.serve("POST", evPath, evBody, env.agentHeaders("POST", evPath, mustJSON(evBody)))
	if status != http.StatusOK {
		t.Fatalf("event status %d: %s", status, out)
	}

	detailPath := "/api/v1/agent/tasks/" + created.Task.ID
	status, out = env.serve("GET", detailPath, nil, env.agentHeaders("GET", detailPath, nil))
	if status != http.StatusOK {
		t.Fatalf("get task status %d: %s", status, out)
	}
	if strings.Contains(string(out), apiLeakAKID) || strings.Contains(string(out), apiLeakSK) {
		t.Fatalf("task detail leaked secret: %s", out)
	}
	var detail struct {
		Events []map[string]any `json:"events"`
	}
	_ = json.Unmarshal(out, &detail)
	if len(detail.Events) == 0 {
		t.Fatalf("no events returned: %s", out)
	}
	msg, _ := detail.Events[0]["message"].(string)
	if strings.Contains(msg, apiLeakAKID) || strings.Contains(msg, apiLeakSK) {
		t.Fatalf("event message leaked secret: %q", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Fatalf("expected redaction marker in event message: %q", msg)
	}
}
