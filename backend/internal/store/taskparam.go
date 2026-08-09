package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const taskParamColumns = `id, node_id, command_id, command_version, arguments_json, arguments_hash,
	last_task_id, first_used_at, last_used_at, use_count`

// CanonicalArgumentsJSON normalizes raw argument JSON (stable key order) and
// returns the canonical bytes and its SHA-256 hex hash. Empty objects and
// null are not considered reusable and return ok=false.
func CanonicalArgumentsJSON(raw string) (canon string, hash string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" || trimmed == "[]" {
		return "", "", false
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return "", "", false
	}
	canonBytes, err := json.Marshal(v)
	if err != nil {
		return "", "", false
	}
	sum := sha256.Sum256(canonBytes)
	return string(canonBytes), hex.EncodeToString(sum[:]), true
}

func scanTaskParam(row interface{ Scan(...any) error }) (*model.TaskParameterHistory, error) {
	var p model.TaskParameterHistory
	var argsJSON, lastTask, firstUsed, lastUsed sql.NullString
	var useCount int64
	if err := row.Scan(&p.ID, &p.NodeID, &p.CommandID, &p.CommandVersion, &argsJSON, &p.ArgumentsHash,
		&lastTask, &firstUsed, &lastUsed, &useCount); err != nil {
		return nil, err
	}
	p.ArgumentsJSON = argsJSON.String
	p.LastTaskID = lastTask.String
	p.UseCount = int(useCount)
	var err error
	if p.FirstUsedAt, err = parseTimeVal(firstUsed); err != nil {
		return nil, err
	}
	if p.LastUsedAt, err = parseTimeVal(lastUsed); err != nil {
		return nil, err
	}
	if p.ArgumentsJSON != "" {
		_ = json.Unmarshal([]byte(p.ArgumentsJSON), &p.Arguments)
	}
	return &p, nil
}

