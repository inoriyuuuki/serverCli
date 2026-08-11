package store

import (
	"context"
	"time"

	"servercli/internal/model"
)

// UpsertServiceOwnership atomically replaces the ownership report of a node:
// stale services are removed, current ones inserted. Callers pass the full
// report from the agent heartbeat.
func (s *Store) UpsertServiceOwnership(ctx context.Context, nodeID string, rows []model.ServiceOwnership) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_ownership WHERE node_id=$1`, nodeID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO service_ownership (node_id, service, owner, config_digest, environment, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (node_id, service) DO UPDATE SET
			owner=excluded.owner, config_digest=excluded.config_digest,
			environment=excluded.environment, updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, nodeID, r.Service, r.Owner, r.ConfigDigest, r.Environment, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearServiceOwnership removes all ownership rows for a node (used when a
// node is deleted or its agent reports an empty ownership set).
func (s *Store) ClearServiceOwnership(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM service_ownership WHERE node_id=$1`, nodeID)
	return err
}

// ListServiceOwnership returns ownership rows for a node, or for all nodes
// when nodeID is empty.
func (s *Store) ListServiceOwnership(ctx context.Context, nodeID string) ([]model.ServiceOwnership, error) {
	query := `SELECT node_id, service, owner, config_digest, environment, updated_at FROM service_ownership`
	args := []any{}
	if nodeID != "" {
		query += ` WHERE node_id=$1`
		args = append(args, nodeID)
	}
	query += ` ORDER BY node_id, service`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ServiceOwnership
	for rows.Next() {
		var r model.ServiceOwnership
		if err := rows.Scan(&r.NodeID, &r.Service, &r.Owner, &r.ConfigDigest, &r.Environment, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
