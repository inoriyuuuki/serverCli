// Command servercli-node-agent runs the node agent: registration, heartbeat,
// command snapshot, task execution and SSH lease key management.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"servercli/internal/agent"
	"servercli/internal/config"
	"servercli/internal/logger"
)

// version is overridden at build time via -X main.version=${VERSION} (see
// .github/workflows/build-binaries.yml) and reported as the agent version in
// enrollments/heartbeats so the server list shows the real release tag.
var version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "node-agent: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := setupLogger(cfg)
	log.Info("node agent starting", "instance", cfg.InstanceName, "role", cfg.NodeRole,
		"primary_backend_url", cfg.PrimaryBackendURL, "state_dir", cfg.AgentStateDir)

	if err := os.MkdirAll(cfg.AgentStateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LogDir, 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent.AgentVersion = version
	a := agent.NewAgent(cfg, log)
	if err := a.Run(ctx); err != nil {
		return err
	}
	log.Info("node agent stopped")
	return nil
}

func setupLogger(cfg *config.Config) *slog.Logger {
	_ = os.MkdirAll(cfg.LogDir, 0o700)
	logFile, err := os.OpenFile(filepath.Join(cfg.LogDir, "node-agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return logger.NewDefault()
	}
	w := io.MultiWriter(os.Stderr, logFile)
	return logger.New(w, cfg.LogLevel)
}
