package opsv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentFilesPermissionsLoadAndCleanup(t *testing.T) {
	root := t.TempDir()
	req := validRequest()
	contextPath, err := WriteAgentContext(root, req, "db/op:1")
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"database_password": "highly-secret-password",
		"api_token":         "highly-secret-token",
	}
	secretDir, err := WriteAgentSecrets(root, secrets)
	if err != nil {
		CleanupAgentFiles(contextPath, "")
		t.Fatal(err)
	}

	assertPerm(t, contextPath, 0o600)
	assertPerm(t, secretDir, 0o700)
	for key, value := range secrets {
		path := filepath.Join(secretDir, key)
		assertPerm(t, path, 0o600)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != value {
			t.Errorf("secret file %q content mismatch", key)
		}
	}

	contextData, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(contextData), secret) {
			t.Fatal("agent context contains a secret value")
		}
	}

	t.Setenv(EnvSecretDir, secretDir)
	input, err := LoadAgentContext(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	if input.Operation.OperationID != req.OperationID || input.ContextPath != contextPath || input.SecretDir != secretDir {
		t.Fatalf("unexpected agent input: %#v", input)
	}

	CleanupAgentFiles(contextPath, secretDir)
	if _, err := os.Stat(contextPath); !os.IsNotExist(err) {
		t.Fatalf("context still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(secretDir); !os.IsNotExist(err) {
		t.Fatalf("secret dir still exists or unexpected stat error: %v", err)
	}
}

func TestWriteAgentSecretsRejectsUnsafeNamesWithoutLeakingValues(t *testing.T) {
	secret := "must-not-appear"
	_, err := WriteAgentSecrets(t.TempDir(), map[string]string{"../escape": secret})
	if err == nil {
		t.Fatal("expected unsafe secret name to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error leaked secret value")
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %#o, want %#o", path, got, want)
	}
}
