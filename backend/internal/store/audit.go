package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const auditColumns = `id, occurred_at, environment_id, node_id, actor_type, actor_id, action,
	resource_type, resource_id, result, request_id, task_id, lease_id, session_id, source_ip,
	summary, details_json, risk_level, is_protected, protected_at, protected_by`

func scanAudit(row interface{ Scan(...any) error }) (*model.AuditEvent, error) {
	var e model.AuditEvent
	var envID, nodeID, actorID, resType, resID, requestID, taskID, leaseID, sessionID, sourceIP, summary, details, protectedBy sql.NullString
	var occurred, protected sql.NullString
	var prot int64
	if err := row.Scan(&e.ID, &occurred, &envID, &nodeID, &e.ActorType, &actorID, &e.Action,
		&resType, &resID, &e.Result, &requestID, &taskID, &leaseID, &sessionID, &sourceIP,
		&summary, &details, &e.RiskLevel, &prot, &protected, &protectedBy); err != nil {
		return nil, err
	}
	e.EnvironmentID = envID.String
	e.NodeID = nodeID.String
	e.ActorID = actorID.String
	e.ResourceType = resType.String
	e.ResourceID = resID.String
	e.RequestID = requestID.String
	e.TaskID = taskID.String
	e.LeaseID = leaseID.String
	e.SessionID = sessionID.String
	e.SourceIP = sourceIP.String
	e.Summary = summary.String
	e.DetailsJSON = details.String
	e.ProtectedBy = protectedBy.String
	e.IsProtected = parseBool(prot)
	var err error
	if e.OccurredAt, err = parseTimeVal(occurred); err != nil {
		return nil, err
	}
	if e.ProtectedAt, err = parseTime(protected); err != nil {
		return nil, err
	}
	return &e, nil
}

// AuditFilter describes audit list filters.
type AuditFilter struct {
	Since        *time.Time
	Until        *time.Time
	NodeID       string
	ActorType    string
	ActorID      string
	Action       string
	Result       string
	RiskLevel    string
	IsProtected  *bool
	ResourceType string
	ResourceID   string
	TaskID       string
	LeaseID      string
	SessionID    string
	RequestID    string
	Limit        int
	Offset       int
}

// CreateAuditEvent inserts an audit event.
func (s *Store) CreateAuditEvent(ctx context.Context, e *model.AuditEvent) error {
	e.ID = model.NewUUID()
	e.OccurredAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_event
		(id, occurred_at, environment_id, node_id, actor_type, actor_id, action,
		 resource_type, resource_id, result, request_id, task_id, lease_id, session_id, source_ip,
		 summary, details_json, risk_level, is_protected, protected_at, protected_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		e.ID, ts(e.OccurredAt), e.EnvironmentID, e.NodeID, e.ActorType, e.ActorID, e.Action,
		e.ResourceType, e.ResourceID, e.Result, e.RequestID, e.TaskID, e.LeaseID, e.SessionID, e.SourceIP,
		e.Summary, e.DetailsJSON, e.RiskLevel, boolInt(e.IsProtected), nullTime(e.ProtectedAt), e.ProtectedBy)
	return err
}

// ListAuditEvents returns audit events matching the filter, newest first.
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]*model.AuditEvent, error) {
	q := `SELECT ` + auditColumns + ` FROM audit_event`
	conds := []string{}
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return `$` + strconv.Itoa(len(args))
	}
	if f.Since != nil {
		conds = append(conds, `occurred_at >= `+add(ts(*f.Since)))
	}
	if f.Until != nil {
		conds = append(conds, `occurred_at <= `+add(ts(*f.Until)))
	}
	if f.NodeID != "" {
		conds = append(conds, `node_id=`+add(f.NodeID))
	}
	if f.ActorType != "" {
		conds = append(conds, `actor_type=`+add(f.ActorType))
	}
	if f.ActorID != "" {
		conds = append(conds, `actor_id=`+add(f.ActorID))
	}
	if f.Action != "" {
		conds = append(conds, `action=`+add(f.Action))
	}
	if f.Result != "" {
		conds = append(conds, `result=`+add(f.Result))
	}
	if f.RiskLevel != "" {
		conds = append(conds, `risk_level=`+add(f.RiskLevel))
	}
	if f.IsProtected != nil {
		conds = append(conds, `is_protected=`+add(boolInt(*f.IsProtected)))
	}
	if f.ResourceType != "" {
		conds = append(conds, `resource_type=`+add(f.ResourceType))
	}
	if f.ResourceID != "" {
		conds = append(conds, `resource_id=`+add(f.ResourceID))
	}
	if f.TaskID != "" {
		conds = append(conds, `task_id=`+add(f.TaskID))
	}
	if f.LeaseID != "" {
		conds = append(conds, `lease_id=`+add(f.LeaseID))
	}
	if f.SessionID != "" {
		conds = append(conds, `session_id=`+add(f.SessionID))
	}
	if f.RequestID != "" {
		conds = append(conds, `request_id=`+add(f.RequestID))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY occurred_at DESC, id DESC`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	if f.Offset > 0 {
		args = append(args, f.Offset)
		q += ` OFFSET $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.AuditEvent{}
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ProtectAudit marks audit events matching resource ids as protected.
func (s *Store) ProtectAudit(ctx context.Context, ids []string, by string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE audit_event SET is_protected=1, protected_at=$1, protected_by=$2 WHERE id=$3`,
			ts(now()), by, id); err != nil {
			return err
		}
	}
	return nil
}
