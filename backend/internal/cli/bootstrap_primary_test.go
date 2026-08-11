package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPlatformMatches(t *testing.T) {
	if !platformMatches("servercli-linux-amd64", "linux", "amd64") {
		t.Fatal("linux/amd64 should match")
	}
	if !platformMatches("servercli-linux-arm64", "linux", "arm64") {
		t.Fatal("linux/arm64 should match")
	}
	if platformMatches("servercli-linux-amd64", "linux", "arm64") {
		t.Fatal("linux/amd64 must not match arm64")
	}
	if platformMatches("servercli-linux-amd64", "darwin", "amd64") {
		t.Fatal("must not match wrong os")
	}
}

func TestInstallBinarySafely(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "servercli")
	data := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(src, data, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "bin", "servercli")

	if err := installBinarySafely(src, dst, 0o755); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("content mismatch")
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination must be a regular file, not symlink")
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", fi.Mode().Perm())
	}
	// no leftover temp files
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' && len(e.Name()) > len(".servercli-install-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestInstallBinarySafelyRejectsSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "servercli")
	if err := os.WriteFile(src, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(dir, "real")
	os.MkdirAll(realDir, 0o755)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip("symlink unsupported")
	}
	err := installBinarySafely(src, filepath.Join(link, "servercli"), 0o755)
	if err == nil {
		t.Fatal("expected error for symlinked parent")
	}
}

func TestInstallServerCLIFromArtifactsPicksPlatform(t *testing.T) {
	// installServerCLIFromArtifacts installs into fixed system dirs, so the
	// safe-to-test surface is the candidate-selection helper. The full
	// install path is covered in e2e/integration with a root sandbox.
	osName, arch := runtime.GOOS, runtime.GOARCH
	platformArtifact := "servercli-" + osName + "-" + arch
	if !platformMatches(platformArtifact, osName, arch) {
		t.Fatalf("platform artifact %s should match %s/%s", platformArtifact, osName, arch)
	}
}
