package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"servercli/internal/model"
)

// CleanupStats tracks candidate/deleted/skipped counts for one table.
type CleanupStats struct {
	Table            string
	Candidates       int64
	Deleted          int64
	SkippedProtected int64
}

// CreateCleanupRun inserts a cleanup run.
func (s *Store) CreateCleanupRun(ctx context.Context, r *model.CleanupRun) error {
	r.ID = model.NewUUID()
	r.StartedAt = now()
	r.Status = "running"
	_, err := s.db.ExecContext(ctx, `INSERT INTO cleanup_run
		(id, started_at, finished_at, trigger_type, policy_snapshot_json, candidate_count, deleted_count,
		 skipped_protected_count, status, error_message, requested_by, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.ID, ts(r.StartedAt), nullTime(r.FinishedAt), r.TriggerType, r.PolicySnapshotJSON,
		r.CandidateCount, r.DeletedCount, r.SkippedProtectedCount, r.Status, r.ErrorMessage, r.RequestedBy,
		boolInt(r.IsProtected), nullTime(r.ProtectedAt))
	return err
}

// FinishCleanupRun records the outcome of a cleanup run.
func (s *Store) FinishCleanupRun(ctx context.Context, id string, finishedAt time.Time, status, errMsg string, candidate, deleted, skipped int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cleanup_run SET
		finished_at=$1, status=$2, error_message=$3, candidate_count=$4, deleted_count=$5, skipped_protected_count=$6
		WHERE id=$7`,
		ts(finishedAt), status, errMsg, candidate, deleted, skipped, id)
	return err
}

// ListCleanupRuns returns cleanup runs newest first.
func (s *Store) ListCleanupRuns(ctx context.Context, limit, offset int) ([]*model.CleanupRun, error) {
	q := `SELECT id, started_at, finished_at, trigger_type, policy_snapshot_json, candidate_count, deleted_count,
		skipped_protected_count, status, error_message, requested_by, is_protected, protected_at FROM cleanup_run ORDER BY started_at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT $1`
		args = append(args, limit)
	}
	if offset > 0 {
		q += ` OFFSET $` + strconv.Itoa(len(args)+1)
		args = append(args, offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.CleanupRun{}
	for rows.Next() {
		var r model.CleanupRun
		var started, finished, policy, protected sql.NullString
		var candidate, deleted, skipped, prot int64
		if err := rows.Scan(&r.ID, &started, &finished, &r.TriggerType, &policy, &candidate, &deleted,
			&skipped, &r.Status, &r.ErrorMessage, &r.RequestedBy, &prot, &protected); err != nil {
			return nil, err
		}
		r.PolicySnapshotJSON = policy.String
		r.CandidateCount = candidate
		r.DeletedCount = deleted
		r.SkippedProtectedCount = skipped
		r.IsProtected = parseBool(prot)
		var err error
		if r.StartedAt, err = parseTimeVal(started); err != nil {
			return nil, err
		}
		if r.FinishedAt, err = parseTime(finished); err != nil {
			return nil, err
		}
		if r.ProtectedAt, err = parseTime(protected); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// deleteBatched removes expired, non-protected rows from table in batches.
// timeCol is the timestamp column and idCol the primary key. The audit/cleanup
// tables pass a cutoff that never includes the current run's rows. When dryRun
// is true only candidate counts are computed.
func (s *Store) deleteBatched(ctx context.Context, table, idCol, timeCol string, cutoff time.Time, limit int, protectedCol bool, dryRun bool) (CleanupStats, error) {
	stats := CleanupStats{Table: table}
	protWhere := ""
	if protectedCol {
		protWhere = ` AND is_protected=0`
	}
	var candidates int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+timeCol+` < $1`+protWhere, ts(cutoff)).Scan(&candidates); err != nil {
		return stats, err
	}
	stats.Candidates = candidates
	if protectedCol {
		var skipped int64
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+timeCol+` < $1 AND is_protected=1`, ts(cutoff)).Scan(&skipped); err != nil {
			return stats, err
		}
		stats.SkippedProtected = skipped
	}
	if dryRun {
		return stats, nil
	}
	for candidates > 0 {
		res, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+idCol+` IN (
			SELECT `+idCol+` FROM `+table+` WHERE `+timeCol+` < $1`+protWhere+` LIMIT $2)`, ts(cutoff), limit)
		if err != nil {
			return stats, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
		stats.Deleted += n
		candidates -= n
	}
	return stats, nil
}

// CleanupHeartbeats removes old node_heartbeat rows.
func (s *Store) CleanupHeartbeats(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "node_heartbeat", "id", "recorded_at", cutoff, limit, true, dryRun)
}

// CleanupTaskOutputs removes old task_output rows.
func (s *Store) CleanupTaskOutputs(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "task_output", "task_id", "created_at", cutoff, limit, true, dryRun)
}

// CleanupTasks removes old task rows.
func (s *Store) CleanupTasks(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "task", "id", "queued_at", cutoff, limit, true, dryRun)
}

// CleanupLeaseRequests removes old ai_lease_request rows.
func (s *Store) CleanupLeaseRequests(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "ai_lease_request", "id", "created_at", cutoff, limit, true, dryRun)
}

// CleanupLeases removes old ai_lease rows.
func (s *Store) CleanupLeases(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "ai_lease", "id", "issued_at", cutoff, limit, true, dryRun)
}

// CleanupSSHSessions removes old ai_ssh_session rows.
func (s *Store) CleanupSSHSessions(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "ai_ssh_session", "id", "started_at", cutoff, limit, true, dryRun)
}

// CleanupAuditEvents removes old audit rows (never the current run's audit).
func (s *Store) CleanupAuditEvents(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "audit_event", "id", "occurred_at", cutoff, limit, true, dryRun)
}

// CleanupRuns removes old cleanup_run rows.
func (s *Store) CleanupRuns(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	return s.deleteBatched(ctx, "cleanup_run", "id", "started_at", cutoff, limit, true, dryRun)
}

// CleanupAdminSessions removes expired/revoked sessions older than cutoff.
func (s *Store) CleanupAdminSessions(ctx context.Context, cutoff time.Time, limit int, dryRun bool) (CleanupStats, error) {
	stats := CleanupStats{Table: "admin_session"}
	var candidates int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_session WHERE (expires_at < $1 OR revoked_at IS NOT NULL)`, ts(cutoff)).Scan(&candidates); err != nil {
		return stats, err
	}
	stats.Candidates = candidates
	if dryRun {
		return stats, nil
	}
	for candidates > 0 {
		res, err := s.db.ExecContext(ctx, `DELETE FROM admin_session WHERE id IN (
			SELECT id FROM admin_session WHERE (expires_at < $1 OR revoked_at IS NOT NULL) LIMIT $2)`, ts(cutoff), limit)
		if err != nil {
			return stats, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
		stats.Deleted += n
		candidates -= n
	}
	return stats, nil
}
