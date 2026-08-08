// Package scheduler runs periodic control plane maintenance: offline
// detection, lease expiry and scheduled cleanup.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"servercli/internal/model"
	"servercli/internal/service"
)

// Scheduler runs periodic jobs.
type Scheduler struct {
	log      *slog.Logger
	nodes    *service.NodeService
	leases   *service.LeaseService
	cleanup  *service.CleanupService
	settings *service.SettingsService
	// scoped marks a child (scoped) control plane. A scoped control plane is
	// not the authority on node liveness (heartbeats go to the primary), so
	// offline detection is skipped to keep the local self node from flipping
	// to offline.
	scoped bool
}

// New builds a scheduler. Pass scoped=true for child control planes whose API
// is bound to the local node identity.
func New(log *slog.Logger, nodes *service.NodeService, leases *service.LeaseService, cleanup *service.CleanupService, settings *service.SettingsService, scoped bool) *Scheduler {
	return &Scheduler{log: log, nodes: nodes, leases: leases, cleanup: cleanup, settings: settings, scoped: scoped}
}

// Run executes maintenance ticks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	// Initial pass shortly after startup.
	time.Sleep(2 * time.Second)
	s.tick(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
			if s.cleanupDue(ctx, lastCleanup) {
				lastCleanup = time.Now().UTC()
				s.runCleanup(ctx)
			}
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	if !s.scoped {
		if _, err := s.nodes.MarkOfflineNodes(ctx); err != nil {
			s.log.Warn("offline detection failed", "error", err)
		}
	}
	if _, err := s.leases.ExpireStaleLeases(ctx); err != nil {
		s.log.Warn("lease expiry failed", "error", err)
	}
}

func (s *Scheduler) cleanupDue(ctx context.Context, last time.Time) bool {
	schedule := s.settings.Str(ctx, "cleanup_schedule", "weekly")
	var interval time.Duration
	switch schedule {
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "daily":
		interval = 24 * time.Hour
	default:
		return false
	}
	return last.IsZero() || time.Since(last) >= interval
}

func (s *Scheduler) runCleanup(ctx context.Context) {
	run, err := s.cleanup.Run(ctx, service.CleanupOptions{
		DryRun:      false,
		RequestedBy: "system",
		Trigger:     "schedule",
	})
	if err != nil {
		s.log.Warn("scheduled cleanup failed", "error", err, "run_id", runID(run))
		return
	}
	s.log.Info("scheduled cleanup completed", "run_id", runID(run), "deleted", run.DeletedCount)
}

func runID(run *model.CleanupRun) string {
	if run == nil {
		return ""
	}
	return run.ID
}
