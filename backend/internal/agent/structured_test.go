package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareStructuredTaskLegacyReturnsNil(t *testing.T) {
	p := &TaskPayload{Arguments: json.RawMessage(`{"operation":"backup","service":"docker"}`)}
	env, err := prepareStructuredTask(p)
	if err != nil {
		t.Fatalf("legacy payload should not error: %v", err)
	}
	if env != nil {
		t.Fatalf("expected nil structured env for legacy payload, got %+v", env)
	}
}

func TestBuildTaskArgsDoesNotExposeStructuredPayload(t *testing.T) {
	const secret = "argv-secret-must-not-leak"
	opJSON := `{
		"operation_id":"op-argv","operation_type":"backup","cluster_id":"clu-1",
		"node_id":"node-1","module_id":"postgres","service_instance_id":"svc-pg",
		"desired_revision":"","arguments":{"password":"` + secret + `"},"approval":"auto",
		"risk_level":"medium","idempotency_key":"idem-argv","deadline":null,
		"primary_epoch":1
	}`
	structuredArgs, err := json.Marshal(map[string]json.RawMessage{
		reservedStructuredKey: json.RawMessage(opJSON),
		structuredSecretsKey:  json.RawMessage(`{"postgres.password":"` + secret + `"}`),
	})
	if err != nil {
		t.Fatalf("marshal structured arguments: %v", err)
	}

	cmd := CommandEntry{
		ParameterSchemaJSON: `{"type":"object","properties":{"operation_request":{},"_secrets":{}}}`,
	}
	structuredPayload := &TaskPayload{Arguments: structuredArgs}
	argv := buildTaskArgs(structuredPayload, cmd)
	if len(argv) != 0 {
		t.Fatalf("structured argv = %q, want empty", argv)
	}
	joined := strings.Join(argv, " ")
	for _, forbidden := range []string{secret, reservedStructuredKey, structuredSecretsKey} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("structured argv contains forbidden value %q: %q", forbidden, joined)
		}
	}

	legacyPayload := &TaskPayload{Arguments: json.RawMessage(`{"operation":"backup","service":"docker"}`)}
	legacyArgv := buildTaskArgs(legacyPayload, CommandEntry{})
	if len(legacyArgv) == 0 {
		t.Fatal("legacy argv must not be empty")
	}
}

func TestPrepareStructuredTaskWrites0600ContextAndSecrets(t *testing.T) {
	opJSON := `{
		"operation_id":"op-1","operation_type":"backup","cluster_id":"clu-1",
		"node_id":"node-1","module_id":"postgres","service_instance_id":"svc-pg",
		"desired_revision":"","arguments":{"data":"x"},"approval":"auto",
		"risk_level":"medium","idempotency_key":"idem-1","deadline":null,
		"primary_epoch":1
	}`
	args := map[string]json.RawMessage{
		"operation_request": json.RawMessage(opJSON),
		"_secrets":          json.RawMessage(`{"postgres.password":"hunter2"}`),
	}
	rawArgs, _ := json.Marshal(args)
	p := &TaskPayload{TaskID: "task-1", Arguments: rawArgs}

	env, err := prepareStructuredTask(p)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if env == nil {
		t.Fatal("expected structured env")
	}
	defer env.cleanup()

	if !strings.Contains(strings.Join(env.extra, " "), "SERVERCLI_OPERATION_CONTEXT=") {
		t.Fatalf("missing context env: %v", env.extra)
	}
	// context file must be 0600 and contain no secret value
	ctxPath := ""
	for _, e := range env.extra {
		if strings.HasPrefix(e, "SERVERCLI_OPERATION_CONTEXT=") {
			ctxPath = strings.TrimPrefix(e, "SERVERCLI_OPERATION_CONTEXT=")
		}
	}
	if ctxPath == "" {
		t.Fatal("no context path")
	}
	fi, err := os.Stat(ctxPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("context file mode = %o, want 600", fi.Mode().Perm())
	}
	ctxData, _ := os.ReadFile(ctxPath)
	if strings.Contains(string(ctxData), "hunter2") {
		t.Fatal("context file must not contain secret values")
	}

	// secret dir 0700, each secret file 0600, secret value present
	secretDir := ""
	for _, e := range env.extra {
		if strings.HasPrefix(e, "SERVERCLI_SECRET_DIR=") {
			secretDir = strings.TrimPrefix(e, "SERVERCLI_SECRET_DIR=")
		}
	}
	if secretDir == "" {
		t.Fatal("no secret dir")
	}
	di, err := os.Stat(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("secret dir mode = %o, want 700", di.Mode().Perm())
	}
	entries, err := os.ReadDir(secretDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 secret file, got %d", len(entries))
	}
	secretPath := filepath.Join(secretDir, entries[0].Name())
	sfi, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if sfi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %o, want 600", sfi.Mode().Perm())
	}
	sdata, _ := os.ReadFile(secretPath)
	if strings.TrimSpace(string(sdata)) != "hunter2" {
		t.Fatalf("secret file content mismatch: %q", string(sdata))
	}

	// cleanup removes everything
	env.cleanup()
	if _, err := os.Stat(ctxPath); !os.IsNotExist(err) {
		t.Fatal("context file not cleaned up")
	}
	if _, err := os.Stat(secretDir); !os.IsNotExist(err) {
		t.Fatal("secret dir not cleaned up")
	}
}

func TestPrepareStructuredTaskRejectsInvalidRequest(t *testing.T) {
	args := map[string]json.RawMessage{
		"operation_request": json.RawMessage(`{"operation_id":"op-1"}`), // missing fields
	}
	rawArgs, _ := json.Marshal(args)
	p := &TaskPayload{TaskID: "task-1", Arguments: rawArgs}
	env, err := prepareStructuredTask(p)
	if err == nil {
		t.Fatal("expected error for invalid operation request")
	}
	if env != nil {
		env.cleanup()
		t.Fatal("no env should be returned on error")
	}
}
