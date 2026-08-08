package scheduler

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/service"
	"servercli/internal/store"
)

func TestScopedSchedulerSkipsOfflineDetection(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AgentStateDir = dir
	cfg.DatabaseURL = filepath.Join(dir, "sched.db")
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
	auditor := service.NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	nodes, err := service.NewNodeService(st, cfg, log, auditor, settings)
	if err != nil {
		t.Fatal(err)
	}
	leases := service.NewLeaseService(st, cfg, log, auditor, nodes, settings)
	cleanup := service.NewCleanupService(st, cfg, log, auditor, settings)

	// A node that is online but has never heartbeated (as the child control
	// plane's local self node looks like: heartbeats go to the primary only).
	node := &model.Node{
		ID:            model.NewUUID(),
		EnvironmentID: cfg.InstanceName + "-env",
		InstanceName:  "test-child-1",
		Role:          "child",
		Status:        model.NodeStatusOnline,
		Enabled:       true,
	}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}

	// A scoped (child) control plane must NOT mark its self node offline.
	scoped := New(log, nodes, leases, cleanup, settings, true)
	scoped.tick(ctx)
	afterScoped, err := st.NodeByID(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterScoped.Status != model.NodeStatusOnline {
		t.Fatalf("scoped scheduler marked node offline: %s", afterScoped.Status)
	}

	// A primary (non-scoped) control plane still runs offline detection.
	primary := New(log, nodes, leases, cleanup, settings, false)
	primary.tick(ctx)
	afterPrimary, err := st.NodeByID(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPrimary.Status != model.NodeStatusOffline {
		t.Fatalf("expected node offline after primary tick, got %s", afterPrimary.Status)
	}
}
