package agent

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"servercli/internal/logger"
)

func TestLoadCommands(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "disk-usage")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: servercli/v1
kind: Command
metadata:
  id: system.disk-usage
  version: 1.0.0
  category: system
  title: 磁盘使用
  description: test
spec:
  executable: disk-usage
  permissionProfile: read-only
  timeoutSeconds: 15
  maxOutputBytes: 262144
  parameters:
    type: object
    additionalProperties: false
    required: [mount]
    properties:
      mount:
        type: string
        enum: ["/", "/data"]
`
	if err := os.WriteFile(filepath.Join(dir, "disk.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	log := logger.New(io.Discard, "error")
	cmds, err := LoadCommands(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	c := cmds[0]
	if c.CommandID != "system.disk-usage" || c.CommandVersion != "1.0.0" {
		t.Fatalf("bad id/version: %+v", c)
	}
	if c.ExecutablePath != exe {
		t.Fatalf("executable path not resolved: %s", c.ExecutablePath)
	}
	if c.ManifestHash == "" || c.ExecutableHash == "" {
		t.Fatal("hashes missing")
	}
	if c.ParameterSchemaJSON == "" {
		t.Fatal("schema missing")
	}
	// Non-executable should be skipped.
	bad := filepath.Join(dir, "bad.yaml")
	badManifest := `apiVersion: servercli/v1
kind: Command
metadata:
  id: bad.cmd
  version: 1.0.0
spec:
  executable: /nonexistent
`
	_ = os.WriteFile(bad, []byte(badManifest), 0o644)
	cmds, err = LoadCommands(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("invalid manifest should be skipped, got %d", len(cmds))
	}
}
