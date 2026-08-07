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

	// Scheduler.
	sched := scheduler.New(log, srv.NodeService(), srv.LeaseService(), srv.CleanupService(), srv.SettingsService())
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

// childIdentityID reads the local agent identity file and returns the node_id.
func childIdentityID(cfg *config.Config) string {
	identityPath := filepath.Join(cfg.AgentStateDir, "identity.json")
	data, err := os.ReadFile(identityPath)
	if err != nil {
		return ""
	}
	var ident struct {
		NodeID       string `json:"node_id"`
		InstanceName string `json:"instance_name"`
		InstanceRole string `json:"instance_role"`
	}
	if err := json.Unmarshal(data, &ident); err != nil || ident.NodeID == "" {
		return ""
	}
	return ident.NodeID
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
			if id := childIdentityID(cfg); id != "" {
				srv.SetChildScope(id)
				// Ensure the local node record exists for the restricted API.
				if _, err := st.NodeByID(ctx, id); err != nil {
					node := &model.Node{
						ID:            id,
						EnvironmentID: cfg.InstanceName + "-env",
						InstanceName:  cfg.InstanceName,
						Role:          cfg.NodeRole,
						Status:        model.NodeStatusOnline,
						Enabled:       true,
					}
					if cerr := st.CreateNode(ctx, node); cerr != nil {
						log.Warn("failed to create local node record", "node_id", id, "error", cerr)
					}
				}
			}
		}
	}
}

// ensureChildNode makes the child's local DB aware of its own node identity so
// the restricted API can serve it. Returns the node_id scope.
func ensureChildNode(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) string {
	ident := childIdentityID(cfg)
	if ident == "" {
		identityPath := filepath.Join(cfg.AgentStateDir, "identity.json")
		log.Warn("no agent identity found; child API scope unavailable until agent claims", "path", identityPath)
		return ""
	}
	var nodeID, instanceName, instanceRole string
	nodeID = ident
	data, _ := os.ReadFile(filepath.Join(cfg.AgentStateDir, "identity.json"))
	var full struct {
		NodeID       string `json:"node_id"`
		InstanceName string `json:"instance_name"`
		InstanceRole string `json:"instance_role"`
	}
	_ = json.Unmarshal(data, &full)
	instanceName = full.InstanceName
	instanceRole = full.InstanceRole
	if _, err := st.NodeByID(ctx, nodeID); err != nil {
		// Create a local representation of this node.
		node := &model.Node{
			ID:            nodeID,
			EnvironmentID: cfg.InstanceName + "-env",
			InstanceName:  instanceName,
			Role:          "child",
			Status:        model.NodeStatusOnline,
			Enabled:       true,
		}
		if instanceRole != "" {
			node.Role = instanceRole
		}
		if err := st.CreateNode(ctx, node); err != nil {
			log.Warn("failed to create local node record", "error", err)
			return ""
		}
	}
	return nodeID
}
