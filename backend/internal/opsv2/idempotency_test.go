package opsv2

import (
	"encoding/json"
	"testing"

	"servercli/internal/model"
)

func TestIdempotencyKeyDeterministicAndCanonical(t *testing.T) {
	a := validRequest()
	a.IdempotencyKey = "caller-one"
	a.Arguments = json.RawMessage(`{"z":3,"nested":{"b":2,"a":1}}`)
	b := *a
	b.IdempotencyKey = "caller-two"
	b.OperationID = "another-request-id"
	b.Arguments = json.RawMessage(` { "nested": { "a": 1, "b": 2 }, "z": 3 } `)

	keyA := IdempotencyKey(a)
	keyB := IdempotencyKey(&b)
	if keyA == "" {
		t.Fatal("IdempotencyKey returned empty key")
	}
	if keyA != keyB {
		t.Fatalf("equivalent requests produced different keys:\n%s\n%s", keyA, keyB)
	}

	b.DesiredRevision = "different-revision"
	if keyA == IdempotencyKey(&b) {
		t.Fatal("semantically different requests produced the same key")
	}
}

func TestMatchIdempotency(t *testing.T) {
	req := validRequest()
	existing := &model.Operation{IdempotencyKey: req.IdempotencyKey}
	if !MatchIdempotency(existing, req) {
		t.Fatal("explicit idempotency key did not match")
	}

	req.IdempotencyKey = ""
	existing.IdempotencyKey = IdempotencyKey(req)
	if !MatchIdempotency(existing, req) {
		t.Fatal("deterministic idempotency key did not match")
	}

	existing.IdempotencyKey = "other"
	if MatchIdempotency(existing, req) {
		t.Fatal("different idempotency key matched")
	}
	if MatchIdempotency(nil, req) || MatchIdempotency(existing, nil) {
		t.Fatal("nil input matched")
	}
}
