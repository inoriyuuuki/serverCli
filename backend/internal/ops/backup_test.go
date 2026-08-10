package ops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"servercli/internal/modman"
	"servercli/internal/ownership"
)

// recordingUploader records uploads for assertions.
type recordingUploader struct {
	mu    sync.Mutex
	paths []string
	data  map[string][]byte
	fail  map[string]error
}

func (u *recordingUploader) Upload(ctx context.Context, path string, data []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.fail[path]; err != nil {
		return err
	}
	u.paths = append(u.paths, path)
	if u.data == nil {
		u.data = map[string][]byte{}
	}
	u.data[path] = append([]byte(nil), data...)
	return nil
}

func (u *recordingUploader) has(path string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, p := range u.paths {
		if p == path {
			return true
		}
	}
	return false
}

func TestBackupGeneratesSignedManifest(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "backup"))
	uploader := &recordingUploader{}
	o.Config.Uploader = uploader

	results, err := o.Backup(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil {
		t.Fatalf("backup err = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	res := results[0]
	if !res.OK {
		t.Fatalf("backup failed: %+v", res)
	}
	if res.BackupID == "" || res.RecoverySetID == "" || res.ManifestPath == "" {
		t.Fatalf("incomplete result: %+v", res)
	}
	raw, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var man BackupManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if man.BackupID != res.BackupID || man.RecoverySetID != res.RecoverySetID {
		t.Fatalf("manifest ids mismatch: %+v", man)
	}
	if man.Service != "svc-a" || man.Environment != testEnv || man.Node != testNode {
		t.Fatalf("manifest context mismatch: %+v", man)
	}
	if man.AppVersion != "1.2.3" {
		t.Fatalf("app version = %q", man.AppVersion)
	}
	if man.Signature == "" {
		t.Fatal("manifest not signed")
	}
	// Signature must verify with the configured key.
	if err := man.Verify(o.Config.VerifyKeyPEM); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Tampered manifest must fail verification.
	man2 := man
	man2.AppVersion = "tampered"
	if err := man2.Verify(o.Config.VerifyKeyPEM); err == nil {
		t.Fatal("tampered manifest still verifies")
	}
	man3 := man
	man3.Files = append([]BackupFile(nil), man.Files...) // deep copy before mutation
	if len(man3.Files) > 0 {
		man3.Files[0].SHA256 = strings.Repeat("0", 64)
	}
	if err := man3.Verify(o.Config.VerifyKeyPEM); err == nil {
		t.Fatal("tampered file digest still verifies")
	}

	// File digest recorded and matches on disk.
	if len(man.Files) != 1 || man.Files[0].Path != "data.txt" {
		t.Fatalf("files = %+v", man.Files)
	}
	backupRoot := filepath.Dir(res.ManifestPath)
	digest, size, err := sha256File(filepath.Join(backupRoot, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if digest != man.Files[0].SHA256 || size != man.Files[0].Size {
		t.Fatalf("digest mismatch: got %s/%d want %s/%d", digest, size, man.Files[0].SHA256, man.Files[0].Size)
	}

	// Remote upload adapter was invoked for the file and the manifest.
	if !uploader.has("servercli/backups/"+res.BackupID+"/data.txt") {
		t.Fatalf("file not uploaded; paths=%v", uploader.paths)
	}
	if !uploader.has("servercli/backups/" + res.BackupID + "/" + manifestFilename) {
		t.Fatalf("manifest not uploaded; paths=%v", uploader.paths)
	}
}

func TestBackupFailureContinuesToNextService(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a", "svc-b"},
		fakeManifest("svc-a", "backup"), fakeManifest("svc-b", "backup"))
	r := runnerFor(t, o)
	r.fail["svc-b:backup"] = errors.New("disk full")

	results, err := o.Backup(context.Background(), []string{"svc-a", "svc-b"}, RunOpts{})
	if err == nil {
		t.Fatal("expected aggregate error")
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if !results[0].OK || results[1].OK {
		t.Fatalf("results = %+v", results)
	}
}

func TestRestoreRequiresExplicitID(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "restore"))
	if err := o.Restore(context.Background(), "svc-a", "", RunOpts{}); !errors.Is(err, ErrRequireExplicitID) {
		t.Fatalf("err = %v, want ErrRequireExplicitID", err)
	}
	// recovery_set_id path uses the same explicit requirement.
	if err := o.Restore(context.Background(), "svc-a", "  ", RunOpts{}); !errors.Is(err, ErrRequireExplicitID) {
		t.Fatalf("err = %v, want ErrRequireExplicitID", err)
	}
}

func TestRestoreRequiresConfirmation(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "backup", "restore"))
	results, err := o.Backup(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil || !results[0].OK {
		t.Fatalf("backup failed: %v %+v", err, results)
	}
	id := results[0].BackupID

	if err := o.Restore(context.Background(), "svc-a", id, RunOpts{}); !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("err = %v, want ErrRequireConfirm", err)
	}
	// TTY-style confirmation with "yes" works.
	var buf strings.Builder
	if err := o.Restore(context.Background(), "svc-a", id, RunOpts{In: strings.NewReader("yes\n"), Out: &buf}); err != nil {
		t.Fatalf("restore with yes failed: %v", err)
	}
	// Anything other than "yes" is refused.
	if err := o.Restore(context.Background(), "svc-a", id, RunOpts{In: strings.NewReader("no\n"), Out: &buf}); !errors.Is(err, ErrRequireConfirm) {
		t.Fatalf("err = %v, want ErrRequireConfirm", err)
	}
}

