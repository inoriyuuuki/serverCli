package opsv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"servercli/internal/model"
)

// idempotencyMaterial intentionally excludes request identity, approval,
// deadline, and the caller-supplied key. It captures the operation's semantic
// target and desired effect.
type idempotencyMaterial struct {
	OperationType     string          `json:"operation_type"`
	ClusterID         string          `json:"cluster_id"`
	NodeID            string          `json:"node_id"`
	ModuleID          string          `json:"module_id"`
	ServiceInstanceID string          `json:"service_instance_id"`
	DesiredRevision   string          `json:"desired_revision"`
	Arguments         json.RawMessage `json:"arguments"`
	PrimaryEpoch      int64           `json:"primary_epoch"`
}

// IdempotencyKey returns a deterministic SHA-256 key for the semantic request.
// Nested JSON object property order in arguments does not affect the result.
func IdempotencyKey(req *OperationRequest) string {
	if req == nil {
		return ""
	}
	arguments, err := canonicalJSON(req.Arguments)
	if err != nil {
		return ""
	}
	material, err := json.Marshal(idempotencyMaterial{
		OperationType:     req.OperationType,
		ClusterID:         req.ClusterID,
		NodeID:            req.NodeID,
		ModuleID:          req.ModuleID,
		ServiceInstanceID: req.ServiceInstanceID,
		DesiredRevision:   req.DesiredRevision,
		Arguments:         arguments,
		PrimaryEpoch:      req.PrimaryEpoch,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:])
}

// MatchIdempotency reports whether an existing operation carries either the
// request's explicit idempotency key or its deterministic fallback key.
func MatchIdempotency(existing *model.Operation, req *OperationRequest) bool {
	if existing == nil || req == nil {
		return false
	}
	explicit := strings.TrimSpace(req.IdempotencyKey)
	if explicit != "" && existing.IdempotencyKey == explicit {
		return true
	}
	key := IdempotencyKey(req)
	return key != "" && existing.IdempotencyKey == key
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
