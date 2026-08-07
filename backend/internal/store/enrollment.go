package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"servercli/internal/model"
)

const enrollmentColumns = `id, instance_request_id, environment_id, requested_role, hostname, instance_name, source_ip,
	reported_addresses_json, agent_version, os_name, os_version, arch, frontend_port, backend_port,
	status, reviewed_by, reviewed_at, review_note, claim_token_hash, claim_expires_at, claimed_at,
	instance_public_key, node_id, created_at`

func scanEnrollment(row interface{ Scan(...any) error }) (*model.NodeEnrollment, error) {
	var e model.NodeEnrollment
	var sourceIP, agentVer, osName, osVer, arch, reviewedBy, reviewNote, claimHash, instancePubKey, nodeID, created sql.NullString
	var reported, reviewedAt, claimExpires, claimedAt sql.NullString
	var frontendPort, backendPort int64
	if err := row.Scan(&e.ID, &e.InstanceRequestID, &e.EnvironmentID, &e.RequestedRole, &e.Hostname, &e.InstanceName, &sourceIP,
		&reported, &agentVer, &osName, &osVer, &arch, &frontendPort, &backendPort,
		&e.Status, &reviewedBy, &reviewedAt, &reviewNote, &claimHash, &claimExpires, &claimedAt,
		&instancePubKey, &nodeID, &created); err != nil {
		return nil, err
	}
	e.SourceIP = sourceIP.String
	e.AgentVersion = agentVer.String
	e.OSName = osName.String
	e.OSVersion = osVer.String
	e.Arch = arch.String
	e.ReviewedBy = reviewedBy.String
	e.ReviewNote = reviewNote.String
	e.ClaimTokenHash = claimHash.String
	e.InstancePublicKey = instancePubKey.String
	e.NodeID = nodeID.String
	e.ReportedAddressesJSON = reported.String
	e.FrontendPort = int(frontendPort)
	e.BackendPort = int(backendPort)
	var err error
	if e.ReviewedAt, err = parseTime(reviewedAt); err != nil {
		return nil, err
	}
	if e.ClaimExpiresAt, err = parseTime(claimExpires); err != nil {
		return nil, err
	}
	if e.ClaimedAt, err = parseTime(claimedAt); err != nil {
		return nil, err
	}
	if e.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &e, nil
}

// CreateEnrollment inserts a pending enrollment.
func (s *Store) CreateEnrollment(ctx context.Context, e *model.NodeEnrollment) error {
	e.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_enrollment
		(id, instance_request_id, environment_id, requested_role, hostname, instance_name, source_ip,
		 reported_addresses_json, agent_version, os_name, os_version, arch, frontend_port, backend_port,
		 status, reviewed_by, reviewed_at, review_note, claim_token_hash, claim_expires_at, claimed_at,
		 instance_public_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		e.ID, e.InstanceRequestID, e.EnvironmentID, e.RequestedRole, e.Hostname, e.InstanceName, e.SourceIP,
		e.ReportedAddressesJSON, e.AgentVersion, e.OSName, e.OSVersion, e.Arch, e.FrontendPort, e.BackendPort,
		e.Status, e.ReviewedBy, nullTime(e.ReviewedAt), e.ReviewNote, e.ClaimTokenHash, nullTime(e.ClaimExpiresAt), nullTime(e.ClaimedAt),
		e.InstancePublicKey, ts(e.CreatedAt))
	return err
}

// EnrollmentByID finds an enrollment.
func (s *Store) EnrollmentByID(ctx context.Context, id string) (*model.NodeEnrollment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+enrollmentColumns+` FROM node_enrollment WHERE id = $1`, id)
	e, err := scanEnrollment(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// EnrollmentByInstanceRequest finds an enrollment by idempotency key.
func (s *Store) EnrollmentByInstanceRequest(ctx context.Context, envID, instanceRequestID string) (*model.NodeEnrollment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+enrollmentColumns+` FROM node_enrollment
		WHERE environment_id = $1 AND instance_request_id = $2`, envID, instanceRequestID)
	e, err := scanEnrollment(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// EnrollmentByClaimTokenHash finds an enrollment with the given claim token.
func (s *Store) EnrollmentByClaimTokenHash(ctx context.Context, tokenHash string) (*model.NodeEnrollment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+enrollmentColumns+` FROM node_enrollment WHERE claim_token_hash = $1`, tokenHash)
	e, err := scanEnrollment(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// EnrollmentByNodeID finds the enrollment that created a node.
func (s *Store) EnrollmentByNodeID(ctx context.Context, nodeID string) (*model.NodeEnrollment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+enrollmentColumns+` FROM node_enrollment WHERE node_id = $1 ORDER BY created_at DESC LIMIT 1`, nodeID)
	e, err := scanEnrollment(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// ListEnrollments returns enrollments filtered by optional status, newest first.
func (s *Store) ListEnrollments(ctx context.Context, status string, limit, offset int) ([]*model.NodeEnrollment, error) {
	q := `SELECT ` + enrollmentColumns + ` FROM node_enrollment`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		q += ` LIMIT $` + strconv.Itoa(len(args)+1)
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
	out := []*model.NodeEnrollment{}
	for rows.Next() {
		e, err := scanEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateEnrollment persists mutable fields of an enrollment.
func (s *Store) UpdateEnrollment(ctx context.Context, e *model.NodeEnrollment) error {
	_, err := s.db.ExecContext(ctx, `UPDATE node_enrollment SET
		status=$1, reviewed_by=$2, reviewed_at=$3, review_note=$4,
		claim_token_hash=$5, claim_expires_at=$6, claimed_at=$7, node_id=$8
		WHERE id=$9`,
		e.Status, e.ReviewedBy, nullTime(e.ReviewedAt), e.ReviewNote,
		e.ClaimTokenHash, nullTime(e.ClaimExpiresAt), nullTime(e.ClaimedAt), e.NodeID, e.ID)
	return err
}

// ExpireEnrollments marks stale pending enrollments as expired.
func (s *Store) ExpireEnrollments(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE node_enrollment SET status = $1
		WHERE status = $2 AND created_at < $3`,
		model.EnrollmentExpired, model.EnrollmentPending, ts(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
