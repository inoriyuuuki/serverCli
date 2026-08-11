package opsv2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"servercli/internal/model"
)

func validRequest() *OperationRequest {
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	return &OperationRequest{
		OperationID:       "op-public-1",
		OperationType:     model.OpTypeUpdate,
		ClusterID:         "cluster-1",
		NodeID:            "node-1",
		ModuleID:          "postgres",
		ServiceInstanceID: "postgres-primary",
		DesiredRevision:   "rev-42",
		Arguments:         json.RawMessage(`{"replicas":2,"config":{"b":2,"a":1}}`),
		Approval:          "approved",
		RiskLevel:         model.RiskMedium,
		IdempotencyKey:    "caller-key",
		Deadline:          &deadline,
		PrimaryEpoch:      7,
	}
}

func requestJSONMap(req *OperationRequest) map[string]any {
	return map[string]any{
		"primary_epoch":       req.PrimaryEpoch,
		"deadline":            req.Deadline,
		"idempotency_key":     req.IdempotencyKey,
		"risk_level":          req.RiskLevel,
		"approval":            req.Approval,
		"arguments":           json.RawMessage(req.Arguments),
		"desired_revision":    req.DesiredRevision,
		"service_instance_id": req.ServiceInstanceID,
		"module_id":           req.ModuleID,
		"node_id":             req.NodeID,
		"cluster_id":          req.ClusterID,
		"operation_type":      req.OperationType,
		"operation_id":        req.OperationID,
	}
}

func TestParseOperationRequestStrictAndOrderIndependent(t *testing.T) {
	req := validRequest()
	data, err := json.Marshal(requestJSONMap(req))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseOperationRequest(data)
	if err != nil {
		t.Fatalf("ParseOperationRequest() error = %v", err)
	}
	if got.OperationID != req.OperationID || got.OperationType != req.OperationType || got.PrimaryEpoch != req.PrimaryEpoch {
		t.Fatalf("parsed request differs: %#v", got)
	}

	// Property order is deliberately reversed relative to the struct.
	reversed := `{"primary_epoch":7,"deadline":null,"idempotency_key":"caller-key","risk_level":"medium","approval":"approved","arguments":{"config":{"a":1,"b":2},"replicas":2},"desired_revision":"rev-42","service_instance_id":"postgres-primary","module_id":"postgres","node_id":"node-1","cluster_id":"cluster-1","operation_type":"update","operation_id":"op-public-1"}`
	got, err = ParseOperationRequest([]byte(reversed))
	if err != nil {
		t.Fatalf("reordered ParseOperationRequest() error = %v", err)
	}
	if got.OperationID != req.OperationID || got.ModuleID != req.ModuleID {
		t.Fatalf("reordered request differs: %#v", got)
	}
}

func TestParseOperationRequestRejectsMissingUnknownAndTrailing(t *testing.T) {
	base := requestJSONMap(validRequest())

	delete(base, "module_id")
	data, _ := json.Marshal(base)
	if _, err := ParseOperationRequest(data); err == nil || !strings.Contains(err.Error(), "module_id") {
		t.Fatalf("missing field error = %v", err)
	}

	base = requestJSONMap(validRequest())
	base["unexpected"] = true
	data, _ = json.Marshal(base)
	if _, err := ParseOperationRequest(data); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}

	data, _ = json.Marshal(requestJSONMap(validRequest()))
	data = append(data, []byte(` {}`)...)
	if _, err := ParseOperationRequest(data); err == nil {
		t.Fatal("expected trailing JSON value to fail")
	}
}

func TestOperationRequestValidate(t *testing.T) {
	tests := []struct {
		name string
		edit func(*OperationRequest)
		want string
	}{
		{"operation type", func(r *OperationRequest) { r.OperationType = "shell" }, "operation_type"},
		{"node", func(r *OperationRequest) { r.NodeID = "" }, "node_id"},
		{"risk", func(r *OperationRequest) { r.RiskLevel = "extreme" }, "risk_level"},
		{"approval", func(r *OperationRequest) { r.Approval = "maybe" }, "approval"},
		{"epoch", func(r *OperationRequest) { r.PrimaryEpoch = 0 }, "primary_epoch"},
		{"deadline", func(r *OperationRequest) { d := time.Now().Add(-time.Second); r.Deadline = &d }, "deadline"},
		{"arguments", func(r *OperationRequest) { r.Arguments = json.RawMessage(`null`) }, "arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.edit(req)
			if err := req.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	for _, opType := range []string{model.OpTypeProvision, model.OpTypePrimaryTransfer} {
		req := validRequest()
		req.OperationType = opType
		req.NodeID = ""
		if err := req.Validate(); err != nil {
			t.Errorf("cluster-level %q rejected: %v", opType, err)
		}
	}
}

func TestToOperation(t *testing.T) {
	req := validRequest()
	req.IdempotencyKey = ""
	now := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	op, err := req.ToOperation("db-id", "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != "db-id" || op.OperationID != req.OperationID || op.Status != model.OpStatusPlanned || op.RequestedBy != "alice" {
		t.Fatalf("unexpected operation: %#v", op)
	}
	if !op.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", op.CreatedAt, now)
	}
	if op.IdempotencyKey != IdempotencyKey(req) {
		t.Fatalf("IdempotencyKey = %q", op.IdempotencyKey)
	}
	if op.ArgumentsJSON != `{"config":{"a":1,"b":2},"replicas":2}` {
		t.Fatalf("ArgumentsJSON = %s", op.ArgumentsJSON)
	}
}
