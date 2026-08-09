package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const leaseRequestColumns = `id, client_request_id, environment_id, ai_agent_id, ai_agent_name, node_id,
	requested_profile, requested_duration_seconds, public_key, public_key_fingerprint, purpose, status,
	decision_reason, source_ip, client_metadata_json, created_at, decided_at, is_protected, protected_at`

func scanLeaseRequest(row interface{ Scan(...any) error }) (*model.AILeaseRequest, error) {
	var r model.AILeaseRequest
	var agentID, agentName, fp, purpose, decisionReason, sourceIP, meta, created, decided, protected sql.NullString
	var duration int64
	var prot int64
	if err := row.Scan(&r.ID, &r.ClientRequestID, &r.EnvironmentID, &agentID, &agentName, &r.NodeID,
		&r.RequestedProfile, &duration, &r.PublicKey, &fp, &purpose, &r.Status,
		&decisionReason, &sourceIP, &meta, &created, &decided, &prot, &protected); err != nil {
		return nil, err
	}
	r.AIAgentID = agentID.String
	r.AIAgentName = agentName.String
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
	r.Status = model.LeaseRequestPending
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_lease_request
		(id, client_request_id, environment_id, ai_agent_id, ai_agent_name, node_id,
		 requested_profile, requested_duration_seconds, public_key, public_key_fingerprint, purpose, status,
		 decision_reason, source_ip, client_metadata_json, created_at, decided_at, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		r.ID, r.ClientRequestID, r.EnvironmentID, r.AIAgentID, r.AIAgentName, r.NodeID,
		r.RequestedProfile, r.RequestedDurationSeconds, r.PublicKey, r.PublicKeyFingerprint, r.Purpose, r.Status,
		r.DecisionReason, r.SourceIP, r.ClientMetadataJSON, ts(r.CreatedAt), nullTime(r.DecidedAt), boolInt(r.IsProtected), nullTime(r.ProtectedAt))
	return err
}

// LeaseRequestByID finds a lease request.
func (s *Store) LeaseRequestByID(ctx context.Context, id string) (*model.AILeaseRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseRequestColumns+` FROM ai_lease_request WHERE id=$1`, id)
	r, err := scanLeaseRequest(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// LeaseRequestByIdempotency finds by (environment_id, client_request_id).
func (s *Store) LeaseRequestByIdempotency(ctx context.Context, envID, clientRequestID string) (*model.AILeaseRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseRequestColumns+` FROM ai_lease_request
		WHERE environment_id=$1 AND client_request_id=$2`, envID, clientRequestID)
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

// ApproveLeaseRequestIfPending persists an approval only while the request is
// still pending; it returns ErrStateTransition otherwise. This guards against
// concurrent double-approval issuing two leases for one request.
func (s *Store) ApproveLeaseRequestIfPending(ctx context.Context, r *model.AILeaseRequest) error {
	res, err := s.db.ExecContext(ctx, `UPDATE ai_lease_request SET
		status=$1, decision_reason=$2, decided_at=$3 WHERE id=$4 AND status=$5`,
		r.Status, r.DecisionReason, nullTime(r.DecidedAt), r.ID, model.LeaseRequestPending)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStateTransition
	}
	return nil
}

// ListLeaseRequests returns requests with filters, newest first.
func (s *Store) ListLeaseRequests(ctx context.Context, nodeID, status string, limit, offset int) ([]*model.AILeaseRequest, error) {
	q := `SELECT ` + leaseRequestColumns + ` FROM ai_lease_request`
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
	q += ` ORDER BY created_at DESC`
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

const leaseColumns = `id, request_id, node_id, ai_agent_id, permission_profile, public_key, public_key_fingerprint,
	issued_at, expires_at, absolute_expires_at, last_renewed_at, renew_count, status, revoked_at, revoke_reason,
	renewal_disabled, renewal_token_hash, renewal_token_prefix, active_session_count, last_heartbeat_at,
	key_installed, key_installed_at, is_protected, protected_at`

func scanLease(row interface{ Scan(...any) error }) (*model.AILease, error) {
	var l model.AILease
	var requestID, agentID, fp, issued, expires, absExpires, lastRenewed, revoked, reason, lastHeartbeatAt, keyInstalledAt, protected sql.NullString
	var renewCount, activeSessions, prot int64
	var renewalDisabled, keyInstalled int64
	if err := row.Scan(&l.ID, &requestID, &l.NodeID, &agentID, &l.PermissionProfile, &l.PublicKey, &fp,
		&issued, &expires, &absExpires, &lastRenewed, &renewCount, &l.Status, &revoked, &reason,
		&renewalDisabled, &l.RenewalTokenHash, &l.RenewalTokenPrefix, &activeSessions, &lastHeartbeatAt,
		&keyInstalled, &keyInstalledAt, &prot, &protected); err != nil {
		return nil, err
	}
	l.RequestID = requestID.String
	l.AIAgentID = agentID.String
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
		 key_installed, key_installed_at, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		l.ID, l.RequestID, l.NodeID, l.AIAgentID, l.PermissionProfile, l.PublicKey, l.PublicKeyFingerprint,
		ts(l.IssuedAt), ts(l.ExpiresAt), ts(l.AbsoluteExpiresAt), nullTime(l.LastRenewedAt), l.RenewCount, l.Status, nullTime(l.RevokedAt), l.RevokeReason,
		boolInt(l.RenewalDisabled), l.RenewalTokenHash, l.RenewalTokenPrefix, l.ActiveSessionCount, nullTime(l.LastHeartbeatAt),
		boolInt(l.KeyInstalled), nullTime(l.KeyInstalledAt), boolInt(l.IsProtected), nullTime(l.ProtectedAt))
	return err
}