func TestRestoreRejectsUnknownBackup(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "restore"))
	err := o.Restore(context.Background(), "svc-a", "bak-does-not-exist", RunOpts{Confirm: true})
	if !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("err = %v, want ErrBackupNotFound", err)
	}
}

func TestRestoreVerifiesSignatureBeforeRestore(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "backup", "restore"))
	results, err := o.Backup(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil || !results[0].OK {
		t.Fatalf("backup failed: %v %+v", err, results)
	}
	id := results[0].BackupID

	// Tamper with a backed-up file: pre-restore digest verification must fail
	// and the restore hook must never run.
	backupRoot := filepath.Dir(results[0].ManifestPath)
	if err := os.WriteFile(filepath.Join(backupRoot, "data.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = o.Restore(context.Background(), "svc-a", id, RunOpts{Confirm: true})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("err = %v, want digest mismatch", err)
	}

	// Wrong verification key must also fail.
	o2, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "backup", "restore"))
	results2, err := o2.Backup(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil || !results2[0].OK {
		t.Fatalf("backup2 failed: %v %+v", err, results2)
	}
	// Reuse the first ops' key (different from o2's key): signature must fail.
	o2.Config.VerifyKeyPEM = o.Config.VerifyKeyPEM
	err = o2.Restore(context.Background(), "svc-a", results2[0].BackupID, RunOpts{Confirm: true})
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err = %v, want signature failure", err)
	}
}

func TestRestoreByRecoverySetID(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "backup", "restore"))
	results, err := o.Backup(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil || !results[0].OK {
		t.Fatalf("backup failed: %v %+v", err, results)
	}
	if err := o.Restore(context.Background(), "svc-a", results[0].RecoverySetID, RunOpts{Confirm: true}); err != nil {
		t.Fatalf("restore by recovery_set_id: %v", err)
	}
}

func TestLegacyAdapterMarksMissingMetadata(t *testing.T) {
	o, _ := newTestOps(t, nil)
	legacyDir := filepath.Join(o.Config.BackupDir, "legacy-backup-1")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "dump.sql"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewLegacyBackupAdapter(o.Config.BackupDir)
	lb, err := adapter.Read(context.Background(), "legacy-backup-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if lb.Verified {
		t.Fatal("legacy backup must never be verified")
	}
	if len(lb.MissingMetadata) == 0 {
		t.Fatal("legacy backup must flag missing metadata")
	}
	if lb.Format != FormatLegacyImport {
		t.Fatalf("format = %q", lb.Format)
	}
	if _, err := lb.Manifest(); !errors.Is(err, ErrNoVerifiedManifest) {
		t.Fatalf("Manifest() = %v, want ErrNoVerifiedManifest", err)
	}
	list, err := adapter.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, err = %v", list, err)
	}
}

