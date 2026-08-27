package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

func TestMinimalEnvAlwaysIncludesLocalBin(t *testing.T) {
	// Simulate a parent process started via sudo -u with a restricted
	// secure_path: deployment hooks rely on docker/compose living under
	// /usr/local/bin, so minimalEnv must never drop those directories.
	old := os.Getenv("PATH")
	defer os.Setenv("PATH", old)
	os.Setenv("PATH", "/sbin:/bin:/usr/sbin:/usr/bin")

	env := minimalEnv()
	path := ""
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			path = kv[5:]
		}
	}
	if path == "" {
		t.Fatal("PATH not set")
	}
	for _, want := range []string{"/usr/local/bin", "/usr/local/sbin", "/usr/sbin"} {
		found := false
		for _, part := range strings.Split(path, ":") {
			if part == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("minimalEnv PATH %q missing %s", path, want)
		}
	}
}

func TestMinimalEnvPreservesExistingLocalBin(t *testing.T) {
	old := os.Getenv("PATH")
	defer os.Setenv("PATH", old)
	os.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	env := minimalEnv()
	var path string
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			path = kv[5:]
		}
	}
	if path != "/usr/sbin:/usr/local/sbin:/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("unexpected PATH: %q", path)
	}
}
