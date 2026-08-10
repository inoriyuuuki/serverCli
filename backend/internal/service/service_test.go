package service

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/store"
)

func tsRFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AgentStateDir = dir
	cfg.DatabaseURL = filepath.Join(dir, "test.db")
	cfg.LogLevel = "error"
	cfg.InstanceName = "test-primary"
	cfg.NodeRole = "primary"
	return cfg
}

func testServices(t *testing.T) (context.Context, *store.Store, *config.Config, *Auditor, *NodeService, *TaskService, *LeaseService, *SettingsService, *CleanupService) {
	t.Helper()
	cfg := testCfg(t)
	ctx := context.Background()
	log := logger.New(os.Stderr, "error")
	database, err := db.Open(ctx, "sqlite", cfg.DatabaseURL, log)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	st := store.New(database, log)
	settings := NewSettingsService(st, cfg)
	if err := settings.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	auditor := NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	nodes, err := NewNodeService(st, cfg, log, auditor, settings)
	if err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskService(st, cfg, log, auditor, nodes)
	leases := NewLeaseService(st, cfg, log, auditor, nodes, settings)
	cleanup := NewCleanupService(st, cfg, log, auditor, settings)
	return ctx, st, cfg, auditor, nodes, tasks, leases, settings, cleanup
}

func mustEnrollAndClaim(t *testing.T, ctx context.Context, nodes *NodeService) (string, string, string) {
	return mustEnrollAndClaimNamed(t, ctx, nodes, "1")
}

