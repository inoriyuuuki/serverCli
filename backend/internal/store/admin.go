package store

import (
	"context"
	"database/sql"
	"time"

	"servercli/internal/model"
)

const adminColumns = `id, username, password_hash, password_changed_at, failed_login_count, locked_until, created_at, updated_at`

func scanAdmin(row interface{ Scan(...any) error }) (*model.AdminUser, error) {
	var a model.AdminUser
	var pwdChanged, locked, created, updated sql.NullString
	var failed int64
	if err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &pwdChanged, &failed, &locked, &created, &updated); err != nil {
		return nil, err
	}
	a.FailedLoginCount = int(failed)
	var err error
	if a.PasswordChangedAt, err = parseTime(pwdChanged); err != nil {
		return nil, err
	}
	if a.LockedUntil, err = parseTime(locked); err != nil {
		return nil, err
	}
	if a.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if a.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &a, nil
}

// AdminCount returns the number of admin users.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_user`).Scan(&n)
	return n, err
}

// AdminByUsername finds an admin by username.
func (s *Store) AdminByUsername(ctx context.Context, username string) (*model.AdminUser, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+adminColumns+` FROM admin_user WHERE username = $1`, username)
	a, err := scanAdmin(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return a, nil
}

// AdminByID finds an admin by id.
func (s *Store) AdminByID(ctx context.Context, id string) (*model.AdminUser, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+adminColumns+` FROM admin_user WHERE id = $1`, id)
	a, err := scanAdmin(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return a, nil
}

// CreateAdmin inserts a new admin user.
func (s *Store) CreateAdmin(ctx context.Context, a *model.AdminUser) error {
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_user
		(id, username, password_hash, password_changed_at, failed_login_count, locked_until, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.ID, a.Username, a.PasswordHash, nullTime(a.PasswordChangedAt), a.FailedLoginCount, nullTime(a.LockedUntil), ts(a.CreatedAt), ts(a.UpdatedAt))
	return err
}

// UpdateAdmin persists mutable admin fields.
func (s *Store) UpdateAdmin(ctx context.Context, a *model.AdminUser) error {
	a.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE admin_user SET
		password_hash=$1, password_changed_at=$2, failed_login_count=$3, locked_until=$4, updated_at=$5
		WHERE id=$6`,
		a.PasswordHash, nullTime(a.PasswordChangedAt), a.FailedLoginCount, nullTime(a.LockedUntil), ts(a.UpdatedAt), a.ID)
	return err
}

const sessionColumns = `id, admin_user_id, token_hash, csrf_secret_hash, ip_address, user_agent, expires_at, revoked_at, last_seen_at, created_at`

func scanSession(row interface{ Scan(...any) error }) (*model.AdminSession, error) {
	var s model.AdminSession
	var expires, created sql.NullString
	var revoked, lastSeen sql.NullString
	var ip, ua sql.NullString
	if err := row.Scan(&s.ID, &s.AdminUserID, &s.TokenHash, &s.CSRFSecretHash, &ip, &ua, &expires, &revoked, &lastSeen, &created); err != nil {
		return nil, err
	}
	s.IPAddress = ip.String
	s.UserAgent = ua.String
	var err error
	if s.ExpiresAt, err = parseTimeVal(expires); err != nil {
		return nil, err
	}
	if s.RevokedAt, err = parseTime(revoked); err != nil {
		return nil, err
	}
	if s.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return nil, err
	}
	if s.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSession inserts a session.
func (s *Store) CreateSession(ctx context.Context, sess *model.AdminSession) error {
	sess.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_session
		(id, admin_user_id, token_hash, csrf_secret_hash, ip_address, user_agent, expires_at, revoked_at, last_seen_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sess.ID, sess.AdminUserID, sess.TokenHash, sess.CSRFSecretHash, sess.IPAddress, sess.UserAgent,
		ts(sess.ExpiresAt), nullTime(sess.RevokedAt), nullTime(sess.LastSeenAt), ts(sess.CreatedAt))
	return err
}

// SessionByTokenHash finds a session by its token hash.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (*model.AdminSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM admin_session WHERE token_hash = $1`, tokenHash)
	sess, err := scanSession(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return sess, nil
}

// TouchSession updates last_seen, IP and user agent.
func (s *Store) TouchSession(ctx context.Context, id, ip, ua string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_session SET last_seen_at=$1, ip_address=$2, user_agent=$3 WHERE id=$4`,
		ts(now()), ip, ua, id)
	return err
}

// RevokeSession marks a session revoked.
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_session SET revoked_at=$1 WHERE id=$2 AND revoked_at IS NULL`, ts(now()), id)
	return err
}

// DeleteExpiredSessions removes sessions expired before the cutoff.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admin_session WHERE expires_at < $1 OR revoked_at IS NOT NULL`, ts(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
