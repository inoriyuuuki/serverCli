package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
	"servercli/internal/ownership"
)

const (
	testEnv  = "test"
	testNode = "node-1"
)

// fakeRegistry implements ModuleRegistry.
type fakeRegistry struct {
	order []string
	mods  map[string]*modman.ModuleManifest
}

func (f *fakeRegistry) Ordered() ([]string, error) {
	return append([]string(nil), f.order...), nil
}

func (f *fakeRegistry) Module(id string) (*modman.ModuleManifest, bool) {
	m, ok := f.mods[id]
	return m, ok
}

// fakeRunner implements ModuleRunner and simulates module hooks; when the
// "backup" operation runs it writes a file into SERVERCLI_BACKUP_DIR.
type fakeRunner struct {
	mu            sync.Mutex
	calls         []string // "module:op"
	fail          map[string]error
	exits         map[string]int
	writeOnBackup bool
}

func (f *fakeRunner) Run(ctx context.Context, opts modman.RunOptions) (*modman.RunResult, error) {
	key := opts.ModuleID + ":" + opts.Operation
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if err := f.fail[key]; err != nil {
		return nil, err
	}
	if f.writeOnBackup && opts.Operation == "backup" {
		backupDir := ""
		for _, e := range opts.Env {
			if len(e) > len("SERVERCLI_BACKUP_DIR=") && e[:len("SERVERCLI_BACKUP_DIR=")] == "SERVERCLI_BACKUP_DIR=" {
				backupDir = e[len("SERVERCLI_BACKUP_DIR="):]
			}
		}
		if backupDir != "" {
			if err := os.MkdirAll(backupDir, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(backupDir, "data.txt"), []byte("payload"), 0o600); err != nil {
				return nil, err
			}
		}
	}
	code := f.exits[key]
	return &modman.RunResult{
		ModuleID:    opts.ModuleID,
		Operation:   opts.Operation,
		ExitCode:    code,
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
	}, nil
}

func fakeManifest(id string, ops ...string) *modman.ModuleManifest {
	m := &modman.ModuleManifest{
		ID:         id,
		Version:    "1.2.3",
		Phase:      modman.PhaseFoundationCore,
		Delivery:   modman.DeliveryEnv,
		Operations: map[string]modman.Operation{},
	}
	for _, op := range ops {
		m.Operations[op] = modman.Operation{Entry: "operations/" + op + ".sh"}
	}
	return m
}

// newTestOps builds an Ops wired to fake registry/runner and a temp ownership
// store with the given services owned by servercli.
func newTestOps(t *testing.T, owned []string, mods ...*modman.ModuleManifest) (*Ops, *ownership.Store) {
	t.Helper()
	dir := t.TempDir()
	ostore := ownership.NewStore(filepath.Join(dir, "ownership.json"))
	ostore.SetLockDir(filepath.Join(dir, "locks"))
	if err := ostore.Load(); err != nil {
		t.Fatal(err)
	}
	for _, svc := range owned {
		if err := ostore.Set(testEnv, testNode, svc, ownership.Ownership{Owner: ownership.OwnerServerCLI}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ostore.Save(); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pemPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	order := make([]string, 0, len(mods))
	mm := map[string]*modman.ModuleManifest{}
	for _, m := range mods {
		order = append(order, m.ID)
		mm[m.ID] = m
	}
	cfg := Config{
		Environment:  testEnv,
		Node:         testNode,
		ModulesDir:   filepath.Join(dir, "modules"),
		RunDir:       filepath.Join(dir, "run"),
		LockDir:      filepath.Join(dir, "locks"),
		BackupDir:    filepath.Join(dir, "backups"),
		StatePath:    filepath.Join(dir, "state.json"),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		SigningKey:   priv,
		SigningKeyID: "test-signing-key",
		VerifyKeyPEM: pemPub,
	}
	o := New(ostore, cfg)
	o.Registry = &fakeRegistry{order: order, mods: mm}
	o.Runner = &fakeRunner{fail: map[string]error{}, exits: map[string]int{}, writeOnBackup: true}
	return o, ostore
}

func runnerFor(t *testing.T, o *Ops) *fakeRunner {
	t.Helper()
	r, ok := o.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runner is %T, want *fakeRunner", o.Runner)
	}
	return r
}

func TestUpdateAggregateFailure(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a", "svc-b"},
		fakeManifest("svc-a", "install", "verify"),
		fakeManifest("svc-b", "install", "verify"))
	r := runnerFor(t, o)
	r.fail["svc-b:install"] = errors.New("boom")

	results, err := o.Update(context.Background(), []string{"svc-a", "svc-b"}, RunOpts{})
	if err == nil {
		t.Fatal("expected aggregate error")
	}
	var agg *AggregateError
	if !errors.As(err, &agg) {
		t.Fatalf("err = %T, want *AggregateError", err)
	}
	if len(agg.Failures) != 1 || agg.Failures[0].Service != "svc-b" {
		t.Fatalf("failures = %+v", agg.Failures)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if !results[0].OK {
		t.Fatalf("svc-a should be ok: %+v", results[0])
	}
	if results[1].OK || results[1].Error == "" {
		t.Fatalf("svc-b should fail: %+v", results[1])
	}
	// svc-a must still have been updated despite svc-b failing.
	for _, call := range []string{"svc-a:install", "svc-a:verify", "svc-b:install"} {
		if !contains(r.calls, call) {
			t.Errorf("missing call %q in %v", call, r.calls)
		}
	}
}

func TestUpdateEmptyMeansAllServices(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a", "svc-b"},
		fakeManifest("svc-a", "install", "verify"),
		fakeManifest("svc-b", "install", "verify"))
	results, err := o.Update(context.Background(), nil, RunOpts{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("service %s failed: %+v", r.Service, r)
		}
	}
}

