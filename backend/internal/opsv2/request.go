// Package opsv2 implements the structured Operation V2 contract shared by the
// control plane and agents.
package opsv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"servercli/internal/model"
)

// OperationRequest is the order-independent, structured input to an Operation
// V2 request. Every JSON property is required on the wire; fields whose values
// are not applicable to a particular operation are represented by an empty
// string or, for deadline, null.
type OperationRequest struct {
	OperationID       string          `json:"operation_id"`
	OperationType     string          `json:"operation_type"`
	ClusterID         string          `json:"cluster_id"`
	NodeID            string          `json:"node_id"`
	ModuleID          string          `json:"module_id"`
	ServiceInstanceID string          `json:"service_instance_id"`
	DesiredRevision   string          `json:"desired_revision"`
	Arguments         json.RawMessage `json:"arguments"`
	Approval          string          `json:"approval"`
	RiskLevel         string          `json:"risk_level"`
	IdempotencyKey    string          `json:"idempotency_key"`
	Deadline          *time.Time      `json:"deadline"`
	PrimaryEpoch      int64           `json:"primary_epoch"`
}

var requiredRequestFields = [...]string{
	"operation_id",
	"operation_type",
	"cluster_id",
	"node_id",
	"module_id",
	"service_instance_id",
	"desired_revision",
	"arguments",
	"approval",
	"risk_level",
	"idempotency_key",
	"deadline",
	"primary_epoch",
}

var validOperationTypes = map[string]struct{}{
	model.OpTypeInit:            {},
	model.OpTypeUpdate:          {},
	model.OpTypeBackup:          {},
	model.OpTypeRestore:         {},
	model.OpTypeAdopt:           {},
	model.OpTypeRollback:        {},
	model.OpTypeVerify:          {},
	model.OpTypeProvision:       {},
	model.OpTypePrimaryTransfer: {},
}

var validRiskLevels = map[string]struct{}{
	model.RiskLow:      {},
	model.RiskMedium:   {},
	model.RiskHigh:     {},
	model.RiskCritical: {},
}

var validApprovals = map[string]struct{}{
	"pending":  {},
	"approved": {},
	"rejected": {},
	"auto":     {},
}

// ParseOperationRequest parses a strict Operation V2 request. JSON property
// order is immaterial, but unknown properties, omitted properties, trailing
// JSON values, and invalid field types are rejected.
func ParseOperationRequest(data []byte) (*OperationRequest, error) {
	var fields map[string]json.RawMessage
	if err := decodeOneJSON(data, &fields, true); err != nil {
		return nil, fmt.Errorf("parse operation request: %w", err)
	}
	if fields == nil {
		return nil, errors.New("parse operation request: expected JSON object")
	}

	for _, name := range requiredRequestFields {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("parse operation request: missing required field %q", name)
		}
	}

	var req OperationRequest
	if err := decodeOneJSON(data, &req, true); err != nil {
		return nil, fmt.Errorf("parse operation request: %w", err)
	}
	return &req, nil
}

func decodeOneJSON(data []byte, dst any, disallowUnknown bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// Validate validates request values after structural JSON validation.
func (r *OperationRequest) Validate() error {
	if r == nil {
		return errors.New("operation request is nil")
	}
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if _, ok := validOperationTypes[r.OperationType]; !ok {
		return fmt.Errorf("invalid operation_type %q", r.OperationType)
	}
	if strings.TrimSpace(r.ClusterID) == "" {
		return errors.New("cluster_id is required")
	}
	if !IsClusterLevelOperation(r.OperationType) && strings.TrimSpace(r.NodeID) == "" {
		return fmt.Errorf("node_id is required for operation_type %q", r.OperationType)
	}
	if len(r.Arguments) == 0 || bytes.Equal(bytes.TrimSpace(r.Arguments), []byte("null")) || !json.Valid(r.Arguments) {
		return errors.New("arguments must contain a non-null JSON value")
	}
	if _, ok := validApprovals[r.Approval]; !ok {
		return fmt.Errorf("invalid approval %q", r.Approval)
	}
	if _, ok := validRiskLevels[r.RiskLevel]; !ok {
		return fmt.Errorf("invalid risk_level %q", r.RiskLevel)
	}
	if r.Deadline != nil && !r.Deadline.After(time.Now()) {
		return errors.New("deadline must be in the future")
	}
	if r.PrimaryEpoch <= 0 {
		return errors.New("primary_epoch must be greater than zero")
	}
	return nil
}

// IsClusterLevelOperation reports whether an operation is addressed to the
// cluster rather than an existing node.
func IsClusterLevelOperation(operationType string) bool {
	switch operationType {
	case model.OpTypeProvision, model.OpTypePrimaryTransfer:
		return true
	default:
		return false
	}
}

// ToOperation converts a validated request into its persistence model. The
// optional now argument is intended for deterministic callers and tests.
func (r *OperationRequest) ToOperation(id, requestedBy string, now ...time.Time) (*model.Operation, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	createdAt := time.Now().UTC()
	if len(now) > 1 {
		return nil, errors.New("ToOperation accepts at most one creation time")
	}
	if len(now) == 1 {
		createdAt = now[0]
	}

	arguments, err := canonicalJSON(r.Arguments)
	if err != nil {
		return nil, fmt.Errorf("canonicalize arguments: %w", err)
	}
	key := strings.TrimSpace(r.IdempotencyKey)
	if key == "" {
		key = IdempotencyKey(r)
	}

	return &model.Operation{
		ID:                id,
		OperationID:       r.OperationID,
		OperationType:     r.OperationType,
		ClusterID:         r.ClusterID,
		NodeID:            r.NodeID,
		ModuleID:          r.ModuleID,
		ServiceInstanceID: r.ServiceInstanceID,
		DesiredRevision:   r.DesiredRevision,
		ArgumentsJSON:     string(arguments),
		Approval:          r.Approval,
		RiskLevel:         r.RiskLevel,
		IdempotencyKey:    key,
		Deadline:          r.Deadline,
		PrimaryEpoch:      r.PrimaryEpoch,
		Status:            model.OpStatusPlanned,
		RequestedBy:       requestedBy,
		CreatedAt:         createdAt,
	}, nil
}

// RequestFingerprint returns a stable sha256 fingerprint of the semantic
// content of an OperationRequest (excluding idempotency key and deadline).
// It is used to detect idempotency-key reuse with a different payload.
func RequestFingerprint(req *OperationRequest) string {
	canon := struct {
		OperationType     string          `json:"operation_type"`
		ClusterID         string          `json:"cluster_id"`
		NodeID            string          `json:"node_id"`
		ModuleID          string          `json:"module_id"`
		ServiceInstanceID string          `json:"service_instance_id"`
		DesiredRevision   string          `json:"desired_revision"`
		Arguments         json.RawMessage `json:"arguments"`
	}{
		OperationType:     req.OperationType,
		ClusterID:         req.ClusterID,
		NodeID:            req.NodeID,
		ModuleID:          req.ModuleID,
		ServiceInstanceID: req.ServiceInstanceID,
		DesiredRevision:   req.DesiredRevision,
		Arguments:         req.Arguments,
	}
	raw, _ := json.Marshal(&canon)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
