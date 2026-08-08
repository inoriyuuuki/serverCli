// Command servercli-control-plane runs the ServerCLI REST API, static
// frontend hosting, scheduler and cleaner.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"servercli/internal/api"
	"servercli/internal/config"
	"servercli/internal/db"
	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/scheduler"
	"servercli/internal/secret"
	"servercli/internal/security"
	"servercli/internal/service"
	"servercli/internal/store"
)

var (
	version = "0.1.0"
	build   = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "control-plane: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	migrateOnly := false
	bootstrapAdminMode := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--migrate-only":
			migrateOnly = true
		case "--bootstrap-admin":
			bootstrapAdminMode = true
		}
	}

	log := setupLogger(cfg)
	log.Info("control-plane starting", "instance", cfg.InstanceName, "role", cfg.NodeRole,
		"env", cfg.AppEnv, "backend_addr", cfg.BackendAddr, "frontend_addr", cfg.FrontendAddr,
		"database_driver", cfg.DatabaseDriver)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.AgentStateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	database, err := db.Open(ctx, cfg.DatabaseDriver, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer database.Close()
	log.Info("database ready", "schema_version", database.SchemaVersion(ctx))

	st := store.New(database, log)
	settings := service.NewSettingsService(st, cfg)
	if err := settings.Seed(ctx); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}

	// Subcommands used by scripts/migrate.sh and scripts/bootstrap-admin.sh.
	if migrateOnly {
		log.Info("migration complete", "schema_version", database.SchemaVersion(ctx))
		return nil
	}
	if bootstrapAdminMode {
		if err := bootstrapAdmin(ctx, cfg, st, log, true); err != nil {
			return fmt.Errorf("admin bootstrap: %w", err)
		}
		log.Info("admin bootstrap/verify complete")
		return nil
	}

	// Bootstrap admin (non-strict during normal startup).
	if err := bootstrapAdmin(ctx, cfg, st, log, false); err != nil {
		return fmt.Errorf("admin bootstrap: %w", err)
	}

	// Child scope: when running as a child control plane, bind API scope to
	// the node identity claimed by the local agent.
	childNodeID := ""
	if cfg.NodeRole == "child" {
		childNodeID = ensureChildNode(ctx, cfg, st, log)
	}

	redactor := secret.NewRedactor()
	srv, err := api.New(api.Options{
		Config:      cfg,
		Log:         log,
		Store:       st,
		Version:     version,
		Build:       build,
		Commit:      commit,
		Redactor:    redactor,
		ChildNodeID: childNodeID,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	// Child control planes re-read the local agent identity so the restricted
	// API scope appears once the agent claims a node_id (no restart needed).
	if cfg.NodeRole == "child" {
		go refreshChildScope(ctx, srv, cfg, st, log)
	}

	// Scheduler. Child (scoped) control planes are not the authority on node
	// liveness (heartbeats go to the primary), so offline detection is skipped.
	sched := scheduler.New(log, srv.NodeService(), srv.LeaseService(), srv.CleanupService(), srv.SettingsService(), cfg.NodeRole == "child")
	go sched.Run(ctx)

	// The control plane serves both the browser frontend port and the API port
	// with the same handler (doc/03 §1.1). API/agent routes live under /api,
	// everything else falls through to the SPA.
	handler := srv.Handler()
	apiServer := &http.Server{
		Addr:              cfg.BackendAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // long polling tasks
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
	webServer := &http.Server{
		Addr:              cfg.FrontendAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 2)
	serve := func(srv *http.Server, addr, kind string) {
		log.Info("control plane listening", "addr", addr, "kind", kind)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("%s: %w", kind, err)
		}
	}
	go serve(apiServer, cfg.BackendAddr, "api")
	go serve(webServer, cfg.FrontendAddr, "frontend")

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiServer.Shutdown(shutdownCtx)
	_ = webServer.Shutdown(shutdownCtx)
	log.Info("control plane stopped")
	return nil
}

func setupLogger(cfg *config.Config) *slog.Logger {
	_ = os.MkdirAll(cfg.LogDir, 0o700)
	logFile, err := os.OpenFile(filepath.Join(cfg.LogDir, "control-plane.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return logger.NewDefault()
	}
	w := io.MultiWriter(os.Stderr, logFile)
	return logger.New(w, cfg.LogLevel)
}

// bootstrapAdmin creates the initial admin when configured.
func bootstrapAdmin(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger, strict bool) error {
	count, err := st.AdminCount(ctx)
	if err != nil {
		return err
	}
	password := cfg.AdminInitialPassword
	if password == "" && cfg.AdminInitialPasswordFile != "" {
		data, err := os.ReadFile(cfg.AdminInitialPasswordFile)
		if err != nil {
			return fmt.Errorf("read ADMIN_INITIAL_PASSWORD_FILE: %w", err)
		}
		password = strings.TrimSpace(string(data))
	}
	defer func() {
		// Secret hygiene: drop the initial password from the environment.
		_ = os.Unsetenv("ADMIN_INITIAL_PASSWORD")
		cfg.AdminInitialPassword = ""
		cfg.AdminInitialPasswordFile = ""
	}()
	if count == 0 {
		if password == "" {
			if strict {
				return fmt.Errorf("no admin exists and no ADMIN_INITIAL_PASSWORD(_FILE) provided")
			}
			log.Warn("no admin configured: set ADMIN_INITIAL_PASSWORD(_FILE) or run bootstrap-admin.sh")
			return nil
		}
		hash, err := security.HashPassword(password)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		admin := &model.AdminUser{
			ID:           model.NewUUID(),
			Username:     "admin",
			PasswordHash: hash,
		}
		admin.PasswordChangedAt = &now
		if err := st.CreateAdmin(ctx, admin); err != nil {
			return err
		}
		log.Info("initial admin created", "username", admin.Username)
		return nil
	}
	if password != "" {
		log.Warn("admin already exists; ignoring ADMIN_INITIAL_PASSWORD")
	}
	return nil
}

// childIdentity reads the local agent identity file.
func childIdentity(cfg *config.Config) (nodeID, credential, instanceName, instanceRole string) {
	identityPath := filepath.Join(cfg.AgentStateDir, "identity.json")
	data, err := os.ReadFile(identityPath)
	if err != nil {
		return "", "", "", ""
	}
	var ident struct {
		NodeID         string `json:"node_id"`
		NodeCredential string `json:"node_credential"`
		InstanceName   string `json:"instance_name"`
		InstanceRole   string `json:"instance_role"`
	}
	if err := json.Unmarshal(data, &ident); err != nil || ident.NodeID == "" {
		return "", "", "", ""
	}
	return ident.NodeID, ident.NodeCredential, ident.InstanceName, ident.InstanceRole
}

// refreshChildScope periodically re-reads the local agent identity so a child
// control plane picks up the node_id once the agent claims it (without restart).
func refreshChildScope(ctx context.Context, srv *api.Server, cfg *config.Config, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshSelfNode(ctx, srv, cfg, st, log)
		}
	}
}

