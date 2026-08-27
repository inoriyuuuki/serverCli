package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"servercli/internal/logger"
	"servercli/internal/model"
)

func TestMigrateSQLite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	log := logger.New(io.Discard, "error")
	ctx := context.Background()

	d, err := Open(ctx, "sqlite", path, log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ver := d.SchemaVersion(ctx)
	if ver != 12 {
		t.Fatalf("expected schema version 12, got %d", ver)
	}
	// Required tables exist.
	expected := []string{
		"admin_user", "admin_session", "node_enrollment", "node", "node_address",
		"node_heartbeat", "node_command", "task", "task_event", "task_output",
		"ai_lease_request", "ai_lease", "ai_lease_event", "ai_ssh_session",
		"audit_event", "system_setting", "cleanup_run",
		"ai_auto_approval", "task_parameter_history",
		"api_access_token", "api_token_usage_log",
		"deployment_feature", "deployment_release", "oss_profile",
		"deployment_config_profile", "deployment_secret_reference",
		"deployment_target", "deployment_target_secret", "deployment_operation",
		"deployment_operation_target", "deployment_step", "deployment_backup",
		"bootstrap_session",
	}
	for _, tbl := range expected {
		var name string
		err := d.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=$1`, tbl).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}
	// Reopen idempotently.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d2, err := Open(ctx, "sqlite", path, log)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()
	if v := d2.SchemaVersion(ctx); v != 12 {
		t.Fatalf("reopen version mismatch: %d", v)
	}
}

func TestMigrateInMemory(t *testing.T) {
	log := logger.New(io.Discard, "error")
	d, err := Open(context.Background(), "sqlite", "file::memory:?cache=shared", log)
	if err != nil {
		t.Fatalf("open in-memory: %v", err)
	}
	defer d.Close()
	if d.SchemaVersion(context.Background()) != 12 {
		t.Fatal("in-memory migration failed")
	}
}

// applyMigrationsBefore applies the embedded SQLite migrations with version <
// stopBefore and records them in schema_migrations, so a later Migrate call
// only applies the remaining migrations. Used to insert pre-migration rows.
func applyMigrationsBefore(t *testing.T, d *sql.DB, stopBefore int) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "migrations/%d_", &version); err != nil {
			continue
		}
		if version >= stopBefore {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := d.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES ($1,$2)`,
			version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigrate0006RewritesCanonicalWildcards (验收 5): migration 0006 rewrites
// exactly the two historical canonical wildcard permission JSON shapes into
// the explicit AI credential grants, and never touches non-canonical manual
// JSON or empty/NULL permission values.
func TestMigrate0006RewritesCanonicalWildcards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer d.Close()

	// Apply migrations 0001..0005 first so the rows exist before 0006 runs.
	applyMigrationsBefore(t, d, 6)

	canonicalWithConstraints := `{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{}}]}`
	canonicalPlain := `{"version":1,"grants":[{"resource":"*","actions":["*"]}]}`
	manualJSON := `{"version":1,"grants":[{"resource":"nodes","actions":["read"]},{"resource":"ai.leases","actions":["renew"]}]}`
	explicitExpansion := `{"version":1,"grants":[{"resource":"nodes","actions":["read"]},{"resource":"ai.lease_requests","actions":["create","read"]},{"resource":"ai.leases","actions":["renew","heartbeat","disconnect"]}]}`

	insert := func(id, name, perms string, nullPerms bool) {
		t.Helper()
		createdAt := time.Now().UTC().Format(time.RFC3339Nano)
		query := `INSERT INTO api_access_token
			(id, environment_id, name, token_hash, token_prefix, created_by, created_at,
			 expires_at, revoked_at, revoked_by, last_used_at, last_used_ip, usage_count, permission_version, permissions_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`
		args := []any{id, "test-env", name, "hash-" + id, "sct_" + id, "admin", createdAt,
			nil, nil, nil, nil, nil, 0, 1, perms}
		if nullPerms {
			args[14] = nil
		}
		if _, err := d.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("w1", "canonical-constraints", canonicalWithConstraints, false)
	insert("w2", "canonical-plain", canonicalPlain, false)
	insert("m1", "manual", manualJSON, false)
	insert("e1", "empty", "", true)

	// Apply the remaining migrations (0006, 0009, 0010) through the standard Migrate path.
	ver, err := Migrate(ctx, "sqlite", d)
	if err != nil {
		t.Fatalf("apply 0006: %v", err)
	}
	if ver != 12 {
		t.Fatalf("expected schema version 12 after applying remaining migrations, got %d", ver)
	}

	rows, err := d.QueryContext(ctx, `SELECT id, permissions_json FROM api_access_token ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	nullCount := 0
	for rows.Next() {
		var id string
		var perms sql.NullString
		if err := rows.Scan(&id, &perms); err != nil {
			t.Fatal(err)
		}
		if !perms.Valid {
			nullCount++
			continue
		}
		got[id] = perms.String
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if got["w1"] != explicitExpansion || got["w2"] != explicitExpansion {
		t.Fatalf("canonical wildcards not rewritten to explicit AI grants: %v", got)
	}
	if got["m1"] != manualJSON {
		t.Fatalf("non-canonical manual JSON must not be rewritten: %q", got["m1"])
	}
	if nullCount != 1 {
		t.Fatalf("empty permissions row must stay NULL, nullCount=%d", nullCount)
	}
}

// TestMigrate0009DeploymentSchema verifies the deployment-management migration:
// deployment_secret_reference stores content_hash + encryption_mode (default
// 'none') and never a plaintext secret body, and the partial unique index
// prevents two active operations for the same feature.
func TestMigrate0009DeploymentSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	log := logger.New(io.Discard, "error")
	d, err := Open(ctx, "sqlite", path, log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// deployment_secret_reference: content_hash/encryption_mode columns exist,
	// default encryption_mode is 'none', and there is no plaintext content column.
	cols := map[string]bool{}
	rows, err := d.QueryContext(ctx, `PRAGMA table_info(deployment_secret_reference)`)
	if err != nil {
		t.Fatalf("describe deployment_secret_reference: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
		if name == "encryption_mode" {
			if s, ok := dflt.(string); !ok || strings.Trim(s, "'\"") != "none" {
				t.Fatalf("encryption_mode default = %v, want 'none'", dflt)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"content_hash", "encryption_mode"} {
		if !cols[col] {
			t.Fatalf("deployment_secret_reference missing column %s", col)
		}
	}
	for _, col := range []string{"content", "secret", "secret_value"} {
		if cols[col] {
			t.Fatalf("deployment_secret_reference must not persist plaintext column %s", col)
		}
	}

	// Inserting a reference without encryption_mode must default to 'none'.
	featureID := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_feature
		(id, feature_key, name, os, arch, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		featureID, "feature-1", "Feature 1", "linux", "amd64", now, now); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_secret_reference
		(id, name, feature_id, object_key, content_hash, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		model.NewUUID(), "secret-1", featureID, "secrets/s1", "sha256:abc", now); err != nil {
		t.Fatalf("insert secret reference: %v", err)
	}
	var encMode, contentHash string
	if err := d.QueryRowContext(ctx, `SELECT encryption_mode, content_hash FROM deployment_secret_reference`).
		Scan(&encMode, &contentHash); err != nil {
		t.Fatalf("read secret reference: %v", err)
	}
	if encMode != "none" || contentHash != "sha256:abc" {
		t.Fatalf("got encryption_mode=%q content_hash=%q, want 'none'/'sha256:abc'", encMode, contentHash)
	}
	// Unknown encryption mode must be rejected by the CHECK constraint.
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_secret_reference
		(id, name, feature_id, object_key, content_hash, encryption_mode, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		model.NewUUID(), "secret-2", featureID, "secrets/s2", "sha256:def", "rot13", now); err == nil {
		t.Fatal("expected CHECK constraint to reject unknown encryption_mode")
	}

	// Partial unique index exists and is actually partial.
	var idxName, idxSQL string
	if err := d.QueryRowContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type='index' AND name='uq_deployment_op_active_feature'`).
		Scan(&idxName, &idxSQL); err != nil {
		t.Fatalf("partial unique index missing: %v", err)
	}
	if !strings.Contains(idxSQL, "WHERE") {
		t.Fatalf("uq_deployment_op_active_feature is not a partial index: %s", idxSQL)
	}
	insertOp := func(id, status string) error {
		_, err := d.ExecContext(ctx, `INSERT INTO deployment_operation
			(id, action, feature_id, status, requested_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, "install", featureID, status, "test", now)
		return err
	}
	if err := insertOp("op-draft", "draft"); err != nil {
		t.Fatalf("insert draft op: %v", err)
	}
	if err := insertOp("op-1", "queued"); err != nil {
		t.Fatalf("insert queued op: %v", err)
	}
	// A second active operation for the same feature must be rejected.
	if err := insertOp("op-2", "queued"); err == nil {
		t.Fatal("expected partial unique index to reject a second active operation for the same feature")
	}
	if err := insertOp("op-3", "running"); err == nil {
		t.Fatal("expected partial unique index to reject a running operation while another is active")
	}
}

// TestMigrate0010NodeSerialIndex verifies migration 0010: the partial unique
// index uq_deployment_optarget_active_node exists, is partial, and rejects a
// second queued/running operation target for the same node while allowing
// inactive targets on the same node.
func TestMigrate0010NodeSerialIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	log := logger.New(io.Discard, "error")
	d, err := Open(ctx, "sqlite", path, log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// The index must exist and be partial (WHERE clause).
	var idxName, idxSQL string
	if err := d.QueryRowContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type='index' AND name='uq_deployment_optarget_active_node'`).
		Scan(&idxName, &idxSQL); err != nil {
		t.Fatalf("partial unique index missing: %v", err)
	}
	if !strings.Contains(idxSQL, "WHERE") || !strings.Contains(idxSQL, "status") {
		t.Fatalf("uq_deployment_optarget_active_node is not a partial status index: %s", idxSQL)
	}

	// Migration 0010 also adds created_at to operation targets and steps for
	// stable creation-order listing.
	for _, table := range []string{"deployment_operation_target", "deployment_step"} {
		cols := map[string]bool{}
		rows, err := d.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			t.Fatalf("describe %s: %v", table, err)
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt any
			var pk int
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			cols[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !cols["created_at"] {
			t.Fatalf("%s missing created_at column from migration 0010", table)
		}
	}

	// Build minimal features + node so operation targets can reference them.
	// Two features are needed: each target owns a distinct (feature, node) pair.
	featureID := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_feature
		(id, feature_key, name, os, arch, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		featureID, "feature-serial", "Serial", "linux", "amd64", now, now); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	featureID2 := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_feature
		(id, feature_key, name, os, arch, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		featureID2, "feature-serial-2", "Serial 2", "linux", "amd64", now, now); err != nil {
		t.Fatalf("insert feature-2: %v", err)
	}
	nodeID := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO node
		(id, environment_id, instance_name, role, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		nodeID, "env-1", "node-serial", "child", now, now); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	opID := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_operation
		(id, action, feature_id, status, requested_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		opID, "install", featureID, "queued", "test", now); err != nil {
		t.Fatalf("insert op: %v", err)
	}
	opID2 := model.NewUUID()
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_operation
		(id, action, feature_id, status, requested_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		opID2, "update", featureID2, "queued", "test", now); err != nil {
		t.Fatalf("insert op-2: %v", err)
	}
	insertTarget := func(id string) error {
		_, err := d.ExecContext(ctx, `INSERT INTO deployment_target
			(id, feature_id, node_id, actual_status, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, featureID, nodeID, "pending", now, now)
		return err
	}
	if err := insertTarget("target-1"); err != nil {
		t.Fatalf("insert target-1: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_target
		(id, feature_id, node_id, actual_status, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		"target-2", featureID2, nodeID, "pending", now, now); err != nil {
		t.Fatalf("insert target-2: %v", err)
	}
	insertOpTarget := func(id, targetID, status string) error {
		_, err := d.ExecContext(ctx, `INSERT INTO deployment_operation_target
			(id, operation_id, target_id, node_id, status) VALUES ($1,$2,$3,$4,$5)`,
			id, opID, targetID, nodeID, status)
		return err
	}
	// First active (queued) target for the node is fine.
	if err := insertOpTarget("opt-1", "target-1", "queued"); err != nil {
		t.Fatalf("insert first queued op target: %v", err)
	}
	// A second queued target on the same node must be rejected.
	if err := insertOpTarget("opt-2", "target-2", "queued"); err == nil {
		t.Fatal("expected partial unique index to reject a second queued target on the same node")
	}
	// A running target on the same node must also be rejected while one is active.
	if err := insertOpTarget("opt-3", "target-2", "running"); err == nil {
		t.Fatal("expected partial unique index to reject a running target on the same node")
	}
	// Inactive (pending) targets on the same node are allowed.
	if err := insertOpTarget("opt-4", "target-2", "pending"); err != nil {
		t.Fatalf("pending target on same node must be allowed: %v", err)
	}
	// Releasing the active target frees the node for the next deployment.
	if _, err := d.ExecContext(ctx, `UPDATE deployment_operation_target SET status='succeeded' WHERE id=$1`, "opt-1"); err != nil {
		t.Fatalf("finish opt-1: %v", err)
	}
	// (opID, target-2) is already taken by the pending opt-4, so use opID2 to
	// verify the node is free again after opt-1 finished.
	if _, err := d.ExecContext(ctx, `INSERT INTO deployment_operation_target
		(id, operation_id, target_id, node_id, status) VALUES ($1,$2,$3,$4,$5)`,
		"opt-5", opID2, "target-2", nodeID, "queued"); err != nil {
		t.Fatalf("queued target after previous finished must be allowed: %v", err)
	}
}

var _ = os.Getenv

// TestMigrate0011RestoreColumns verifies migration 0011 adds backup_id and
// force_delete to deployment_operation (restore support).
func TestMigrate0011RestoreColumns(t *testing.T) {
	dir := t.TempDir()
	log := logger.New(io.Discard, "error")
	ctx := context.Background()
	d, err := Open(ctx, "sqlite", filepath.Join(dir, "t.db"), log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if v := d.SchemaVersion(ctx); v != 12 {
		t.Fatalf("schema version = %d, want 12", v)
	}
	cols := map[string]bool{}
	rows, err := d.QueryContext(ctx, `PRAGMA table_info(deployment_operation)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	rows.Close()
	if !cols["backup_id"] {
		t.Fatal("deployment_operation missing backup_id column")
	}
	if !cols["force_delete"] {
		t.Fatal("deployment_operation missing force_delete column")
	}
}

// TestMigrate0012ReleaseRestoreHook verifies migration 0012 adds restore_hook
// to deployment_release.
func TestMigrate0012ReleaseRestoreHook(t *testing.T) {
	dir := t.TempDir()
	log := logger.New(io.Discard, "error")
	ctx := context.Background()
	d, err := Open(ctx, "sqlite", filepath.Join(dir, "t.db"), log)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	cols := map[string]bool{}
	rows, err := d.QueryContext(ctx, `PRAGMA table_info(deployment_release)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	rows.Close()
	if !cols["restore_hook"] {
		t.Fatal("deployment_release missing restore_hook column")
	}
}
