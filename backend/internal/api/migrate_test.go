package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"servercli/internal/model"
)

// bringNodeOnline sends a signed heartbeat so the node is online and
// (optionally) reports ownership rows through the real agent path.
func (e *testEnv) bringNodeOnline(t *testing.T, ownership []map[string]any) {
	t.Helper()
	body := map[string]any{"hostname": "api-node", "agent_version": "test"}
	if ownership != nil {
		body["ownership"] = ownership
	}
	raw := mustJSON(body)
	status, out := e.serveRaw("POST", "/api/v1/agent/heartbeat", raw, e.agentHeaders("POST", "/api/v1/agent/heartbeat", raw))
	if status != http.StatusOK {
		t.Fatalf("heartbeat status %d: %s", status, out)
	}
}

func TestMigrateServicesAndPlan(t *testing.T) {
	e := setupAPI(t)
	e.login(t)

	// Ownership reported through the heartbeat path (agent -> control plane).
	e.bringNodeOnline(t, []map[string]any{
		{"service": "docker", "owner": "legacy-init", "environment": "production"},
		{"service": "caddy", "owner": "servercli", "environment": "production"},
	})

	code, out := e.serve("GET", "/api/v1/migrate/services?node_id="+e.nodeID, nil, e.adminHeaders())
	if code != http.StatusOK {
		t.Fatalf("services status %d: %s", code, out)
	}
	body := string(out)
	if !strings.Contains(body, "docker") || !strings.Contains(body, "legacy-init") || !strings.Contains(body, "caddy") {
		t.Fatalf("services missing rows: %s", body)
	}

	// Plan is read-only with the fixed adopt steps.
	code, out = e.serve("GET", "/api/v1/migrate/plan?node_id="+e.nodeID+"&service=docker", nil, e.adminHeaders())
	if code != http.StatusOK {
		t.Fatalf("plan status %d: %s", code, out)
	}
	body = string(out)
	if !strings.Contains(body, "只读发现") || !strings.Contains(body, "current_owner") || !strings.Contains(body, "legacy-init") {
		t.Fatalf("plan unexpected: %s", body)
	}

	// Plan without service is 400.
	code, _ = e.serve("GET", "/api/v1/migrate/plan?node_id="+e.nodeID, nil, e.adminHeaders())
	if code != http.StatusBadRequest {
		t.Fatalf("plan without service should be 400, got %d", code)
	}
}

func TestMigrateOpsDispatch(t *testing.T) {
	e := setupAPI(t)
	e.login(t)
	e.bringNodeOnline(t, []map[string]any{{"service": "docker", "owner": "legacy-init"}})

	// Advertise the servercli-ops command on the node.
	ctx := context.Background()
	if err := e.st.UpsertNodeCommand(ctx, &model.NodeCommand{
		NodeID: e.nodeID, CommandID: "servercli-ops", CommandVersion: "1.0.0",
		CapabilityID: "servercli.ops", Category: "service", Title: "ServerCLI ops",
		Description: "ops", PermissionProfile: "admin", TimeoutSeconds: 1800,
		MaxOutputBytes: 1048576, Enabled: true, ManifestHash: "x", ExecutableHash: "y",
	}); err != nil {
		t.Fatal(err)
	}

	h := e.adminHeaders()
	h["Idempotency-Key"] = "migrate-idem-1"

	// confirm=false is rejected.
	code, out := e.post("/api/v1/migrate/ops", map[string]any{
		"node_id": e.nodeID, "service": "docker", "operation": "adopt", "confirm": false,
	}, h)
	if code != http.StatusBadRequest {
		t.Fatalf("adopt without confirm should be 400, got %d: %s", code, out)
	}

	// restore without backup_id is rejected.
	code, _ = e.post("/api/v1/migrate/ops", map[string]any{
		"node_id": e.nodeID, "service": "docker", "operation": "restore", "confirm": true,
	}, h)
	if code != http.StatusBadRequest {
		t.Fatalf("restore without backup_id should be 400, got %d", code)
	}

	// valid adopt dispatch -> task created.
	code, out = e.post("/api/v1/migrate/ops", map[string]any{
		"node_id": e.nodeID, "service": "docker", "operation": "adopt", "confirm": true,
	}, h)
	if code != http.StatusCreated {
		t.Fatalf("adopt dispatch status %d: %s", code, out)
	}
	if !strings.Contains(string(out), `"task"`) || !strings.Contains(string(out), `"id"`) {
		t.Fatalf("adopt dispatch response missing task: %s", out)
	}
}

func TestMigrateOpsRequiresAdvertisedCommand(t *testing.T) {
	e := setupAPI(t)
	e.login(t)
	e.bringNodeOnline(t, nil) // no command advertised

	h := e.adminHeaders()
	h["Idempotency-Key"] = "migrate-idem-2"
	code, out := e.post("/api/v1/migrate/ops", map[string]any{
		"node_id": e.nodeID, "service": "docker", "operation": "update", "confirm": true,
	}, h)
	if code != http.StatusBadRequest || !strings.Contains(string(out), "servercli-ops") {
		t.Fatalf("should require advertised command, got %d: %s", code, out)
	}
}
