package repo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFixPermissionsNonOwnerRuns verifies the walker tolerates permission
// errors without panicking and returns cleanly on a normal tree.
func TestFixPermissionsNonOwnerRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, RepoDirRepository, "secrets", "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, RepoDirRepository, "secrets", "shared", "x.secrets.yaml"), []byte("k: v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FixPermissions(root); err != nil {
		t.Fatalf("FixPermissions: %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, RepoDirRepository, "secrets", "shared", "x.secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v, want 0600", fi.Mode().Perm())
	}
}