// ensureChildNode makes the child's local DB aware of its own node identity so
// the restricted API can serve it. Returns the node_id scope.
func ensureChildNode(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) string {
	return refreshSelfNode(ctx, nil, cfg, st, log)
}

// refreshSelfNode binds the child API scope to the local agent identity and
// keeps a coherent local "self" node record: status Online with identity
// fields populated. The primary is authoritative for real heartbeat status;
// this only guarantees the child's restricted self-view is usable and is not
// flipped to offline by the local (non-authoritative) scheduler.
func refreshSelfNode(ctx context.Context, srv *api.Server, cfg *config.Config, st *store.Store, log *slog.Logger) string {
	nodeID, credential, instanceName, instanceRole := childIdentity(cfg)
	if nodeID == "" {
		identityPath := filepath.Join(cfg.AgentStateDir, "identity.json")
		log.Warn("no agent identity found; child API scope unavailable until agent claims", "path", identityPath)
		return ""
	}
	if credential == "" {
		log.Warn("agent identity has no node credential; child proxy will stay disabled", "node_id", nodeID)
	}
	if srv != nil {
		srv.SetChildScope(nodeID)
		srv.SetChildProxy(nodeID, credential)
	}
	hostname, _ := os.Hostname()
	frontendPort := addrPort(cfg.FrontendAddr)
	backendPort := addrPort(cfg.BackendAddr)
	osName := runtime.GOOS
	arch := runtime.GOARCH

	n, err := st.NodeByID(ctx, nodeID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Warn("failed to load local self node record", "node_id", nodeID, "error", err)
			return nodeID
		}
		now := time.Now().UTC()
		n = &model.Node{
			ID:              nodeID,
			EnvironmentID:   cfg.InstanceName + "-env",
			InstanceName:    instanceName,
			Role:            "child",
			Hostname:        hostname,
			Status:          model.NodeStatusOnline,
			Enabled:         true,
			OSName:          osName,
			OSVersion:       arch,
			Arch:            arch,
			FrontendPort:    frontendPort,
			BackendPort:     backendPort,
			LastHeartbeatAt: &now,
			LastOnlineAt:    &now,
		}
		if instanceRole != "" {
			n.Role = instanceRole
		}
		if err := st.CreateNode(ctx, n); err != nil {
			log.Warn("failed to create local self node record", "node_id", nodeID, "error", err)
			return ""
		}
		return nodeID
	}

	// Refresh only when something changed to avoid needless writes.
	changed := false
	if n.Status != model.NodeStatusOnline {
		n.Status = model.NodeStatusOnline
		changed = true
	}
	if instanceName != "" && n.InstanceName != instanceName {
		n.InstanceName = instanceName
		changed = true
	}
	if instanceRole != "" && n.Role != instanceRole {
		n.Role = instanceRole
		changed = true
	}
	if n.Hostname != hostname {
		n.Hostname = hostname
		changed = true
	}
	if n.FrontendPort != frontendPort {
		n.FrontendPort = frontendPort
		changed = true
	}
	if n.BackendPort != backendPort {
		n.BackendPort = backendPort
		changed = true
	}
	if n.OSName != osName {
		n.OSName = osName
		changed = true
	}
	if n.OSVersion != arch {
		n.OSVersion = arch
		changed = true
	}
	if n.Arch != arch {
		n.Arch = arch
		changed = true
	}
	if n.LastHeartbeatAt == nil {
		now := time.Now().UTC()
		n.LastHeartbeatAt = &now
		n.LastOnlineAt = &now
		changed = true
	}
	if changed {
		if err := st.UpdateNode(ctx, n); err != nil {
			log.Warn("failed to refresh local self node record", "node_id", nodeID, "error", err)
		}
	}
	return nodeID
}

// addrPort extracts the port from a host:port address ("" or 0 when absent).
func addrPort(addr string) int {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return 0
}
