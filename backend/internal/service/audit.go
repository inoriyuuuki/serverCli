package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"servercli/internal/model"
	"servercli/internal/secret"
	"servercli/internal/store"
)

// Risk levels.
const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

// Result values for audit events.
const (
	ResultSuccess = "success"
	ResultDenied  = "denied"
	ResultFailure = "failure"
)

// AuditInput describes an audit event to record.
type AuditInput struct {
	NodeID       string
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	RequestID    string
	TaskID       string
	LeaseID      string
	SessionID    string
	SourceIP     string
	Summary      string
	Details      map[string]any
	RiskLevel    string
	IsProtected  bool
	ProtectedBy  string
}

// Auditor writes audit events with secret redaction.
type Auditor struct {
	store    *store.Store
	log      *slog.Logger
	redactor *secret.Redactor
	envID    string
	instance string
}

// NewAuditor builds an Auditor.
func NewAuditor(st *store.Store, log *slog.Logger, envID, instance string) *Auditor {
	return &Auditor{store: st, log: log, redactor: secret.NewRedactor(), envID: envID, instance: instance}
}

// Redactor exposes the shared redactor (for redaction counts in responses).
func (a *Auditor) Redactor() *secret.Redactor { return a.redactor }

// Record persists an audit event after redaction.
func (a *Auditor) Record(ctx context.Context, in AuditInput) error {
	risk := in.RiskLevel
	if risk == "" {
		risk = RiskLow
	}
	result := in.Result
	if result == "" {
		result = ResultSuccess
	}
	details := ""
	if in.Details != nil {
		raw, err := json.Marshal(in.Details)
		if err == nil {
			details = string(a.redactor.RedactJSON(raw))
		}
	}
	ev := &model.AuditEvent{
		EnvironmentID: a.envID,
		NodeID:        in.NodeID,
		ActorType:     in.ActorType,
		ActorID:       in.ActorID,
		Action:        in.Action,
		ResourceType:  in.ResourceType,
		ResourceID:    in.ResourceID,
		Result:        result,
		RequestID:     in.RequestID,
		TaskID:        in.TaskID,
		LeaseID:       in.LeaseID,
		SessionID:     in.SessionID,
		SourceIP:      in.SourceIP,
		Summary:       a.redactor.RedactString(in.Summary),
		DetailsJSON:   details,
		RiskLevel:     risk,
		IsProtected:   in.IsProtected,
		ProtectedBy:   in.ProtectedBy,
	}
	if err := a.store.CreateAuditEvent(ctx, ev); err != nil {
		a.log.Error("audit write failed", "error", err, "action", in.Action)
		return err
	}
	return nil
}

// OK is a convenience for successful events.
func (a *Auditor) OK(ctx context.Context, in AuditInput) error {
	in.Result = ResultSuccess
	return a.Record(ctx, in)
}

// Denied is a convenience for denied events.
func (a *Auditor) Denied(ctx context.Context, in AuditInput) error {
	in.Result = ResultDenied
	if in.RiskLevel == "" {
		in.RiskLevel = RiskMedium
	}
	return a.Record(ctx, in)
}

// Failure is a convenience for failed events.
func (a *Auditor) Failure(ctx context.Context, in AuditInput) error {
	in.Result = ResultFailure
	return a.Record(ctx, in)
}
