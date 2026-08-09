package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"servercli/internal/model"
)

const autoApprovalColumns = `id, environment_id, ai_agent_id, ai_agent_name, node_id,
	source_request_id, created_by, created_at, updated_at, expires_at`

func scanAutoApproval(row interface{ Scan(...any) error }) (*model.AIAutoApproval, error) {
	var a model.AIAutoApproval
	var agentName, sourceReq, createdBy, createdAt, updatedAt, expiresAt sql.NullString
	if err := row.Scan(&a.ID, &a.EnvironmentID, &a.AIAgentID, &agentName, &a.NodeID,
		&sourceReq, &createdBy, &createdAt, &updatedAt, &expiresAt); err != nil {
		return nil, err
	}
	a.AIAgentName = agentName.String
	a.SourceRequestID = sourceReq.String
	a.CreatedBy = createdBy.String
	var err error
	if a.CreatedAt, err = parseTimeVal(createdAt); err != nil {
		return nil, err
	}
	if a.UpdatedAt, err = parseTimeVal(updatedAt); err != nil {
		return nil, err
	}
	if a.ExpiresAt, err = parseTimeVal(expiresAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// AutoApprovalByID finds an auto-approval rule.
func (s *Store) AutoApprovalByID(ctx context.Context, id string) (*model.AIAutoApproval, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+autoApprovalColumns+` FROM ai_auto_approval WHERE id=$1`, id)
	a, err := scanAutoApproval(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return a, nil
}

// AutoApprovalByAgentNode finds the rule for (env, device, node); returns
// ErrNotFound when absent.
func (s *Store) AutoApprovalByAgentNode(ctx context.Context, envID, aiAgentID, nodeID string) (*model.AIAutoApproval, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+autoApprovalColumns+` FROM ai_auto_approval
		WHERE environment_id=$1 AND ai_agent_id=$2 AND node_id=$3`, envID, aiAgentID, nodeID)
	a, err := scanAutoApproval(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return a, nil
}

// UpsertAutoApproval creates or updates the rule for (env, device, node).
// The existing ID is preserved on update; the returned rule is the persisted
// row. Uses INSERT ... ON CONFLICT so concurrent writers cannot race past the
// unique constraint.
func (s *Store) UpsertAutoApproval(ctx context.Context, a *model.AIAutoApproval) (*model.AIAutoApproval, error) {
	nowT := now()
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		if _, err := tsx.exec(ctx, `INSERT INTO ai_auto_approval
			(id, environment_id, ai_agent_id, ai_agent_name, node_id, source_request_id,
			 created_by, created_at, updated_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(environment_id, ai_agent_id, node_id) DO UPDATE SET
				ai_agent_name=$4, source_request_id=$6, updated_at=$9, expires_at=$10`,
			model.NewUUID(), a.EnvironmentID, a.AIAgentID, a.AIAgentName, a.NodeID, a.SourceRequestID,
			a.CreatedBy, ts(nowT), ts(nowT), ts(a.ExpiresAt)); err != nil {
			return err
		}
		return tsx.queryRow(ctx, `SELECT id FROM ai_auto_approval
			WHERE environment_id=$1 AND ai_agent_id=$2 AND node_id=$3`,
			a.EnvironmentID, a.AIAgentID, a.NodeID).Scan(&a.ID)
	})
	if err != nil {
		return nil, err
	}
	return s.AutoApprovalByID(ctx, a.ID)
}

// ListAutoApprovals lists rules with optional filters, newest expiry first.
// status: "" = all, "active" (expires_at > now), "expired" (expires_at <= now).
func (s *Store) ListAutoApprovals(ctx context.Context, nodeID, status string, limit, offset int) ([]*model.AIAutoApproval, error) {
	q := `SELECT ` + autoApprovalColumns + ` FROM ai_auto_approval`
	conds := []string{}
	args := []any{}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `node_id=$`+strconv.Itoa(len(args)))
	}
	switch status {
	case "active":
		args = append(args, ts(now()))
		conds = append(conds, `expires_at > $`+strconv.Itoa(len(args)))
	case "expired":
		args = append(args, ts(now()))
		conds = append(conds, `expires_at <= $`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY expires_at DESC`
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
	out := []*model.AIAutoApproval{}
	for rows.Next() {
		a, err := scanAutoApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
