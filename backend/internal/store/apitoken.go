package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const accessTokenColumns = `id, environment_id, name, token_hash, token_prefix, created_by, created_at,
	expires_at, revoked_at, revoked_by, last_used_at, last_used_ip, usage_count, permission_version, permissions_json`

func scanAccessToken(row interface{ Scan(...any) error }) (*model.APIAccessToken, error) {
	var t model.APIAccessToken
	var createdBy, createdAt, expiresAt, revokedAt, revokedBy, lastUsedAt, lastUsedIP, permissions sql.NullString
	var usageCount, permissionVersion int64
	if err := row.Scan(&t.ID, &t.EnvironmentID, &t.Name, &t.TokenHash, &t.TokenPrefix, &createdBy, &createdAt,
		&expiresAt, &revokedAt, &revokedBy, &lastUsedAt, &lastUsedIP, &usageCount, &permissionVersion, &permissions); err != nil {
		return nil, err
	}
	t.CreatedBy = createdBy.String
	t.RevokedBy = revokedBy.String
	t.LastUsedIP = lastUsedIP.String
	t.PermissionsJSON = permissions.String
	t.UsageCount = usageCount
	t.PermissionVersion = int(permissionVersion)
	var err error
	if t.CreatedAt, err = parseTimeVal(createdAt); err != nil {
		return nil, err
	}
	if t.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, err
	}
	if t.RevokedAt, err = parseTime(revokedAt); err != nil {
		return nil, err
	}
	if t.LastUsedAt, err = parseTime(lastUsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateAccessToken inserts a new access token.
func (s *Store) CreateAccessToken(ctx context.Context, t *model.APIAccessToken) error {
	t.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_access_token
		(id, environment_id, name, token_hash, token_prefix, created_by, created_at,
		 expires_at, revoked_at, revoked_by, last_used_at, last_used_ip, usage_count, permission_version, permissions_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		t.ID, t.EnvironmentID, t.Name, t.TokenHash, t.TokenPrefix, t.CreatedBy, ts(t.CreatedAt),
		nullTime(t.ExpiresAt), nullTime(t.RevokedAt), t.RevokedBy, nullTime(t.LastUsedAt), t.LastUsedIP,
		t.UsageCount, t.PermissionVersion, t.PermissionsJSON)
	return err
}

// AccessTokenByHash finds a token by its SHA-256 hash.
func (s *Store) AccessTokenByHash(ctx context.Context, hash string) (*model.APIAccessToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accessTokenColumns+` FROM api_access_token WHERE token_hash=$1`, hash)
	t, err := scanAccessToken(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// AccessTokenByID finds a token by id.
func (s *Store) AccessTokenByID(ctx context.Context, id string) (*model.APIAccessToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accessTokenColumns+` FROM api_access_token WHERE id=$1`, id)
	t, err := scanAccessToken(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// ListAccessTokens returns tokens newest first with pagination.
func (s *Store) ListAccessTokens(ctx context.Context, limit, offset int) ([]*model.APIAccessToken, error) {
	q := `SELECT ` + accessTokenColumns + ` FROM api_access_token ORDER BY created_at DESC`
	if limit > 0 {
		q += ` LIMIT $1`
		args := []any{limit}
		if offset > 0 {
			q += ` OFFSET $2`
			args = append(args, offset)
		}
		rows, err := s.db.QueryContext(ctx, q, args...)
		return scanAccessTokens(rows, err)
	}
	if offset > 0 {
		q += ` OFFSET $1`
		rows, err := s.db.QueryContext(ctx, q, offset)
		return scanAccessTokens(rows, err)
	}
	rows, err := s.db.QueryContext(ctx, q)
	return scanAccessTokens(rows, err)
}

func scanAccessTokens(rows *sql.Rows, err error) ([]*model.APIAccessToken, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.APIAccessToken{}
	for rows.Next() {
		t, err := scanAccessToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAccessTokenAndLeases marks an access token revoked and revokes every
// active lease bound to it in a single transaction, so a crash cannot leave a
// half-revoked state. Idempotent: a token already revoked updates nothing and
// returns no leases. The returned leases are the ones actually revoked in this
// call (avoiding duplicate events on repeated revoke).
func (s *Store) RevokeAccessTokenAndLeases(ctx context.Context, tokenID, revokedBy, reason string) ([]*model.AILease, error) {
	var affected []*model.AILease
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		nowT := now()
		if _, err := tsx.exec(ctx, `UPDATE api_access_token SET revoked_at=$1, revoked_by=$2
			WHERE id=$3 AND revoked_at IS NULL`, ts(nowT), revokedBy, tokenID); err != nil {
			return err
		}
		rows, err := tsx.query(ctx, `SELECT `+leaseColumns+leaseFrom+`
			WHERE l.access_token_id=$1 AND l.status=$2`, tokenID, model.LeaseActive)
		if err != nil {
			return err
		}
		for rows.Next() {
			l, err := scanLease(rows)
			if err != nil {
				rows.Close()
				return err
			}
			affected = append(affected, l)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, l := range affected {
			if _, err := tsx.exec(ctx, `UPDATE ai_lease SET status=$1, revoked_at=$2, revoke_reason=$3
				WHERE id=$4 AND status=$5`, model.LeaseRevoked, ts(nowT), reason, l.ID, model.LeaseActive); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return affected, nil
}

// TouchAccessToken updates last use metadata.
func (s *Store) TouchAccessToken(ctx context.Context, id, sourceIP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_access_token SET
		last_used_at=$1, last_used_ip=$2, usage_count=usage_count+1 WHERE id=$3`,
		ts(now()), sourceIP, id)
	return err
}

// CreateTokenUsageLog inserts a token usage log entry.
func (s *Store) CreateTokenUsageLog(ctx context.Context, l *model.APITokenUsageLog) error {
	l.ID = model.NewUUID()
	l.OccurredAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_token_usage_log
		(id, token_id, environment_id, request_id, occurred_at, method, route, resource, action,
		 source_ip, user_agent, status_code, outcome, lease_request_id, lease_id, token_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		l.ID, l.TokenID, l.EnvironmentID, l.RequestID, ts(l.OccurredAt), l.Method, l.Route, l.Resource, l.Action,
		l.SourceIP, l.UserAgent, l.StatusCode, l.Outcome, l.LeaseRequestID, l.LeaseID, l.TokenState)
	return err
}

// ListTokenUsageLogs returns usage logs for a token, newest first.
// outcome: "" = all, otherwise success/denied/failure.
func (s *Store) ListTokenUsageLogs(ctx context.Context, tokenID, outcome string, limit, offset int) ([]*model.APITokenUsageLog, error) {
	q := `SELECT id, token_id, environment_id, request_id, occurred_at, method, route, resource, action,
		source_ip, user_agent, status_code, outcome, lease_request_id, lease_id, token_state
		FROM api_token_usage_log`
	conds := []string{`token_id=$1`}
	args := []any{tokenID}
	if outcome != "" {
		args = append(args, outcome)
		conds = append(conds, `outcome=$`+strconv.Itoa(len(args)))
	}
	q += ` WHERE ` + strings.Join(conds, ` AND `) + ` ORDER BY occurred_at DESC`
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
	out := []*model.APITokenUsageLog{}
	for rows.Next() {
		var l model.APITokenUsageLog
		var reqID, occurredAt, resource, action, sourceIP, userAgent, lrID, leaseID sql.NullString
		var statusCode int64
		if err := rows.Scan(&l.ID, &l.TokenID, &l.EnvironmentID, &reqID, &occurredAt, &l.Method, &l.Route,
			&resource, &action, &sourceIP, &userAgent, &statusCode, &l.Outcome, &lrID, &leaseID, &l.TokenState); err != nil {
			return nil, err
		}
		l.RequestID = reqID.String
		l.Resource = resource.String
		l.Action = action.String
		l.SourceIP = sourceIP.String
		l.UserAgent = userAgent.String
		l.LeaseRequestID = lrID.String
		l.LeaseID = leaseID.String
		l.StatusCode = int(statusCode)
		if l.OccurredAt, err = parseTimeVal(occurredAt); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

// ActiveLeasesByAccessToken returns active leases bound to a token.
func (s *Store) ActiveLeasesByAccessToken(ctx context.Context, tokenID string) ([]*model.AILease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+leaseColumns+leaseFrom+`
		WHERE l.access_token_id=$1 AND l.status=$2`, tokenID, model.LeaseActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AILease{}
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ActiveLeaseCountByAccessToken returns the number of active leases for a token.
func (s *Store) ActiveLeaseCountByAccessToken(ctx context.Context, tokenID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_lease WHERE access_token_id=$1 AND status=$2`,
		tokenID, model.LeaseActive).Scan(&n)
	return n, err
}

// RejectLegacyPendingRequests marks every pending lease request rejected with
// the given reason (used once at startup after the token migration).
func (s *Store) RejectLegacyPendingRequests(ctx context.Context, reason string) (int64, error) {
	nowT := ts(now())
	res, err := s.db.ExecContext(ctx, `UPDATE ai_lease_request SET status=$1, decision_reason=$2, decided_at=$3
		WHERE status=$4`, model.LeaseRequestRejected, reason, nowT, model.LeaseRequestPending)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RevokeLegacyUntokenizedLeases marks active leases without a bound access
// token revoked (used once at startup after the token migration).
func (s *Store) RevokeLegacyUntokenizedLeases(ctx context.Context, reason string) (int64, error) {
	nowT := ts(now())
	res, err := s.db.ExecContext(ctx, `UPDATE ai_lease SET status=$1, revoked_at=$2, revoke_reason=$3
		WHERE status=$4 AND (access_token_id IS NULL OR access_token_id='')`,
		model.LeaseRevoked, nowT, reason, model.LeaseActive)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ExpireLeasesBeforeAccessToken is a startup fallback sweep: it marks active
// leases expired once their own expires_at has passed, mirroring the scheduler
// so a restart cannot leave stale "active" rows. (Lease expiry is already
// capped at the token expiry at issue/renew time.)
func (s *Store) ExpireLeasesBeforeAccessToken(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE ai_lease SET status=$1 WHERE status=$2 AND access_token_id IS NOT NULL
		AND access_token_id <> '' AND expires_at <= $3`,
		model.LeaseExpired, model.LeaseActive, ts(now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// RevokeLeasesByTokenTx marks every active lease bound to a token revoked in a
// single transaction so a partial failure cannot leave leases half-revoked.
// Returns the number of leases revoked.
func (s *Store) RevokeLeasesByTokenTx(ctx context.Context, tokenID, reason string) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		nowT := ts(now())
		res, err := tsx.exec(ctx, `UPDATE ai_lease SET status=$1, revoked_at=$2, revoke_reason=$3
			WHERE access_token_id=$4 AND status=$5`,
			model.LeaseRevoked, nowT, reason, tokenID, model.LeaseActive)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// LeasesRevokedByToken returns leases for a token that were just revoked with
// the given reason (used to emit events/audit after the revoke transaction).
func (s *Store) LeasesRevokedByToken(ctx context.Context, tokenID, reason string) ([]*model.AILease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+leaseColumns+leaseFrom+`
		WHERE l.access_token_id=$1 AND l.status=$2 AND l.revoke_reason=$3
		ORDER BY l.revoked_at DESC`, tokenID, model.LeaseRevoked, reason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AILease{}
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateAccessTokenPermissions conditionally replaces a token's permission set
// under an optimistic lock: the row only changes when its current
// permission_version equals expectedRevision, and the revision is bumped by
// one. Returns true when exactly one row was updated, false when the revision
// no longer matches (caller maps that to a conflict). Never touches
// revoked_at/expires_at.
func (s *Store) UpdateAccessTokenPermissions(ctx context.Context, tokenID string, expectedRevision int, permissionsJSON string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE api_access_token SET permissions_json=$1, permission_version=permission_version+1
		WHERE id=$2 AND permission_version=$3 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $4)`,
		permissionsJSON, tokenID, expectedRevision, ts(now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