func mustEnrollAndClaimNamed(t *testing.T, ctx context.Context, nodes *NodeService, name string) (string, string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	e, err := nodes.CreateEnrollment(ctx, EnrollmentInput{
		InstanceRequestID: "inst-" + name,
		Hostname:          "node-" + name,
		RequestedRole:     "child",
		AgentVersion:      "test",
		ReportedAddresses: []AddressInput{{Address: "10.0.0.5", AddressType: "reported", ServicePort: 9047}},
		InstancePublicKey: pubB64,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := nodes.ApproveEnrollment(ctx, e.ID, "admin-1", "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	_, claimToken, err := nodes.Enrollment(ctx, e.ID)
	if err != nil || claimToken == "" {
		t.Fatalf("claim token missing: err=%v", err)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := ed25519.Sign(priv, []byte(ts+"|"+e.ID))
	res, err := nodes.ClaimEnrollment(ctx, e.ID, claimToken, base64.StdEncoding.EncodeToString(sig), ts, pubB64)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if res.NodeID == "" || res.NodeCredential == "" {
		t.Fatal("claim incomplete")
	}
	// Second claim must fail (one-time).
	ts2 := fmt.Sprintf("%d", time.Now().Unix())
	sig2 := ed25519.Sign(priv, []byte(ts2+"|"+e.ID))
	if _, err := nodes.ClaimEnrollment(ctx, e.ID, claimToken, base64.StdEncoding.EncodeToString(sig2), ts2, pubB64); err == nil {
		t.Fatal("second claim should have failed")
	}
	return res.NodeID, res.NodeCredential, e.ID
}

// mustCreatePrincipal creates an access token row and returns its principal.
func mustCreatePrincipal(t *testing.T, st *store.Store, envID, name string, ttl time.Duration) *TokenPrincipal {
	t.Helper()
	plaintext, err := GenerateAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().UTC().Add(ttl)
	tok := &model.APIAccessToken{
		ID:                model.NewUUID(),
		EnvironmentID:     envID,
		Name:              name,
		TokenHash:         security.HashToken(plaintext),
		TokenPrefix:       security.Prefix(plaintext, 12),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         &exp,
		PermissionVersion: 1,
		PermissionsJSON:   defaultPermissionsJSON,
	}
	if err := st.CreateAccessToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	perms, err := parsePermissions(defaultPermissionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	return &TokenPrincipal{TokenID: tok.ID, Name: tok.Name, TokenPrefix: tok.TokenPrefix, ExpiresAt: &exp, Permissions: perms}
}

func TestRegistrationClaimHeartbeatFlow(t *testing.T) {
	ctx, st, _, _, nodes, _, _, _, _ := testServices(t)
	nodeID, credential, _ := mustEnrollAndClaim(t, ctx, nodes)

	// Heartbeat brings the node online.
	hb := HeartbeatInput{
		Hostname:         "node-a",
		AgentVersion:     "test",
		OSName:           "linux",
		Addresses:        []AddressInput{{Address: "10.0.0.5", AddressType: "reported", ServicePort: 9047}},
		CPUUsagePercent:  12.5,
		MemoryTotalBytes: 1024 * 1024 * 1024,
		MemoryUsedBytes:  512 * 1024 * 1024,
	}
	resp, err := nodes.Heartbeat(ctx, nodeID, hb, "10.0.0.5")
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if resp.Status != model.NodeStatusOnline {
		t.Fatalf("expected online, got %s", resp.Status)
	}
	node, err := st.NodeByID(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.CredentialHash == "" || node.CredentialPrefix == "" {
		t.Fatal("credential hash/prefix not stored")
	}
	if !strings.HasPrefix(credential, "ncred_") {
		t.Fatalf("unexpected credential format %q", credential)
	}
	// Heartbeat with a wrong node id must fail.
	if _, err := nodes.Heartbeat(ctx, nodeID+"x", hb, "10.0.0.5"); err == nil {
		t.Fatal("heartbeat for unknown node should fail")
	}
	// Node addresses recorded.
	addrs, err := st.NodeAddresses(ctx, nodeID)
	if err != nil || len(addrs) == 0 {
		t.Fatalf("addresses not recorded: %v", err)
	}
}

func TestTaskLifecycleAndIdempotency(t *testing.T) {
	ctx, _, _, _, nodes, tasks, _, _, _ := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	// Register a command.
	if _, err := nodes.CommandsSnapshot(ctx, nodeID, []CommandsSnapshotInput{{
		CommandID:           "system.echo",
		CommandVersion:      "1.0.0",
		Category:            "system",
		Title:               "echo",
		ParameterSchemaJSON: `{"type":"object","properties":{"message":{"type":"string"}}}`,
		PermissionProfile:   "read-only",
		TimeoutSeconds:      30,
		MaxOutputBytes:      1024,
		Enabled:             true,
	}}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Create task with idempotency key.
	in := CreateTaskInput{CommandID: "system.echo", CommandVersion: "1.0.0", Arguments: map[string]any{"message": "hi"}}
	t1, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-1", in)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if t1.Status != model.TaskQueued {
		t.Fatalf("expected queued, got %s", t1.Status)
	}
	// Same key returns the same task (no duplicate).
	t2, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-1", in)
	if err != nil || t2.ID != t1.ID {
		t.Fatalf("idempotency failed: %v (ids %s != %s)", err, t2.ID, t1.ID)
	}
	// Different key creates a new task.
	t3, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-2", in)
	if err != nil || t3.ID == t1.ID {
		t.Fatal("different idempotency key should create a new task")
	}

	// Poll the first task.
	payload, err := tasks.PollTask(ctx, nodeID)
	if err != nil || payload == nil {
		t.Fatalf("poll: %v", err)
	}
	if payload.TaskID != t1.ID {
		t.Fatalf("expected task %s, got %s", t1.ID, payload.TaskID)
	}
	// Signature verifies against the derived credential.
	cred, err := nodes.CredentialForNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	clone := *payload
	clone.PayloadHash, clone.Signature = "", ""
	raw, _ := json.Marshal(&clone)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != payload.PayloadHash {
		t.Fatal("payload hash mismatch")
	}
	mac := hmac.New(sha256.New, []byte(cred))
	mac.Write([]byte("task:" + payload.TaskID + ":" + payload.PayloadHash))
	if hex.EncodeToString(mac.Sum(nil)) != payload.Signature {
		t.Fatal("signature mismatch")
	}

	// Events and result.
	if err := tasks.RecordEvent(ctx, nodeID, t1.ID, EventInput{EventType: "accepted", Message: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.RecordEvent(ctx, nodeID, t1.ID, EventInput{EventType: "started", Message: "go"}); err != nil {
		t.Fatal(err)
	}
	ec := 0
	_, err = tasks.RecordResult(ctx, nodeID, t1.ID, ResultInput{Status: "succeeded", StdoutText: "hi", ExitCode: &ec})
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	task, _, output, err := tasks.GetTask(ctx, "", t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskSucceeded {
		t.Fatalf("expected succeeded, got %s", task.Status)
	}
	if output == nil || output.StdoutText != "hi" {
		t.Fatalf("output missing: %+v", output)
	}
	// Terminal state is irreversible: a second result does not change status.
	ec2 := 1
	if _, err := tasks.RecordResult(ctx, nodeID, t1.ID, ResultInput{Status: "failed", ExitCode: &ec2}); err != nil {
		t.Fatal(err)
	}
	task, _, _, _ = tasks.GetTask(ctx, "", t1.ID)
	if task.Status != model.TaskSucceeded {
		t.Fatalf("terminal state was mutated: %s", task.Status)
	}

	// Cancel a queued task.
	t4, err := tasks.CreateTask(ctx, nodeID, "admin-1", model.ActorAdmin, "idem-3", in)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := tasks.CancelTask(ctx, "", t4.ID, model.ActorAdmin, "admin-1")
	if err != nil || cancelled.Status != model.TaskCancelled {
		t.Fatalf("cancel: %v %v", err, cancelled)
	}
	// Poll must not deliver the cancelled task.
	if payload, err := tasks.PollTask(ctx, nodeID); err != nil || (payload != nil && payload.TaskID == t4.ID) {
		t.Fatalf("cancelled task should not be dispatched: %v", err)
	}
}

func TestLeaseTimeRules(t *testing.T) {
	ctx, st, _, _, nodes, _, leases, _, _ := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	principal := mustCreatePrincipal(t, st, nodes.EnvID(), "ai-test", 2*time.Hour)
	res, err := leases.CreateLeaseRequest(ctx, LeaseRequestInput{
		NodeSelector:          nodeID,
		PublicKey:             "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 test",
		PermissionProfile:     "read-only",
		RequestedDurationSecs: 3600,
		Purpose:               "test",
		ClientRequestID:       "lr-1",
		AIAgentID:             "ai-1",
	}, "9.9.9.9", principal)
	if err != nil {
		t.Fatalf("lease request: %v", err)
	}
	if res.Lease == nil {
		t.Fatalf("expected auto-approved lease, got %s", res.LeaseRequest.Status)
	}
	lease := res.Lease
	if lease.Status != model.LeaseActive {
		t.Fatalf("expected active lease, got %s", lease.Status)
	}
	if lease.AccessTokenID != principal.TokenID {
		t.Fatalf("lease not bound to token: %s", lease.AccessTokenID)
	}
	// Default duration = 1h.
	if lease.ExpiresAt.Sub(lease.IssuedAt) > 61*time.Minute {
		t.Fatalf("expires_at too far: %v", lease.ExpiresAt.Sub(lease.IssuedAt))
	}
	// Absolute cap = issued_at + 24h.
	if lease.AbsoluteExpiresAt.Sub(lease.IssuedAt) < 23*time.Hour || lease.AbsoluteExpiresAt.Sub(lease.IssuedAt) > 25*time.Hour {
		t.Fatalf("absolute expiry wrong: %v", lease.AbsoluteExpiresAt.Sub(lease.IssuedAt))
	}
	if lease.ExpiresAt.After(lease.AbsoluteExpiresAt) {
		t.Fatal("expires_at exceeds absolute cap")
	}
	// Renew extends without resetting issued_at / absolute.
	origIssued := lease.IssuedAt
	origAbs := lease.AbsoluteExpiresAt
	renewed, err := leases.Renew(ctx, lease.ID, principal, 3600)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.IssuedAt.Equal(origIssued) {
		t.Fatal("renew changed issued_at")
	}
	if !renewed.AbsoluteExpiresAt.Equal(origAbs) {
		t.Fatal("renew changed absolute_expires_at")
	}
	if renewed.RenewCount != 1 {
		t.Fatalf("expected renew_count 1, got %d", renewed.RenewCount)
	}
	// A different principal is not the owner: 404.
	other := mustCreatePrincipal(t, st, nodes.EnvID(), "other", time.Hour)
	if _, err := leases.Renew(ctx, lease.ID, other, 3600); err == nil {
		t.Fatal("renew with non-owner token should fail")
	}
	// Revoke makes lease terminal and disables further renew.
	revoked, err := leases.Revoke(ctx, lease.ID, "admin-1", "test revoke", false)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked.Status != model.LeaseRevoked {
		t.Fatalf("expected revoked, got %s", revoked.Status)
	}
	if _, err := leases.Renew(ctx, lease.ID, principal, 3600); err == nil {
		t.Fatal("renew after revoke should fail")
	}
	if _, err := leases.Heartbeat(ctx, lease.ID, principal); err == nil {
		t.Fatal("heartbeat after revoke should fail")
	}
	// Lease in DB retains only a renewal token hash + prefix (schema compat).
	fromDB, err := st.LeaseByID(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fromDB.RenewalTokenHash == "" {
		t.Fatal("renewal token hash must be stored for schema compatibility")
	}
	if fromDB.RenewalTokenPrefix == "" {
		t.Fatal("renewal token prefix missing")
	}
}

func TestLeaseAbsoluteCapBlocksRenew(t *testing.T) {
	ctx, st, _, _, nodes, _, leases, _, _ := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	principal := mustCreatePrincipal(t, st, nodes.EnvID(), "cap", 24*time.Hour)
	res, err := leases.CreateLeaseRequest(ctx, LeaseRequestInput{
		NodeSelector: nodeID, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 test",
		PermissionProfile: "read-only", RequestedDurationSecs: 3600, ClientRequestID: "cap-1", AIAgentID: "ai-1",
		Purpose: "test",
	}, "1.1.1.1", principal)
	if err != nil || res.Lease == nil {
		t.Fatalf("lease request: %v", err)
	}
	// Force the lease close to its absolute cap, then renew: new expiry must
	// clamp to absolute_expires_at, and once at cap renewal must fail.
	lease := res.Lease
	now := time.Now().UTC()
	if _, err := leases.store.DB().ExecContext(ctx,
		`UPDATE ai_lease SET absolute_expires_at=$1, expires_at=$2, last_heartbeat_at=$3 WHERE id=$4`,
		tsRFC3339(now.Add(10*time.Minute)), tsRFC3339(now.Add(2*time.Minute)), tsRFC3339(now), lease.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.store.DB().ExecContext(ctx,
		`UPDATE ai_lease SET expires_at=$1 WHERE id=$2`,
		tsRFC3339(now.Add(2*time.Minute)), lease.ID); err != nil {
		t.Fatal(err)
	}
	lease, err = leases.store.LeaseByID(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := leases.Renew(ctx, lease.ID, principal, 3600)
	if err != nil {
		t.Fatalf("renew near cap: %v", err)
	}
	if !renewed.ExpiresAt.Equal(lease.AbsoluteExpiresAt) {
		t.Fatalf("expected expiry clamped to absolute cap: %v vs %v", renewed.ExpiresAt, lease.AbsoluteExpiresAt)
	}
	// Push past the absolute cap: renew must be denied.
	lease.ExpiresAt = now.Add(-time.Minute)
	if err := leases.store.UpdateLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Renew(ctx, lease.ID, principal, 3600); err == nil {
		t.Fatal("renew past absolute cap should fail")
	}
}

func TestRenewalsDisabled(t *testing.T) {
	ctx, st, _, _, nodes, _, leases, settings, _ := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	principal := mustCreatePrincipal(t, st, nodes.EnvID(), "renew", 24*time.Hour)
	res, err := leases.CreateLeaseRequest(ctx, LeaseRequestInput{
		NodeSelector: nodeID, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 test",
		PermissionProfile: "read-only", ClientRequestID: "nr-1", AIAgentID: "ai-1", Purpose: "test",
	}, "1.1.1.1", principal)
	if err != nil || res.Lease == nil {
		t.Fatalf("lease request: %v", err)
	}
	f := false
	if err := leases.SetAIAccess(ctx, nil, &f, "", "admin-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Renew(ctx, res.Lease.ID, principal, 3600); err == nil {
		t.Fatal("renew should be denied when renewals disabled")
	}
	// New requests still allowed? Test gate.
	if _, err := settings.Patch(ctx, map[string]any{KeyNewRequestsEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.CreateLeaseRequest(ctx, LeaseRequestInput{
		NodeSelector: nodeID, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 test",
		PermissionProfile: "read-only", ClientRequestID: "nr-2", AIAgentID: "ai-1", Purpose: "test",
	}, "1.1.1.1", principal); err == nil {
		t.Fatal("new requests should be denied when gate off")
	}
}

func TestCleanupBoundaries(t *testing.T) {
	ctx, st, _, _, nodes, _, _, _, cleanup := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	// Insert old rows via raw SQL (store methods force now()).
	oldTS := tsRFC3339(time.Now().UTC().Add(-30 * 24 * time.Hour))
	d := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("raw insert: %v", err)
		}
	}
	exec(`INSERT INTO node_heartbeat (id, node_id, recorded_at, cpu_usage_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms, is_protected) VALUES ($1,$2,$3,0,0,0,0,0,0,0,0,0,0,0)`,
		model.NewUUID(), nodeID, oldTS)
	exec(`INSERT INTO node_heartbeat (id, node_id, recorded_at, cpu_usage_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms, is_protected) VALUES ($1,$2,$3,0,0,0,0,0,0,0,0,0,0,1)`,
		model.NewUUID(), nodeID, oldTS)
	exec(`INSERT INTO task (id, node_id, command_id, command_version, requested_by, idempotency_key, arguments_json, status, queued_at, timeout_seconds, is_protected) VALUES ($1,$2,'c','1','a','k','{}','succeeded',$3,10,0)`,
		"task-old", nodeID, oldTS)
	exec(`INSERT INTO task_output (task_id, stdout_text, stderr_text, stdout_bytes, stderr_bytes, truncated, redaction_count, encoding, created_at, is_protected) VALUES ($1,'x','',1,0,0,0,'utf-8',$2,0)`,
		"task-old", oldTS)
	exec(`INSERT INTO audit_event (id, occurred_at, actor_type, action, result, risk_level, is_protected, protected_by) VALUES ($1,$2,'system','test','success','low',1,'manual')`,
		model.NewUUID(), oldTS)
	exec(`INSERT INTO audit_event (id, occurred_at, actor_type, action, result, risk_level, is_protected) VALUES ($1,$2,'system','test2','success','low',0)`,
		model.NewUUID(), oldTS)

	run, err := cleanup.Run(ctx, CleanupOptions{DryRun: false, RequestedBy: "test", Trigger: "manual"})
	if err != nil {
		t.Fatalf("cleanup run: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("cleanup failed: %s %s", run.Status, run.ErrorMessage)
	}
	if run.DeletedCount == 0 {
		t.Fatal("expected some deletions")
	}
	// Protected heartbeat must remain.
	hbs, err := st.RecentHeartbeats(ctx, nodeID, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundProtected := false
	for _, h := range hbs {
		if h.IsProtected {
			foundProtected = true
		}
	}
	if !foundProtected {
		t.Fatal("protected heartbeat was deleted")
	}
	if len(hbs) != 2 {
		t.Fatalf("expected recent + protected heartbeat to remain, got %d", len(hbs))
	}
	// Protected audit must remain; unprotected old audit deleted.
	events, err := st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	foundAuditProtected := false
	for _, e := range events {
		if e.Action == "test" && e.IsProtected {
			foundAuditProtected = true
		}
		if e.Action == "test2" {
			t.Fatal("unprotected old audit was not deleted")
		}
	}
	if !foundAuditProtected {
		t.Fatal("protected audit event was deleted")
	}

	// Dry run must not delete anything. Insert a fresh old unprotected row so
	// the dry run has candidates to report.
	if _, err := d.ExecContext(ctx, `INSERT INTO node_heartbeat (id, node_id, recorded_at, cpu_usage_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms, is_protected) VALUES ($1,$2,$3,0,0,0,0,0,0,0,0,0,0,0)`,
		model.NewUUID(), nodeID, oldTS); err != nil {
		t.Fatal(err)
	}
	before, _ := st.RecentHeartbeats(ctx, nodeID, 100)
	dr, err := cleanup.Run(ctx, CleanupOptions{DryRun: true, DataTypes: []string{"heartbeats"}, RequestedBy: "test", Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := st.RecentHeartbeats(ctx, nodeID, 100)
	if len(after) != len(before) {
		t.Fatal("dry run deleted rows")
	}
	if dr.CandidateCount == 0 {
		t.Fatal("dry run should report candidates")
	}
}

func TestAuthSessionCSRF(t *testing.T) {
	ctx, st, _, auditor, _, _, _, _, _ := testServices(t)
	master, err := MasterKey(testCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthService(st, slog.New(slog.NewTextHandler(os.Stderr, nil)), auditor, master)
	// Create admin.
	hash, _ := security.HashPassword("pass1234")
	admin := &model.AdminUser{ID: model.NewUUID(), Username: "admin", PasswordHash: hash}
	if err := st.CreateAdmin(ctx, admin); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, "admin", "pass1234", "1.2.3.4", "test")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.SessionToken == "" || res.CSRF == "" {
		t.Fatal("session token/csrf missing")
	}
	sess, got, err := auth.Authenticate(ctx, res.SessionToken)
	if err != nil || got.Username != "admin" {
		t.Fatalf("authenticate: %v", err)
	}
	// CSRF round trip from /auth/session equivalent.
	if !auth.ValidateCSRF(sess, auth.CSRFFor(sess)) {
		t.Fatal("CSRF should validate")
	}
	if auth.ValidateCSRF(sess, "wrong-csrf") {
		t.Fatal("wrong CSRF should fail")
	}
	// Wrong password.
	if _, err := auth.Login(ctx, "admin", "wrong", "1.2.3.4", "test"); err == nil {
		t.Fatal("bad password should fail")
	}
	// Password change.
	if err := auth.ChangePassword(ctx, got, "pass1234", "newpass99", "1.2.3.4"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, err := auth.Login(ctx, "admin", "newpass99", "1.2.3.4", "test"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
	if _, err := auth.Login(ctx, "admin", "pass1234", "1.2.3.4", "test"); err == nil {
		t.Fatal("old password should no longer work")
	}
}

func TestLoginRateLimitLockout(t *testing.T) {
	ctx, st, _, auditor, _, _, _, _, _ := testServices(t)
	master, _ := MasterKey(testCfg(t))
	auth := NewAuthService(st, slog.New(slog.NewTextHandler(os.Stderr, nil)), auditor, master)
	hash, _ := security.HashPassword("pass")
	admin := &model.AdminUser{ID: model.NewUUID(), Username: "admin", PasswordHash: hash}
	_ = st.CreateAdmin(ctx, admin)
	for i := 0; i < 5; i++ {
		_, _ = auth.Login(ctx, "admin", "bad", "5.6.7.8", "test")
	}
	if _, err := auth.Login(ctx, "admin", "pass", "5.6.7.8", "test"); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected lockout, got %v", err)
	}
	// Different IP is unaffected.
	if _, err := auth.Login(ctx, "admin", "pass", "9.9.9.9", "test"); err != nil {
		t.Fatalf("different IP should not be locked: %v", err)
	}
}

func TestLeaseDisableRenewalProtectRevokeAll(t *testing.T) {
	ctx, st, _, _, nodes, _, leases, _, _ := testServices(t)
	nodeID, _, _ := mustEnrollAndClaim(t, ctx, nodes)
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	principal := mustCreatePrincipal(t, st, nodes.EnvID(), "prot", 24*time.Hour)
	newLease := func(id string) *model.AILease {
		res, err := leases.CreateLeaseRequest(ctx, LeaseRequestInput{
			NodeSelector: nodeID, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGenericTestKeyValue123456789 " + id,
			PermissionProfile: "read-only", RequestedDurationSecs: 3600, Purpose: "test", ClientRequestID: id, AIAgentID: "ai-1",
		}, "9.9.9.9", principal)
		if err != nil || res.Lease == nil {
			t.Fatalf("lease request %s: %v", id, err)
		}
		return res.Lease
	}

	l1 := newLease("lr-a")
	l2 := newLease("lr-b")

	// Disable renewal on l1.
	after, err := leases.DisableRenewal(ctx, l1.ID, "admin-1", "no more")
	if err != nil {
		t.Fatalf("disable renewal: %v", err)
	}
	if !after.RenewalDisabled {
		t.Fatal("renewal_disabled should be true")
	}
	if _, err := leases.Renew(ctx, l1.ID, principal, 3600); err == nil {
		t.Fatal("renew after disable should fail")
	}
	// Unknown lease -> not found.
	if _, err := leases.DisableRenewal(ctx, "missing", "admin-1", "x"); err == nil {
		t.Fatal("disable renewal on missing lease should fail")
	}

	// Protect l2.
	p, err := leases.ProtectLease(ctx, l2.ID, "admin-1")
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	if !p.IsProtected || p.ProtectedAt == nil {
		t.Fatal("lease should be protected")
	}

	// Revoke-all (global) revokes every still-active lease (both l1 and l2;
	// disabling renewal does not make a lease terminal).
	count, revokedLeases, err := leases.RevokeAll(ctx, "", "admin-1", "emergency", true)
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if len(revokedLeases) != count {
		t.Fatalf("revoke all returned %d leases but count=%d", len(revokedLeases), count)
	}
	if count != 2 {
		t.Fatalf("expected 2 revoked, got %d", count)
	}
	for _, id := range []string{l1.ID, l2.ID} {
		got, err := st.LeaseByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.LeaseRevoked {
			t.Fatalf("lease %s expected revoked, got %s", id, got.Status)
		}
	}
	// Node-scoped revoke-all with unknown node errors.
	if _, _, err := leases.RevokeAll(ctx, "missing-node", "admin-1", "x", true); err == nil {
		t.Fatal("revoke all for unknown node should fail")
	}
}

func TestHeartbeatRecordsPublicSourceIPAsNodeAddress(t *testing.T) {
	ctx, st, _, _, nodes, _, _, _, _ := testServices(t)

	// 节点未上报任何地址时，控制面把心跳连接的公网来源 IP 自动记为可达地址。
	nodeID, _, _ := mustEnrollAndClaimNamed(t, ctx, nodes, "pub")
	if _, err := nodes.Heartbeat(ctx, nodeID, HeartbeatInput{Hostname: "n"}, "43.142.9.9"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	addrs, err := st.NodeAddresses(ctx, nodeID)
	if err != nil || len(addrs) != 1 {
		t.Fatalf("expected 1 auto-recorded address, got %d (err=%v)", len(addrs), err)
	}
	if addrs[0].Address != "43.142.9.9" || addrs[0].AddressType != "source" || !addrs[0].IsPreferred {
		t.Fatalf("unexpected auto-recorded address: %+v", addrs[0])
	}

	// 私网/回环来源 IP 不应当被记录为 SSH 目标。
	for _, src := range []string{"10.0.0.9", "192.168.1.5", "127.0.0.1"} {
		id, _, _ := mustEnrollAndClaimNamed(t, ctx, nodes, "priv-"+strings.ReplaceAll(src, ".", "-"))
		if _, err := nodes.Heartbeat(ctx, id, HeartbeatInput{Hostname: "n"}, src); err != nil {
			t.Fatalf("heartbeat(%s): %v", src, err)
		}
		got, err := st.NodeAddresses(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("source IP %s should not be recorded, got %d addresses", src, len(got))
		}
	}

	// 节点已上报地址且来源 IP 为公网时：公网来源 IP 作为首选 SSH 目标，
	// 上报地址保留在其后（自动穿透优先）。
	id2, _, _ := mustEnrollAndClaimNamed(t, ctx, nodes, "reported")
	if _, err := nodes.Heartbeat(ctx, id2, HeartbeatInput{
		Hostname:  "n2",
		Addresses: []AddressInput{{Address: "10.1.2.3", AddressType: "reported", ServicePort: 9047}},
	}, "43.142.9.10"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got2, err := st.NodeAddresses(ctx, id2)
	if err != nil || len(got2) != 2 {
		t.Fatalf("expected public source + reported address, got %d (err=%v)", len(got2), err)
	}
	if got2[0].Address != "43.142.9.10" || got2[0].AddressType != "source" || !got2[0].IsPreferred {
		t.Fatalf("public source IP should be preferred SSH target: %+v", got2[0])
	}
	if got2[1].Address != "10.1.2.3" || got2[1].AddressType != "reported" {
		t.Fatalf("reported address should be preserved after source: %+v", got2[1])
	}

	// 上报地址全为私网时，公网来源 IP 必须优先（真实云服务器场景）。
	id3, _, _ := mustEnrollAndClaimNamed(t, ctx, nodes, "private-reported")
	if _, err := nodes.Heartbeat(ctx, id3, HeartbeatInput{
		Hostname:  "n3",
		Addresses: []AddressInput{{Address: "172.17.0.1", AddressType: "reported", ServicePort: 9043}},
	}, "218.244.159.177"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got3, err := st.NodeAddresses(ctx, id3)
	if err != nil || len(got3) != 2 {
		t.Fatalf("expected public source + private reported, got %d (err=%v)", len(got3), err)
	}
	if got3[0].Address != "218.244.159.177" || !got3[0].IsPreferred {
		t.Fatalf("public source IP should be preferred over private reported: %+v", got3[0])
	}
}
