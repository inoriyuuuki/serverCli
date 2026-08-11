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
	"testing"
	"time"

	"servercli/internal/logger"
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
	if ver != 8 {
		t.Fatalf("expected schema version 8, got %d", ver)
	}
	// Required tables exist.
	expected := []string{
		"admin_user", "admin_session", "node_enrollment", "node", "node_address",
		"node_heartbeat", "node_command", "task", "task_event", "task_output",
		"service_ownership",
		"ai_lease_request", "ai_lease", "ai_lease_event", "ai_ssh_session",
		"audit_event", "system_setting", "cleanup_run",
		"ai_auto_approval", "task_parameter_history",
		"api_access_token", "api_token_usage_log",
		"cluster", "node_profile", "declarative_node", "service_reference",
		"desired_state_revision", "applied_state_revision", "operation_v2",
		"operation_step", "backup_set", "oss_sync_revision",
		"release_cache_entry", "primary_transfer",
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
	if v := d2.SchemaVersion(ctx); v != 8 {
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
	if d.SchemaVersion(context.Background()) != 8 {
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

	// Apply the remaining migration (0006) through the standard Migrate path.
	ver, err := Migrate(ctx, "sqlite", d)
	if err != nil {
		t.Fatalf("apply 0006: %v", err)
	}
	if ver != 8 {
		t.Fatalf("expected schema version 8 after applying 0007+0008, got %d", ver)
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

var _ = os.Getenv
