package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servercli/internal/logger"
)

func TestLeaseKeyManagerApplyAndSweep(t *testing.T) {
	log := logger.New(io.Discard, "error")
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	m := NewLeaseKeyManager(path, "/opt/bin/servercli-lease-shell", log)

	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyValueForTesting12345 fake"
	installs := []LeaseInstall{{LeaseID: "lease-1", PublicKey: pub, ExpiresAt: time.Now().Add(time.Hour)}}
	if err := m.Apply(installs, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "restrict") {
		t.Fatal("missing restrict option")
	}
	if !strings.Contains(content, "no-agent-forwarding") || !strings.Contains(content, "no-port-forwarding") ||
		!strings.Contains(content, "no-X11-forwarding") || !strings.Contains(content, "no-user-rc") {
		t.Fatal("missing forwarding restrictions")
	}
	if !strings.Contains(content, `command="/opt/bin/servercli-lease-shell --lease lease-1"`) {
		t.Fatalf("missing command wrapper: %s", content)
	}
	// File perms 0600.
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %v", info.Mode().Perm())
	}
	// Remove.
	if err := m.Apply(nil, []LeaseRemove{{LeaseID: "lease-1", Reason: "revoked"}}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("key not removed: %s", data)
	}
	// Reinstall an expired key and sweep locally.
	if err := m.Apply([]LeaseInstall{{LeaseID: "lease-2", PublicKey: pub, ExpiresAt: time.Now().Add(-time.Minute)}}, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := m.SweepExpired(time.Now()); err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
}