func TestRestoreRefusesLegacyBackup(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "restore"))
	legacyDir := filepath.Join(o.Config.BackupDir, "legacy-backup-1")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "dump.sql"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := o.Restore(context.Background(), "svc-a", "legacy-backup-1", RunOpts{Confirm: true})
	if !errors.Is(err, ErrLegacyBackup) {
		t.Fatalf("err = %v, want ErrLegacyBackup", err)
	}
}

// TestOpsWithRealRunner exercises the full wiring against the real
// modman.Runner and DepGraph with a temporary module, proving that update,
// backup (signed manifest + digest) and restore work end to end.
func TestOpsWithRealRunner(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "modules")
	moduleDir := filepath.Join(modulesDir, "gitea")
	opsDir := filepath.Join(moduleDir, "operations")
	for _, d := range []string{moduleDir, opsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	moduleYAML := `id: gitea
version: 9.9.9
phase: services
delivery: env
operations:
  install:
    entry: operations/install.sh
  verify:
    entry: operations/verify.sh
  backup:
    entry: operations/backup.sh
  restore:
    entry: operations/restore.sh
concurrency: service
`
	if err := os.WriteFile(filepath.Join(moduleDir, "module.yaml"), []byte(moduleYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"install.sh":  "#!/bin/sh\necho \"install $SERVERCLI_SERVICE ok\"\nexit 0\n",
		"verify.sh":   "#!/bin/sh\nexit 0\n",
		"backup.sh":   "#!/bin/sh\nmkdir -p \"$SERVERCLI_BACKUP_DIR\"\necho \"payload\" > \"$SERVERCLI_BACKUP_DIR/data.txt\"\nexit 0\n",
		"restore.sh":  "#!/bin/sh\ntest -f \"$SERVERCLI_BACKUP_DIR/data.txt\"\nexit $?\n",
	}
	for name, body := range scripts {
		p := filepath.Join(opsDir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mods, err := modman.LoadAll(modulesDir)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := modman.NewDepGraph(mods)
	if err != nil {
		t.Fatal(err)
	}

	ostore := ownership.NewStore(filepath.Join(dir, "ownership.json"))
	ostore.SetLockDir(filepath.Join(dir, "locks"))
	if err := ostore.Load(); err != nil {
		t.Fatal(err)
	}
	if err := ostore.Set(testEnv, testNode, "gitea", ownership.Ownership{Owner: ownership.OwnerServerCLI}); err != nil {
		t.Fatal(err)
	}
	if err := ostore.Save(); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Environment: testEnv,
		Node:        testNode,
		ModulesDir:  modulesDir,
		RunDir:      filepath.Join(dir, "run"),
		LockDir:     filepath.Join(dir, "locks"),
		BackupDir:   filepath.Join(dir, "backups"),
		StatePath:   filepath.Join(dir, "state.json"),
	}
	o := New(ostore, cfg)
	o.Registry = graph
	o.Runner = modman.NewRunner(modulesDir, cfg.RunDir, filepath.Join(cfg.LockDir, "runner"), nil, graph)
	// Inject random signing/verification keys.
	pub, priv, err := ed25519Generate()
	if err != nil {
		t.Fatal(err)
	}
	o.Config.SigningKey = priv
	o.Config.SigningKeyID = "test-key"
	o.Config.VerifyKeyPEM = ed25519PEM(t, pub)

	ctx := context.Background()
	results, err := o.Update(ctx, []string{"gitea"}, RunOpts{})
	if err != nil || !results[0].OK {
		t.Fatalf("update failed: %v %+v", err, results)
	}

	bs, err := o.Backup(ctx, []string{"gitea"}, RunOpts{})
	if err != nil || !bs[0].OK {
		t.Fatalf("backup failed: %v %+v", err, bs)
	}
	raw, err := os.ReadFile(bs[0].ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var man BackupManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if err := man.Verify(o.Config.VerifyKeyPEM); err != nil {
		t.Fatalf("manifest verify: %v", err)
	}
	if len(man.Files) != 1 || man.Files[0].Path != "data.txt" {
		t.Fatalf("files = %+v", man.Files)
	}

	if err := o.Restore(ctx, "gitea", bs[0].BackupID, RunOpts{Confirm: true}); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
}
