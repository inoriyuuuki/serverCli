package db

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

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
	if ver != 6 {
		t.Fatalf("expected schema version 6, got %d", ver)
	}
	// Required tables exist.
	expected := []string{
		"admin_user", "admin_session", "node_enrollment", "node", "node_address",
		"node_heartbeat", "node_command", "task", "task_event", "task_output",
		"ai_lease_request", "ai_lease", "ai_lease_event", "ai_ssh_session",
		"audit_event", "system_setting", "cleanup_run",
		"ai_auto_approval", "task_parameter_history",
		"api_access_token", "api_token_usage_log",
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
	if v := d2.SchemaVersion(ctx); v != 6 {
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
	if d.SchemaVersion(context.Background()) != 6 {
		t.Fatal("in-memory migration failed")
	}
}

var _ = os.Getenv
