package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const leaseRequestColumns = `lr.id, lr.client_request_id, lr.environment_id, lr.ai_agent_id, lr.ai_agent_name, lr.node_id,
	lr.requested_profile, lr.requested_duration_seconds, lr.public_key, lr.public_key_fingerprint, lr.purpose, lr.status,
	lr.decision_reason, lr.source_ip, lr.client_metadata_json, lr.created_at, lr.decided_at, lr.is_protected, lr.protected_at,
	lr.access_token_id, t.name, t.token_prefix`

const leaseRequestFrom = ` FROM ai_lease_request lr
	LEFT JOIN api_access_token t ON t.id = lr.access_token_id`

func scanLeaseRequest(row interface{ Scan(...any) error }) (*model.AILeaseRequest, error) {
	var r model.AILeaseRequest
	var agentID, agentName, fp, purpose, decisionReason, sourceIP, meta, created, decided, protected, tokenID, tokenName, tokenPrefix sql.NullString
	var duration int64
	var prot int64
	if err := row.Scan(&r.ID, &r.ClientRequestID, &r.EnvironmentID, &agentID, &agentName, &r.NodeID,
		&r.RequestedProfile, &duration, &r.PublicKey, &fp, &purpose, &r.Status,
		&decisionReason, &sourceIP, &meta, &created, &decided, &prot, &protected,
		&tokenID, &tokenName, &tokenPrefix); err != nil {
		return nil, err
	}
	r.AIAgentID = agentID.String
	r.AIAgentName = agentName.String
	r.AccessTokenID = tokenID.String
	r.AccessTokenName = tokenName.String
	r.AccessTokenPrefix = tokenPrefix.String
	r.PublicKeyFingerprint = fp.String
	r.Purpose = purpose.String
	r.DecisionReason = decisionReason.String
	r.SourceIP = sourceIP.String
	r.ClientMetadataJSON = meta.String
	r.RequestedDurationSeconds = int(duration)
	r.IsProtected = parseBool(prot)
	var err error
	if r.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if r.DecidedAt, err = parseTime(decided); err != nil {
		return nil, err
	}
	if r.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateLeaseRequest inserts a lease request.
func (s *Store) CreateLeaseRequest(ctx context.Context, r *model.AILeaseRequest) error {
	r.CreatedAt = now()
	if r.Status == "" {
		r.Status = model.LeaseRequestPending
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_lease_request
		(id, client_request_id, environment_id, ai_agent_id, ai_agent_name, node_id,
		 requested_profile, requested_duration_seconds, public_key, public_key_fingerprint, purpose, status,
		 decision_reason, source_ip, client_metadata_json, created_at, decided_at, is_protected, protected_at, access_token_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		r.ID, r.ClientRequestID, r.EnvironmentID, r.AIAgentID, r.AIAgentName, r.NodeID,
		r.RequestedProfile, r.RequestedDurationSeconds, r.PublicKey, r.PublicKeyFingerprint, r.Purpose, r.Status,
		r.DecisionReason, r.SourceIP, r.ClientMetadataJSON, ts(r.CreatedAt), nullTime(r.DecidedAt), boolInt(r.IsProtected), nullTime(r.ProtectedAt),
		nullString(r.AccessTokenID))
	return err
}

// LeaseRequestByID finds a lease request.
func (s *Store) LeaseRequestByID(ctx context.Context, id string) (*model.AILeaseRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseRequestColumns+leaseRequestFrom+` WHERE lr.id=$1`, id)
	r, err := scanLeaseRequest(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// LeaseRequestByIdempotency finds by (environment_id, client_request_id).
func (s *Store) LeaseRequestByIdempotency(ctx context.Context, envID, clientRequestID string) (*model.AILeaseRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseRequestColumns+leaseRequestFrom+`
		WHERE lr.environment_id=$1 AND lr.client_request_id=$2`, envID, clientRequestID)
	r, err := scanLeaseRequest(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// UpdateLeaseRequest persists status fields.
func (s *Store) UpdateLeaseRequest(ctx context.Context, r *model.AILeaseRequest) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_lease_request SET
		status=$1, decision_reason=$2, decided_at=$3, is_protected=$4, protected_at=$5
		WHERE id=$6`,
		r.Status, r.DecisionReason, nullTime(r.DecidedAt), boolInt(r.IsProtected), nullTime(r.ProtectedAt), r.ID)
	return err
}

// ListLeaseRequests returns requests with filters, newest first.
func (s *Store) ListLeaseRequests(ctx context.Context, nodeID, status string, limit, offset int) ([]*model.AILeaseRequest, error) {
	q := `SELECT ` + leaseRequestColumns + leaseRequestFrom
	conds := []string{}
	args := []any{}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `lr.node_id=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `lr.status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY lr.created_at DESC`
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
	out := []*model.AILeaseRequest{}
	for rows.Next() {
		r, err := scanLeaseRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const leaseColumns = `l.id, l.request_id, l.node_id, l.ai_agent_id, l.permission_profile, l.public_key, l.public_key_fingerprint,
	l.issued_at, l.expires_at, l.absolute_expires_at, l.last_renewed_at, l.renew_count, l.status, l.revoked_at, l.revoke_reason,
	l.renewal_disabled, l.renewal_token_hash, l.renewal_token_prefix, l.active_session_count, l.last_heartbeat_at,
	l.key_installed, l.key_installed_at, l.is_protected, l.protected_at, l.access_token_id, t.name, t.token_prefix`

const leaseFrom = ` FROM ai_lease l
	LEFT JOIN api_access_token t ON t.id = l.access_token_id`

func scanLease(row interface{ Scan(...any) error }) (*model.AILease, error) {
	var l model.AILease
	var requestID, agentID, fp, issued, expires, absExpires, lastRenewed, revoked, reason, lastHeartbeatAt, keyInstalledAt, protected, tokenID, tokenName, tokenPrefix sql.NullString
	var renewCount, activeSessions, prot int64
	var renewalDisabled, keyInstalled int64
	if err := row.Scan(&l.ID, &requestID, &l.NodeID, &agentID, &l.PermissionProfile, &l.PublicKey, &fp,
		&issued, &expires, &absExpires, &lastRenewed, &renewCount, &l.Status, &revoked, &reason,
		&renewalDisabled, &l.RenewalTokenHash, &l.RenewalTokenPrefix, &activeSessions, &lastHeartbeatAt,
		&keyInstalled, &keyInstalledAt, &prot, &protected, &tokenID, &tokenName, &tokenPrefix); err != nil {
		return nil, err
	}
	l.RequestID = requestID.String
	l.AIAgentID = agentID.String
	l.AccessTokenID = tokenID.String
	l.AccessTokenName = tokenName.String
	l.AccessTokenPrefix = tokenPrefix.String
	l.PublicKeyFingerprint = fp.String
	l.RevokeReason = reason.String
	l.RenewCount = int(renewCount)
	l.ActiveSessionCount = int(activeSessions)
	l.RenewalDisabled = parseBool(renewalDisabled)
	l.KeyInstalled = parseBool(keyInstalled)
	l.IsProtected = parseBool(prot)
	var err error
	if l.IssuedAt, err = parseTimeVal(issued); err != nil {
		return nil, err
	}
	if l.ExpiresAt, err = parseTimeVal(expires); err != nil {
		return nil, err
	}
	if l.AbsoluteExpiresAt, err = parseTimeVal(absExpires); err != nil {
		return nil, err
	}
	if l.LastRenewedAt, err = parseTime(lastRenewed); err != nil {
		return nil, err
	}
	if l.LastHeartbeatAt, err = parseTime(lastHeartbeatAt); err != nil {
		return nil, err
	}
	if l.RevokedAt, err = parseTime(revoked); err != nil {
		return nil, err
	}
	if l.KeyInstalledAt, err = parseTime(keyInstalledAt); err != nil {
		return nil, err
	}
	if l.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &l, nil
}

// CreateLease inserts a lease.
func (s *Store) CreateLease(ctx context.Context, l *model.AILease) error {
	l.Status = model.LeaseActive
	l.IssuedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_lease
		(id, request_id, node_id, ai_agent_id, permission_profile, public_key, public_key_fingerprint,
		 issued_at, expires_at, absolute_expires_at, last_renewed_at, renew_count, status, revoked_at, revoke_reason,
		 renewal_disabled, renewal_token_hash, renewal_token_prefix, active_session_count, last_heartbeat_at,
		 key_installed, key_installed_at, is_protected, protected_at, access_token_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
		l.ID, l.RequestID, l.NodeID, l.AIAgentID, l.PermissionProfile, l.PublicKey, l.PublicKeyFingerprint,
		ts(l.IssuedAt), ts(l.ExpiresAt), ts(l.AbsoluteExpiresAt), nullTime(l.LastRenewedAt), l.RenewCount, l.Status, nullTime(l.RevokedAt), l.RevokeReason,
		boolInt(l.RenewalDisabled), l.RenewalTokenHash, l.RenewalTokenPrefix, l.ActiveSessionCount, nullTime(l.LastHeartbeatAt),
		boolInt(l.KeyInstalled), nullTime(l.KeyInstalledAt), boolInt(l.IsProtected), nullTime(l.ProtectedAt),
		nullString(l.AccessTokenID))
	return err
}

// LeaseByID finds a lease.
func (s *Store) LeaseByID(ctx context.Context, id string) (*model.AILease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseColumns+leaseFrom+` WHERE l.id=$1`, id)
	l, err := scanLease(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return l, nil
}

// LeaseByRequestID finds the lease created for a request.
func (s *Store) LeaseByRequestID(ctx context.Context, requestID string) (*model.AILease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseColumns+leaseFrom+` WHERE l.request_id=$1 ORDER BY l.issued_at DESC LIMIT 1`, requestID)
	l, err := scanLease(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return l, nil
}

// UpdateLease persists mutable lease fields.
func (s *Store) UpdateLease(ctx context.Context, l *model.AILease) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_lease SET
		expires_at=$1, last_renewed_at=$2, renew_count=$3, status=$4, revoked_at=$5, revoke_reason=$6,
		renewal_disabled=$7, active_session_count=$8, last_heartbeat_at=$9, key_installed=$10, key_installed_at=$11,
		is_protected=$12, protected_at=$13
		WHERE id=$14`,
		ts(l.ExpiresAt), nullTime(l.LastRenewedAt), l.RenewCount, l.Status, nullTime(l.RevokedAt), l.RevokeReason,
		boolInt(l.RenewalDisabled), l.ActiveSessionCount, nullTime(l.LastHeartbeatAt), boolInt(l.KeyInstalled), nullTime(l.KeyInstalledAt),
		boolInt(l.IsProtected), nullTime(l.ProtectedAt), l.ID)
	return err
}

// ListLeases returns leases with filters, newest first.
func (s *Store) ListLeases(ctx context.Context, nodeID, status string, limit, offset int) ([]*model.AILease, error) {
	q := `SELECT ` + leaseColumns + leaseFrom
	conds := []string{}
	args := []any{}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `l.node_id=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `l.status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY issued_at DESC`
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

// ActiveLeasesOnNode returns active leases for a node.
func (s *Store) ActiveLeasesOnNode(ctx context.Context, nodeID string) ([]*model.AILease, error) {
	return s.ListLeases(ctx, nodeID, model.LeaseActive, 0, 0)
}

// ExpiredLeases returns active leases whose expires_at is before cutoff.
func (s *Store) ExpiredLeases(ctx context.Context, before time.Time) ([]*model.AILease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+leaseColumns+leaseFrom+` WHERE l.status=$1 AND l.expires_at < $2`,
		model.LeaseActive, ts(before))
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

// ExpireLease marks a lease expired.
func (s *Store) ExpireLease(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE ai_lease SET status=$1 WHERE id=$2 AND status=$3`,
		model.LeaseExpired, id, model.LeaseActive)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStateTransition
	}
	return nil
}

// AppendLeaseEvent inserts a lease event.
func (s *Store) AppendLeaseEvent(ctx context.Context, e *model.AILeaseEvent) error {
	e.ID = model.NewUUID()
	e.OccurredAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_lease_event (id, lease_id, event_type, actor_type, actor_id, details_json, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.ID, e.LeaseID, e.EventType, e.ActorType, e.ActorID, e.DetailsJSON, ts(e.OccurredAt))
	return err
}

// LeaseEvents lists events for a lease.
func (s *Store) LeaseEvents(ctx context.Context, leaseID string) ([]*model.AILeaseEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, lease_id, event_type, actor_type, actor_id, details_json, occurred_at
		FROM ai_lease_event WHERE lease_id=$1 ORDER BY occurred_at, id`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AILeaseEvent{}
	for rows.Next() {
		var e model.AILeaseEvent
		var actorType, actorID, details sql.NullString
		var occ sql.NullString
		if err := rows.Scan(&e.ID, &e.LeaseID, &e.EventType, &actorType, &actorID, &details, &occ); err != nil {
			return nil, err
		}
		e.ActorType = actorType.String
		e.ActorID = actorID.String
		e.DetailsJSON = details.String
		if e.OccurredAt, err = parseTimeVal(occ); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- ai_ssh_session ----

// CreateSSHSession inserts an SSH session record.
func (s *Store) CreateSSHSession(ctx context.Context, sess *model.AISSHSession) error {
	sess.ID = model.NewUUID()
	sess.StartedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_ssh_session
		(id, lease_id, node_id, remote_address, connection_id, os_pid, cgroup_id, started_at, last_seen_at,
		 ended_at, end_reason, exit_code, command_count, recording_ref, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		sess.ID, sess.LeaseID, sess.NodeID, sess.RemoteAddress, sess.ConnectionID, sess.OSPid, sess.CgroupID, ts(sess.StartedAt),
		nullTime(sess.LastSeenAt), nullTime(sess.EndedAt), sess.EndReason, sess.ExitCode, sess.CommandCount, sess.RecordingRef,
		boolInt(sess.IsProtected), nullTime(sess.ProtectedAt))
	return err
}

// EndSSHSession marks a session ended.
func (s *Store) EndSSHSession(ctx context.Context, id string, endedAt time.Time, reason string, exitCode *int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_ssh_session SET ended_at=$1, end_reason=$2, exit_code=$3 WHERE id=$4 AND ended_at IS NULL`,
		ts(endedAt), reason, exitCode, id)
	return err
}

// SSHSessionsForLease lists sessions for a lease.
func (s *Store) SSHSessionsForLease(ctx context.Context, leaseID string) ([]*model.AISSHSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, lease_id, node_id, remote_address, connection_id, os_pid, cgroup_id,
		started_at, last_seen_at, ended_at, end_reason, exit_code, command_count, recording_ref, is_protected, protected_at
		FROM ai_ssh_session WHERE lease_id=$1 ORDER BY started_at`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AISSHSession{}
	for rows.Next() {
		sess, err := scanSSHSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func scanSSHSession(row interface{ Scan(...any) error }) (*model.AISSHSession, error) {
	var s model.AISSHSession
	var remote, connID, cgroup, started, lastSeen, ended, endReason, recording, protected sql.NullString
	var pid sql.NullInt64
	var exitCode sql.NullInt64
	var commandCount, prot int64
	if err := row.Scan(&s.ID, &s.LeaseID, &s.NodeID, &remote, &connID, &pid, &cgroup,
		&started, &lastSeen, &ended, &endReason, &exitCode, &commandCount, &recording, &prot, &protected); err != nil {
		return nil, err
	}
	s.RemoteAddress = remote.String
	s.ConnectionID = connID.String
	s.CgroupID = cgroup.String
	s.EndReason = endReason.String
	s.RecordingRef = recording.String
	s.OSPid = pid.Int64
	s.CommandCount = int(commandCount)
	s.IsProtected = parseBool(prot)
	if exitCode.Valid {
		ec := int(exitCode.Int64)
		s.ExitCode = &ec
	}
	var err error
	if s.StartedAt, err = parseTimeVal(started); err != nil {
		return nil, err
	}
	if s.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return nil, err
	}
	if s.EndedAt, err = parseTime(ended); err != nil {
		return nil, err
	}
	if s.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &s, nil
}