func TestUpdateBlockedWithoutServerCLIOwner(t *testing.T) {
	o, _ := newTestOps(t, nil, fakeManifest("svc-a", "install"))
	results, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{})
	if err == nil {
		t.Fatal("expected aggregate error")
	}
	if len(results) != 1 || results[0].OK || !strings.Contains(results[0].Error, "no ownership") {
		t.Fatalf("results = %+v", results)
	}
}

func TestUpdateSkipsUndeclaredVerify(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "install"))
	r := runnerFor(t, o)
	results, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	if contains(r.calls, "svc-a:verify") {
		t.Fatalf("verify should be skipped when undeclared: %v", r.calls)
	}
}

func TestUpdateRespectsServiceLock(t *testing.T) {
	o, store := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "install"))
	unlock, err := store.Lock(testEnv, testNode, "svc-a", "adopt")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	results, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{})
	if err == nil {
		t.Fatal("expected aggregate error")
	}
	if results[0].OK || results[0].Error == "" {
		t.Fatalf("results = %+v", results[0])
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestUpdateSchemaCompatGate(t *testing.T) {
	o, _ := newTestOps(t, []string{"svc-a"}, fakeManifest("svc-a", "install", "verify"))
	compat := &bootstrap.SchemaCompat{MinSchemaVersion: "1.0.0", MaxSchemaVersion: "2.0.0", Reversible: true}
	o.Config.ReleaseCompat = compat

	// Too-old current schema -> blocked.
	o.Config.CurrentSchemaVersion = "0.9.0"
	if _, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{}); err == nil {
		t.Fatal("expected schema compat error for too-old current schema")
	}
	// Compatible window -> passes.
	o.Config.CurrentSchemaVersion = "1.5.0"
	if _, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{}); err != nil {
		t.Fatalf("compatible update should pass: %v", err)
	}
	// Irreversible migration requires maintenance mode.
	o.Config.ReleaseCompat = &bootstrap.SchemaCompat{MinSchemaVersion: "1.0.0", MaxSchemaVersion: "2.0.0", Reversible: false}
	if _, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{}); err == nil {
		t.Fatal("expected maintenance-mode error for irreversible migration")
	}
	o.Config.Inventory = &bootstrap.Inventory{Update: bootstrap.UpdatePolicy{Maintenance: true}}
	if _, err := o.Update(context.Background(), []string{"svc-a"}, RunOpts{}); err != nil {
		t.Fatalf("irreversible with maintenance should pass: %v", err)
	}
}
