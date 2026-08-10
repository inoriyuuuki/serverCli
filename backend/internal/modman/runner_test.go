package modman

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeTestModule(t *testing.T, dir string) string {
	t.Helper()
	modDir := filepath.Join(dir, "testmod")
	if err := os.MkdirAll(filepath.Join(modDir, "operations"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `id: testmod
version: 1.0.0
phase: foundation-core
delivery: env
config_fields:
  - name: APP_NAME
    type: string
    required: true
secret_fields:
  - name: PASSWORD
    type: string
    sensitive: true
operations:
  install:
    entry: operations/install.sh
    timeout_seconds: 60
  verify:
    entry: operations/verify.sh
`
	script := `#!/bin/sh
echo "app=$SERVERCLI_CFG_APP_NAME"
echo "pw=$SERVERCLI_SEC_PASSWORD"
exit 0
`
	verify := `#!/bin/sh
exit 0
`
	if err := os.WriteFile(filepath.Join(modDir, "module.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "operations", "install.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "operations", "verify.sh"), []byte(verify), 0o755); err != nil {
		t.Fatal(err)
	}
	return modDir
}

func TestRunnerExecutesAndRedacts(t *testing.T) {
	dir := t.TempDir()
	writeTestModule(t, dir)
	runner := NewRunner(dir, filepath.Join(dir, "run"), filepath.Join(dir, "locks"), slog.Default(), nil)
	res, err := runner.Run(context.Background(), RunOptions{
		ModuleID:  "testmod",
		Operation: "install",
		Config:    map[string]string{"APP_NAME": "demo"},
		Secrets:   map[string]string{"PASSWORD": "supersecret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d output=%s", res.ExitCode, res.Output)
	}
	if !contains(res.Output, "app=***") {
		t.Fatalf("config value not redacted in output: %s", res.Output)
	}
	if contains(res.Output, "supersecret") {
		t.Fatalf("secret leaked into output: %s", res.Output)
	}
	if !contains(res.Output, "pw=***") {
		t.Fatalf("secret not redacted: %s", res.Output)
	}
	if res.Digest == "" {
		t.Fatal("missing digest")
	}
}

func TestRunnerRejectsNonWhitelistedOp(t *testing.T) {
	dir := t.TempDir()
	writeTestModule(t, dir)
	runner := NewRunner(dir, filepath.Join(dir, "run"), filepath.Join(dir, "locks"), slog.Default(), nil)
	_, err := runner.Run(context.Background(), RunOptions{
		ModuleID:  "testmod",
		Operation: "rm -rf /",
	})
	if err == nil {
		t.Fatal("expected forbidden error")
	}
}

func TestRunnerDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	writeTestModule(t, dir)
	runner := NewRunner(dir, filepath.Join(dir, "run"), filepath.Join(dir, "locks"), slog.Default(), nil)
	_, err := runner.Run(context.Background(), RunOptions{
		ModuleID:       "testmod",
		Operation:      "verify",
		Config:         map[string]string{"APP_NAME": "demo"},
		Secrets:        map[string]string{"PASSWORD": "x"},
		ExpectedDigest: "deadbeef",
	})
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestComputeInputDigestDeterministic(t *testing.T) {
	a := ComputeInputDigest(map[string]string{"B": "1", "A": "2"}, map[string]string{"S": "x"})
	b := ComputeInputDigest(map[string]string{"A": "2", "B": "1"}, map[string]string{"S": "x"})
	if a != b {
		t.Fatalf("digest not deterministic: %s vs %s", a, b)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestValidateModuleIDRejectsTraversal(t *testing.T) {
	bad := []string{"", "..", "../x", "a/b", "/etc/passwd", "a\\b", "a b", "a.b", "A", "a\x00b"}
	for _, id := range bad {
		if err := ValidateModuleID(id); err == nil {
			t.Errorf("ValidateModuleID(%q) should fail", id)
		}
	}
	good := []string{"postgres", "control-plane", "a1", "x-y-z"}
	for _, id := range good {
		if err := ValidateModuleID(id); err != nil {
			t.Errorf("ValidateModuleID(%q) should pass: %v", id, err)
		}
	}
}
