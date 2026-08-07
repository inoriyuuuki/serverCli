package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/store"
)

// BatchLimit caps rows deleted per table per run.
const BatchLimit = 500

// CleanupService runs retention-based data cleanup.
type CleanupService struct {
	store    *store.Store
	settings *SettingsService
	cfg      *config.Config
	log      *slog.Logger
	auditor  *Auditor
}

// NewCleanupService builds the service.
func NewCleanupService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, settings *SettingsService) *CleanupService {
	return &CleanupService{store: st, settings: settings, cfg: cfg, log: log, auditor: auditor}
}

// DataType names accepted by the API.
const (
	DataHeartbeats    = "heartbeats"
	DataTaskOutputs   = "task_outputs"
	DataTasks         = "tasks"
	DataLeaseRequests = "lease_requests"
	DataLeases        = "leases"
	DataSSHSessions   = "ssh_sessions"
	DataAudit         = "audit"
	DataCleanupRuns   = "cleanup_runs"
	DataAdminSessions = "admin_sessions"
)

// CleanupOptions configures one cleanup run.
type CleanupOptions struct {
	DryRun      bool
	DataTypes   []string
	RequestedBy string
	Trigger     string
}

// RetentionDays returns the configured retention in days.
func (s *CleanupService) RetentionDays(ctx context.Context) int {
	d := s.settings.Int(ctx, KeyRetentionDays, s.cfg.RetentionDays)
	if d <= 0 {
		d = 7
	}
	return d
}

// Run executes cleanup and records a cleanup_run + audit event.
func (s *CleanupService) Run(ctx context.Context, opts CleanupOptions) (*model.CleanupRun, error) {
	if opts.Trigger == "" {
		opts.Trigger = "manual"
	}
	if opts.DryRun {
		opts.Trigger = "dry_run"
	}
	policy := map[string]any{
		"retention_days": s.RetentionDays(ctx),
		"batch_limit":    BatchLimit,
		"data_types":     opts.DataTypes,
		"dry_run":        opts.DryRun,
		"trigger":        opts.Trigger,
	}
	policyJSON, _ := json.Marshal(policy)
	run := &model.CleanupRun{
		TriggerType:        opts.Trigger,
		PolicySnapshotJSON: string(policyJSON),
		RequestedBy:        opts.RequestedBy,
	}
	if err := s.store.CreateCleanupRun(ctx, run); err != nil {
		return nil, err
	}
	stats := []store.CleanupStats{}
	var err error
	if len(opts.DataTypes) == 0 {
		stats, err = s.runAll(ctx, run.StartedAt, opts.DryRun)
	} else {
		stats, err = s.runSelected(ctx, opts.DataTypes, run.StartedAt, opts.DryRun)
	}
	finished := time.Now().UTC()
	var totalCandidates, totalDeleted, totalSkipped int64
	for _, st := range stats {
		totalCandidates += st.Candidates
		totalDeleted += st.Deleted
		totalSkipped += st.SkippedProtected
	}
	status := "completed"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = err.Error()
	}
	if err := s.store.FinishCleanupRun(ctx, run.ID, finished, status, errMsg, totalCandidates, totalDeleted, totalSkipped); err != nil {
		return run, err
	}
	run.Status = status
	run.FinishedAt = &finished
	run.CandidateCount = totalCandidates
	run.DeletedCount = totalDeleted
	run.SkippedProtectedCount = totalSkipped
	run.ErrorMessage = errMsg
	details := map[string]any{"dry_run": opts.DryRun, "candidates": totalCandidates, "deleted": totalDeleted,
		"skipped_protected": totalSkipped, "tables": stats}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorSystem, ActorID: opts.RequestedBy, Action: "data.cleanup",
		ResourceType: "cleanup_run", ResourceID: run.ID, Summary: "data cleanup completed",
		Details: details, RiskLevel: RiskMedium,
	})
	if err != nil {
		s.auditor.Failure(ctx, AuditInput{
			ActorType: model.ActorSystem, ActorID: opts.RequestedBy, Action: "data.cleanup_failed",
			ResourceType: "cleanup_run", ResourceID: run.ID, Summary: "data cleanup failed: " + errMsg,
			RiskLevel: RiskHigh,
		})
	}
	return run, err
}

func (s *CleanupService) runAll(ctx context.Context, startedAt time.Time, dryRun bool) ([]store.CleanupStats, error) {
	return s.runSelected(ctx, []string{
		DataHeartbeats, DataTaskOutputs, DataSSHSessions, DataAudit,
		DataTasks, DataLeaseRequests, DataLeases, DataCleanupRuns, DataAdminSessions,
	}, startedAt, dryRun)
}

func (s *CleanupService) runSelected(ctx context.Context, types []string, startedAt time.Time, dryRun bool) ([]store.CleanupStats, error) {
	retention := s.RetentionDays(ctx)
	now := time.Now().UTC()
	cutoff7 := now.Add(-time.Duration(retention) * 24 * time.Hour)
	cutoff30 := now.Add(-30 * 24 * time.Hour)
	cutoff90 := now.Add(-90 * 24 * time.Hour)
	stats := []store.CleanupStats{}
	var firstErr error
	for _, t := range types {
		var st store.CleanupStats
		var err error
		switch t {
		case DataHeartbeats:
			st, err = s.store.CleanupHeartbeats(ctx, cutoff7, BatchLimit, dryRun)
		case DataTaskOutputs:
			st, err = s.store.CleanupTaskOutputs(ctx, cutoff7, BatchLimit, dryRun)
		case DataSSHSessions:
			st, err = s.store.CleanupSSHSessions(ctx, cutoff7, BatchLimit, dryRun)
		case DataAudit:
			st, err = s.store.CleanupAuditEvents(ctx, cutoff7, BatchLimit, dryRun)
		case DataTasks:
			st, err = s.store.CleanupTasks(ctx, cutoff30, BatchLimit, dryRun)
		case DataLeaseRequests:
			st, err = s.store.CleanupLeaseRequests(ctx, cutoff30, BatchLimit, dryRun)
		case DataLeases:
			st, err = s.store.CleanupLeases(ctx, cutoff30, BatchLimit, dryRun)
		case DataCleanupRuns:
			st, err = s.store.CleanupRuns(ctx, cutoff90, BatchLimit, dryRun)
		case DataAdminSessions:
			st, err = s.store.CleanupAdminSessions(ctx, cutoff7, BatchLimit, dryRun)
		default:
			err = fmt.Errorf("unknown data type %q", t)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		stats = append(stats, st)
	}
	return stats, firstErr
}

// ListRuns returns recent cleanup runs.
func (s *CleanupService) ListRuns(ctx context.Context, limit, offset int) ([]*model.CleanupRun, error) {
	return s.store.ListCleanupRuns(ctx, limit, offset)
}

var _ = errors.Is