// RecordTaskParameterUsage records (or bumps) the parameter history entry for
// a task. Empty argument sets are ignored. Call after the task is queued.
func (s *Store) RecordTaskParameterUsage(ctx context.Context, nodeID, commandID, commandVersion, argsJSON, hash, taskID string) error {
	canon, computedHash, ok := CanonicalArgumentsJSON(argsJSON)
	if !ok {
		return nil
	}
	if hash == "" {
		hash = computedHash
	}
	nowT := now()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		var existing string
		err := tsx.queryRow(ctx, `SELECT id FROM task_parameter_history
			WHERE node_id=$1 AND command_id=$2 AND command_version=$3 AND arguments_hash=$4`,
			nodeID, commandID, commandVersion, hash).Scan(&existing)
		if err == nil {
			_, err = tsx.exec(ctx, `UPDATE task_parameter_history SET
				last_used_at=$1, last_task_id=$2, use_count=use_count+1 WHERE id=$3`,
				ts(nowT), taskID, existing)
			return err
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tsx.exec(ctx, `INSERT INTO task_parameter_history
			(id, node_id, command_id, command_version, arguments_json, arguments_hash,
			 last_task_id, first_used_at, last_used_at, use_count)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			model.NewUUID(), nodeID, commandID, commandVersion, canon, hash,
			taskID, ts(nowT), ts(nowT), 1)
		return err
	})
}

// ListTaskParameterHistories returns reusable parameter sets, newest usage
// first. nodeIDs may be empty for all nodes.
func (s *Store) ListTaskParameterHistories(ctx context.Context, nodeIDs []string, commandID, commandVersion string, limit, offset int) ([]*model.TaskParameterHistory, error) {
	q := `SELECT ` + taskParamColumns + ` FROM task_parameter_history`
	conds := []string{}
	args := []any{}
	if len(nodeIDs) > 0 {
		ph := make([]string, 0, len(nodeIDs))
		for _, id := range nodeIDs {
			args = append(args, id)
			ph = append(ph, `$`+strconv.Itoa(len(args)))
		}
		conds = append(conds, `node_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if commandID != "" {
		args = append(args, commandID)
		conds = append(conds, `command_id=$`+strconv.Itoa(len(args)))
	}
	if commandVersion != "" {
		args = append(args, commandVersion)
		conds = append(conds, `command_version=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY last_used_at DESC`
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
	out := []*model.TaskParameterHistory{}
	for rows.Next() {
		p, err := scanTaskParam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TaskParameterHistoryByID finds one entry.
func (s *Store) TaskParameterHistoryByID(ctx context.Context, id string) (*model.TaskParameterHistory, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskParamColumns+` FROM task_parameter_history WHERE id=$1`, id)
	p, err := scanTaskParam(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return p, nil
}

// DeleteTaskParameterHistory removes one entry.
func (s *Store) DeleteTaskParameterHistory(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM task_parameter_history WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BackfillTaskParameterHistories recomputes parameter history from existing
// tasks. It is idempotent: use counts are derived from the task table each
// run, never incremented.
func (s *Store) BackfillTaskParameterHistories(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, command_id, command_version, arguments_json, queued_at
		FROM task WHERE arguments_json IS NOT NULL AND arguments_json != ''`)
	if err != nil {
		return 0, err
	}
	type entry struct {
		canon     string
		hash      string
		count     int
		firstUsed time.Time
		lastTask  string
		lastUsed  time.Time
	}
	entries := map[string]*entry{}
	order := []string{}
	for rows.Next() {
		var taskID, nodeID, cmdID, ver, argsJSON string
		var queuedAt sql.NullString
		if err := rows.Scan(&taskID, &nodeID, &cmdID, &ver, &argsJSON, &queuedAt); err != nil {
			rows.Close()
			return 0, err
		}
		canon, hash, ok := CanonicalArgumentsJSON(argsJSON)
		if !ok {
			continue
		}
		key := nodeID + "\x00" + cmdID + "\x00" + ver + "\x00" + hash
		e := entries[key]
		if e == nil {
			e = &entry{canon: canon, hash: hash}
			entries[key] = e
			order = append(order, key)
		}
		e.count++
		t, terr := parseTimeVal(queuedAt)
		if terr != nil {
			continue
		}
		if e.firstUsed.IsZero() || t.Before(e.firstUsed) {
			e.firstUsed = t
		}
		if t.After(e.lastUsed) {
			e.lastUsed = t
			e.lastTask = taskID
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var recorded int64
	for _, key := range order {
		e := entries[key]
		parts := strings.SplitN(key, "\x00", 4)
		if len(parts) != 4 {
			continue
		}
		nodeID, cmdID, ver, hash := parts[0], parts[1], parts[2], parts[3]
		firstUsed := e.firstUsed
		if firstUsed.IsZero() {
			firstUsed = e.lastUsed
		}
		if firstUsed.IsZero() {
			firstUsed = now()
		}
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			tsx := s.Tx(tx)
			var existing string
			err := tsx.queryRow(ctx, `SELECT id FROM task_parameter_history
				WHERE node_id=$1 AND command_id=$2 AND command_version=$3 AND arguments_hash=$4`,
				nodeID, cmdID, ver, hash).Scan(&existing)
			if err == nil {
				_, err = tsx.exec(ctx, `UPDATE task_parameter_history SET
					arguments_json=$1, last_task_id=$2, first_used_at=$3, last_used_at=$4, use_count=$5
					WHERE id=$6`,
					e.canon, e.lastTask, ts(firstUsed), ts(e.lastUsed), e.count, existing)
				return err
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			_, err = tsx.exec(ctx, `INSERT INTO task_parameter_history
				(id, node_id, command_id, command_version, arguments_json, arguments_hash,
				 last_task_id, first_used_at, last_used_at, use_count)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				model.NewUUID(), nodeID, cmdID, ver, e.canon, hash,
				e.lastTask, ts(firstUsed), ts(e.lastUsed), e.count)
			return err
		})
		if err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}