// LeaseByID finds a lease.
func (s *Store) LeaseByID(ctx context.Context, id string) (*model.AILease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM ai_lease WHERE id=$1`, id)
	l, err := scanLease(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return l, nil
}

// LeaseByRequestID finds the lease created for a request.
func (s *Store) LeaseByRequestID(ctx context.Context, requestID string) (*model.AILease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM ai_lease WHERE request_id=$1 ORDER BY issued_at DESC LIMIT 1`, requestID)
	l, err := scanLease(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return l, nil
}

// LeaseByRenewalTokenHash finds a lease by renewal token hash.
func (s *Store) LeaseByRenewalTokenHash(ctx context.Context, hash string) (*model.AILease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM ai_lease WHERE renewal_token_hash=$1`, hash)
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
	q := `SELECT ` + leaseColumns + ` FROM ai_lease`
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+leaseColumns+` FROM ai_lease WHERE status=$1 AND expires_at < $2`,
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

// ApproveRequestWithAutoApproval atomically upserts an auto-approval rule,
// marks the lease request approved (guarded on pending), and creates the
// lease in a single transaction so a partial failure cannot leave
// rule/request/lease out of sync, and a concurrent double-approval cannot
// issue two leases for one request. The caller fills in IDs and timestamps;
// created timestamps are set here for the rule and lease.
func (s *Store) ApproveRequestWithAutoApproval(ctx context.Context, a *model.AIAutoApproval, req *model.AILeaseRequest, lease *model.AILease) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		nowT := now()

		// Upsert auto-approval rule (keep existing id on update).
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
		if err := tsx.queryRow(ctx, `SELECT id FROM ai_auto_approval
			WHERE environment_id=$1 AND ai_agent_id=$2 AND node_id=$3`,
			a.EnvironmentID, a.AIAgentID, a.NodeID).Scan(&a.ID); err != nil {
			return err
		}

		// Approve the request, only while it is still pending. A concurrent
		// approval (admin or AI replay) loses the race and rolls back.
		res, err := tsx.exec(ctx, `UPDATE ai_lease_request SET
			status=$1, decision_reason=$2, decided_at=$3 WHERE id=$4 AND status=$5`,
			req.Status, req.DecisionReason, nullTime(req.DecidedAt), req.ID, model.LeaseRequestPending)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrStateTransition
		}

		// Create the lease.
		lease.ID = model.NewUUID()
		lease.IssuedAt = nowT
		lease.Status = model.LeaseActive
		_, err = tsx.exec(ctx, `INSERT INTO ai_lease
			(id, request_id, node_id, ai_agent_id, permission_profile, public_key, public_key_fingerprint,
			 issued_at, expires_at, absolute_expires_at, last_renewed_at, renew_count, status, revoked_at, revoke_reason,
			 renewal_disabled, renewal_token_hash, renewal_token_prefix, active_session_count, last_heartbeat_at,
			 key_installed, key_installed_at, is_protected, protected_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
			lease.ID, lease.RequestID, lease.NodeID, lease.AIAgentID, lease.PermissionProfile, lease.PublicKey, lease.PublicKeyFingerprint,
			ts(lease.IssuedAt), ts(lease.ExpiresAt), ts(lease.AbsoluteExpiresAt), nullTime(lease.LastRenewedAt), lease.RenewCount, lease.Status, nullTime(lease.RevokedAt), lease.RevokeReason,
			boolInt(lease.RenewalDisabled), lease.RenewalTokenHash, lease.RenewalTokenPrefix, lease.ActiveSessionCount, nullTime(lease.LastHeartbeatAt),
			boolInt(lease.KeyInstalled), nullTime(lease.KeyInstalledAt), boolInt(lease.IsProtected), nullTime(lease.ProtectedAt))
		return err
	})
}
