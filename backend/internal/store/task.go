package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const taskColumns = `id, node_id, command_id, command_version, requested_by, idempotency_key, arguments_json,
	status, queued_at, started_at, finished_at, timeout_seconds, exit_code, error_code, error_message,
	result_summary_json, is_protected, protected_at`

func scanTask(row interface{ Scan(...any) error }) (*model.Task, error) {
	var t model.Task
	var queued, started, finished, protected sql.NullString
	var exitCode sql.NullInt64
	var timeout int64
	var prot int64
	if err := row.Scan(&t.ID, &t.NodeID, &t.CommandID, &t.CommandVersion, &t.RequestedBy, &t.IdempotencyKey, &t.ArgumentsJSON,
		&t.Status, &queued, &started, &finished, &timeout, &exitCode, &t.ErrorCode, &t.ErrorMessage,
		&t.ResultSummaryJSON, &prot, &protected); err != nil {
		return nil, err
	}
	t.TimeoutSeconds = int(timeout)
	t.IsProtected = parseBool(prot)
	if exitCode.Valid {
		ec := int(exitCode.Int64)
		t.ExitCode = &ec
	}
	var err error
	if t.QueuedAt, err = parseTimeVal(queued); err != nil {
		return nil, err
	}
	if t.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if t.FinishedAt, err = parseTime(finished); err != nil {
		return nil, err
	}
	if t.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTask inserts a queued task.
func (s *Store) CreateTask(ctx context.Context, t *model.Task) error {
	t.QueuedAt = now()
	t.Status = model.TaskQueued
	_, err := s.db.ExecContext(ctx, `INSERT INTO task
		(id, node_id, command_id, command_version, requested_by, idempotency_key, arguments_json,
		 status, queued_at, started_at, finished_at, timeout_seconds, exit_code, error_code, error_message,
		 result_summary_json, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		t.ID, t.NodeID, t.CommandID, t.CommandVersion, t.RequestedBy, t.IdempotencyKey, t.ArgumentsJSON,
		t.Status, ts(t.QueuedAt), nullTime(t.StartedAt), nullTime(t.FinishedAt), t.TimeoutSeconds, t.ExitCode, t.ErrorCode, t.ErrorMessage,
		t.ResultSummaryJSON, boolInt(t.IsProtected), nullTime(t.ProtectedAt))
	return err
}

// TaskByID finds a task.
func (s *Store) TaskByID(ctx context.Context, id string) (*model.Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task WHERE id=$1`, id)
	t, err := scanTask(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// TaskByIdempotency finds a task by (requested_by, idempotency_key).
func (s *Store) TaskByIdempotency(ctx context.Context, requestedBy, key string) (*model.Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task WHERE requested_by=$1 AND idempotency_key=$2`, requestedBy, key)
	t, err := scanTask(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// ListTasks returns tasks with filters, newest first.
func (s *Store) ListTasks(ctx context.Context, nodeID, status string, limit, offset int) ([]*model.Task, error) {
	q := `SELECT ` + taskColumns + ` FROM task`
	conds := []string{}
	args := []any{}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `node_id=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY queued_at DESC`
	if limit > 0 {
		args = append(args, limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	if offset > 0 {
		args = append(args, offset)
		q += ` OFFSET $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTask persists a task (status and terminal metadata).
func (s *Store) UpdateTask(ctx context.Context, t *model.Task) error {
	_, err := s.db.ExecContext(ctx, `UPDATE task SET
		status=$1, started_at=$2, finished_at=$3, exit_code=$4, error_code=$5, error_message=$6,
		result_summary_json=$7, is_protected=$8, protected_at=$9
		WHERE id=$10`,
		t.Status, nullTime(t.StartedAt), nullTime(t.FinishedAt), t.ExitCode, t.ErrorCode, t.ErrorMessage,
		t.ResultSummaryJSON, boolInt(t.IsProtected), nullTime(t.ProtectedAt), t.ID)
	return err
}

// ClaimTaskForDispatch atomically moves a queued task to dispatched.
func (s *Store) ClaimTaskForDispatch(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE task SET status=$1, started_at=$2 WHERE id=$3 AND status=$4`,
		model.TaskDispatched, ts(at), id, model.TaskQueued)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStateTransition
	}
	return nil
}

// MarkTaskRunning moves a dispatched task to running.
func (s *Store) MarkTaskRunning(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE task SET status=$1 WHERE id=$2 AND status=$3`,
		model.TaskRunning, id, model.TaskDispatched)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStateTransition
	}
	return nil
}

// QueueTasksForRetry moves stale dispatched tasks back to queued (node was
// unreachable and no result arrived).
func (s *Store) QueueTasksForRetry(ctx context.Context, nodeID string, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM task WHERE node_id=$1 AND status=$2 AND started_at < $3`,
		nodeID, model.TaskDispatched, ts(before))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE task SET status=$1 WHERE id=$2 AND status=$3`,
			model.TaskNodeUnreachable, id, model.TaskDispatched); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// ---- task_event ----

// AppendTaskEvent inserts a task event with an auto-incrementing sequence.
func (s *Store) AppendTaskEvent(ctx context.Context, ev *model.TaskEvent) error {
	ev.ID = model.NewUUID()
	ev.OccurredAt = now()
	// Sequence: max+1 scoped to the task.
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM task_event WHERE task_id=$1`, ev.TaskID).Scan(&ev.Sequence); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_event (id, task_id, sequence, event_type, status, message, occurred_at, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		ev.ID, ev.TaskID, ev.Sequence, ev.EventType, ev.Status, ev.Message, ts(ev.OccurredAt), ev.Source)
	return err
}

// TaskEvents lists events for a task ordered by sequence.
func (s *Store) TaskEvents(ctx context.Context, taskID string) ([]*model.TaskEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, sequence, event_type, status, message, occurred_at, source
		FROM task_event WHERE task_id=$1 ORDER BY sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.TaskEvent{}
	for rows.Next() {
		var e model.TaskEvent
		var occ sql.NullString
		var status, msg, source sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Sequence, &e.EventType, &status, &msg, &occ, &source); err != nil {
			return nil, err
		}
		e.Status = status.String
		e.Message = msg.String
		e.Source = source.String
		if e.OccurredAt, err = parseTimeVal(occ); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- task_output ----

// CreateTaskOutput inserts output for a task.
func (s *Store) CreateTaskOutput(ctx context.Context, o *model.TaskOutput) error {
	o.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_output
		(task_id, stdout_text, stderr_text, stdout_bytes, stderr_bytes, truncated, redaction_count, encoding, created_at, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		o.TaskID, o.StdoutText, o.StderrText, o.StdoutBytes, o.StderrBytes, boolInt(o.Truncated), o.RedactionCount, o.Encoding, ts(o.CreatedAt), boolInt(o.IsProtected), nullTime(o.ProtectedAt))
	return err
}

// UpdateTaskOutput replaces output content.
func (s *Store) UpdateTaskOutput(ctx context.Context, o *model.TaskOutput) error {
	_, err := s.db.ExecContext(ctx, `UPDATE task_output SET
		stdout_text=$1, stderr_text=$2, stdout_bytes=$3, stderr_bytes=$4, truncated=$5, redaction_count=$6
		WHERE task_id=$7`,
		o.StdoutText, o.StderrText, o.StdoutBytes, o.StderrBytes, boolInt(o.Truncated), o.RedactionCount, o.TaskID)
	return err
}

// TaskOutput fetches output for a task (ErrNotFound if absent).
func (s *Store) TaskOutput(ctx context.Context, taskID string) (*model.TaskOutput, error) {
	row := s.db.QueryRowContext(ctx, `SELECT task_id, stdout_text, stderr_text, stdout_bytes, stderr_bytes, truncated, redaction_count, encoding, created_at, is_protected, protected_at
		FROM task_output WHERE task_id=$1`, taskID)
	var o model.TaskOutput
	var created, protected sql.NullString
	var stdoutBytes, stderrBytes int64
	var truncated, prot int64
	if err := row.Scan(&o.TaskID, &o.StdoutText, &o.StderrText, &stdoutBytes, &stderrBytes, &truncated, &o.RedactionCount, &o.Encoding, &created, &prot, &protected); err != nil {
		return nil, sqlErr(err)
	}
	o.StdoutBytes = stdoutBytes
	o.StderrBytes = stderrBytes
	o.Truncated = parseBool(truncated)
	o.IsProtected = parseBool(prot)
	var err error
	if o.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if o.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &o, nil
}

// ClaimNextTask atomically claims the oldest queued task for a node. It
// retries on contention and returns ErrNotFound when the queue is empty.
func (s *Store) ClaimNextTask(ctx context.Context, nodeID string) (*model.Task, error) {
	for {
		claimed := false
		var t *model.Task
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			tsx := s.Tx(tx)
			row := tsx.queryRow(ctx, `SELECT `+taskColumns+` FROM task WHERE node_id=$1 AND status=$2 ORDER BY queued_at, id LIMIT 1`,
				nodeID, model.TaskQueued)
			var err error
			t, err = scanTask(row)
			if err != nil {
				return err
			}
			res, err := tsx.exec(ctx, `UPDATE task SET status=$1, started_at=$2 WHERE id=$3 AND status=$4`,
				model.TaskDispatched, ts(now()), t.ID, model.TaskQueued)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				claimed = false
				return nil
			}
			claimed = true
			return nil
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if !claimed {
			continue
		}
		return t, nil
	}
}

// AppendTaskEventDedup inserts a task event with an explicit sequence,
// ignoring duplicates.
func (s *Store) AppendTaskEventDedup(ctx context.Context, ev *model.TaskEvent) error {
	ev.ID = model.NewUUID()
	ev.OccurredAt = now()
	ignore := "INSERT OR IGNORE"
	if s.db.Driver == "postgres" {
		ignore = "INSERT"
	}
	insert := ignore + ` INTO task_event (id, task_id, sequence, event_type, status, message, occurred_at, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	if s.db.Driver == "postgres" {
		insert += ` ON CONFLICT(task_id, sequence) DO NOTHING`
	}
	_, err := s.db.ExecContext(ctx, insert,
		ev.ID, ev.TaskID, ev.Sequence, ev.EventType, ev.Status, ev.Message, ts(ev.OccurredAt), ev.Source)
	return err
}

// TaskEventMaxSequence returns the highest sequence for a task.
func (s *Store) TaskEventMaxSequence(ctx context.Context, taskID string) (int64, error) {
	var maxSeq sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(sequence) FROM task_event WHERE task_id=$1`, taskID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	return maxSeq.Int64, nil
}

// CancelledRunningTasks returns running tasks for a node that were cancelled.
func (s *Store) CancelledRunningTasks(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM task WHERE node_id=$1 AND status=$2`, nodeID, model.TaskCancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
