package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"servercli/internal/model"
	"servercli/internal/service"
)

func TestLeaseRuntimeStatusEndpoint(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()

	// Create an access token + lease so we have a real lease row.
	id, tok := createAPIToken(t, env, "rt-test", "6h")
	grantAIPermissions(t, env, id, 1)
	h := tokenHeaders(tok)
	h["Idempotency-Key"] = "rt-1"
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "rt", "read-only"), h)
	if status != http.StatusCreated {
		t.Fatalf("lease request status %d: %s", status, out)
	}
	lease := mustDecode[struct {
		Lease struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
			NodeID    string `json:"node_id"`
		} `json:"lease"`
	}](t, out)

	master, err := service.MasterKey(env.srv.cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Missing token -> 401.
	status, _ = env.serve("GET", "/api/v1/ai/leases/"+lease.Lease.ID+"/status", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing runtime token should be 401, got %d", status)
	}

	// Valid signed runtime token -> 200 with lease status.
	exp := time.Now().UTC().Add(10 * time.Minute)
	rt, err := service.SignLeaseRuntimeToken(master, lease.Lease.ID, lease.Lease.NodeID, exp)
	if err != nil {
		t.Fatal(err)
	}
	status, out = env.serve("GET", "/api/v1/ai/leases/"+lease.Lease.ID+"/status", nil,
		map[string]string{"X-Lease-Runtime-Token": rt})
	if status != http.StatusOK {
		t.Fatalf("runtime status status %d: %s", status, out)
	}
	body := mustDecode[struct {
		Lease struct {
			Status string `json:"status"`
		} `json:"lease"`
	}](t, out)
	if body.Lease.Status != model.LeaseActive {
		t.Fatalf("runtime status not active: %s", out)
	}

	// Token bound to the wrong node -> 403.
	rtWrong, _ := service.SignLeaseRuntimeToken(master, lease.Lease.ID, "other-node", exp)
	status, _ = env.serve("GET", "/api/v1/ai/leases/"+lease.Lease.ID+"/status", nil,
		map[string]string{"X-Lease-Runtime-Token": rtWrong})
	if status != http.StatusForbidden {
		t.Fatalf("wrong-node runtime token should be 403, got %d", status)
	}

	// Expired runtime token -> 401 even though the lease is still active in DB.
	rtExpired, _ := service.SignLeaseRuntimeToken(master, lease.Lease.ID, lease.Lease.NodeID, time.Now().UTC().Add(-time.Minute))
	status, _ = env.serve("GET", "/api/v1/ai/leases/"+lease.Lease.ID+"/status", nil,
		map[string]string{"X-Lease-Runtime-Token": rtExpired})
	if status != http.StatusUnauthorized {
		t.Fatalf("expired runtime token should be 401, got %d", status)
	}

	// After revoking the lease the runtime check also fails.
	if _, err := env.srv.LeaseService().Revoke(ctx, lease.Lease.ID, "admin-1", "test", false); err != nil {
		t.Fatal(err)
	}
	status, _ = env.serve("GET", "/api/v1/ai/leases/"+lease.Lease.ID+"/status", nil,
		map[string]string{"X-Lease-Runtime-Token": rt})
	if status != http.StatusForbidden {
		t.Fatalf("runtime status after revoke should be 403, got %d", status)
	}
}

// TestRenewRefreshesNodeKeyRegistration verifies that after a renewal moves the
// lease expiry, the lease is flagged for key re-installation so the node gets a
// freshly signed runtime token / expiry marker (P1-1 regression guard).
func TestRenewRefreshesNodeKeyRegistration(t *testing.T) {
	ctx := context.Background()
	env := setupAPI(t)

	id, tok := createAPIToken(t, env, "renew-refresh", "6h")
	grantAIPermissions(t, env, id, 1)
	h := tokenHeaders(tok)
	h["Idempotency-Key"] = "renew-refresh-1"
	status, out := env.serve("POST", "/api/v1/ai/lease-requests", leaseRequestBody(env.nodeID, "rr", "read-only"), h)
	if status != http.StatusCreated {
		t.Fatalf("lease request status %d: %s", status, out)
	}
	lease := mustDecode[struct {
		Lease struct {
			ID string `json:"id"`
		} `json:"lease"`
	}](t, out)

	// Mark the key installed (simulating the node having installed it).
	if _, err := env.st.DB().ExecContext(ctx,
		`UPDATE ai_lease SET key_installed=1 WHERE id=$1`, lease.Lease.ID); err != nil {
		t.Fatal(err)
	}

	// Renew: the lease must now be flagged for re-installation.
	status, out = env.serve("POST", "/api/v1/ai/leases/"+lease.Lease.ID+"/renew",
		map[string]any{"requested_duration_seconds": 3600}, tokenHeaders(tok))
	if status != http.StatusOK {
		t.Fatalf("renew status %d: %s", status, out)
	}
	fromDB, err := env.st.LeaseByID(ctx, lease.Lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fromDB.KeyInstalled {
		t.Fatal("renewal should reset key_installed so the node re-installs with a fresh runtime token")
	}
	// The heartbeat ops must now include an install instruction with a token.
	install, _, err := env.srv.NodeService().LeaseInstallOpsForNode(ctx, env.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range install {
		if op.LeaseID == lease.Lease.ID {
			found = true
			if op.RuntimeToken == "" {
				t.Fatal("reinstall op missing runtime token")
			}
		}
	}
	if !found {
		t.Fatalf("expected a reinstall op for lease %s after renew", lease.Lease.ID)
	}
}
