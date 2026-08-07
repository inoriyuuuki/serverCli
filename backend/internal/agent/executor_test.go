package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestVerifyPayloadSignature(t *testing.T) {
	credential := "ncred_testcredential"
	p := &TaskPayload{
		TaskID: "task-1", NodeID: "node-1", CommandID: "x", CommandVersion: "1.0.0",
		Arguments: json.RawMessage(`{"a":1}`), IdempotencyKey: "k",
	}
	clone := *p
	raw, _ := json.Marshal(&clone)
	clone.PayloadHash = fmt.Sprintf("%x", sha256SumBytes(raw))
	clone.Signature = hmacSign(credential, "task:"+clone.TaskID+":"+clone.PayloadHash)
	if err := VerifyPayload(&clone, credential); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	if err := VerifyPayload(&clone, "wrong-credential"); err == nil {
		t.Fatal("verify with wrong credential should fail")
	}
	clone.Arguments = json.RawMessage(`{"a":2}`)
	if err := VerifyPayload(&clone, credential); err == nil {
		t.Fatal("tampered payload should fail")
	}
}

func TestBuildArgsOrder(t *testing.T) {
	cmd := CommandEntry{
		CommandID:           "x",
		CommandVersion:      "1",
		ParameterSchemaJSON: `{"type":"object","properties":{"mount":{"type":"string"},"level":{"type":"integer"}}}`,
	}
	args := buildArgs(cmd, json.RawMessage(`{"level":3,"mount":"/"}`))
	if len(args) != 2 || args[0] != "/" || args[1] != "3" {
		t.Fatalf("unexpected args: %v", args)
	}
}
