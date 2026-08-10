package ownership

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	testEnv = "test"
	testNode = "node-1"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "ownership.json"))
	st.SetLockDir(filepath.Join(dir, "locks"))
	if err := st.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return st
}

func mustSet(t *testing.T, st *Store, service, owner string) {
	t.Helper()
	if err := st.Set(testEnv, testNode, service, Ownership{Owner: owner}); err != nil {
		t.Fatalf("set %s: %v", service, err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("save %s: %v", service, err)
	}
}

func TestSaveLoadRoundTripAndPerms(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "postgres", OwnerServerCLI)
	st.Set(testEnv, testNode, "postgres", Ownership{Owner: OwnerServerCLI, ConfigDigest: "abc", SecretRef: "postgres.password"})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("ownership file perms = %o, want 600", fi.Mode().Perm())
	}

	st2 := NewStore(st.Path())
	st2.SetLockDir(filepath.Join(t.TempDir(), "locks"))
	if err := st2.Load(); err != nil {
		t.Fatal(err)
	}
	o, ok := st2.Get(testEnv, testNode, "postgres")
	if !ok {
		t.Fatal("record missing after reload")
	}
	if o.Owner != OwnerServerCLI || o.ConfigDigest != "abc" || o.SecretRef != "postgres.password" {
		t.Fatalf("roundtrip mismatch: %+v", o)
	}
}

func TestMissingFileLoadsEmpty(t *testing.T) {
	st := newTestStore(t)
	if _, ok := st.Get(testEnv, testNode, "anything"); ok {
		t.Fatal("unexpected record")
	}
	if err := st.CanOperate(testEnv, testNode, "anything"); !errors.Is(err, ErrNoOwnership) {
		t.Fatalf("CanOperate = %v, want ErrNoOwnership", err)
	}
}

func TestCorruptFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ownership.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewStore(path)
	if err := st.Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load = %v, want ErrCorrupt", err)
	}
}

func TestSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ownership.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	st := NewStore(link)
	if err := st.Load(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Load = %v, want ErrSymlink", err)
	}
	if err := st.Save(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Save = %v, want ErrSymlink", err)
	}
}

func TestTransitionLegalAndIllegal(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{OwnerLegacyInit, OwnerMigrationFrozen, true},
		{OwnerMigrationFrozen, OwnerAdopting, true},
		{OwnerAdopting, OwnerServerCLI, true},
		{OwnerServerCLI, OwnerRollbackPending, true},
		{OwnerAdopting, OwnerLegacyInit, true}, // adopt failure recovery
		// illegal
		{OwnerLegacyInit, OwnerAdopting, false},
		{OwnerLegacyInit, OwnerServerCLI, false},
		{OwnerMigrationFrozen, OwnerServerCLI, false},
		{OwnerAdopting, OwnerMigrationFrozen, false},
		{OwnerServerCLI, OwnerLegacyInit, false},
		{OwnerRollbackPending, OwnerServerCLI, false},
	}
	for _, c := range cases {
		st := newTestStore(t)
		mustSet(t, st, "svc", c.from)
		err := st.Transition(testEnv, testNode, "svc", c.from, c.to)
		if c.ok && err != nil {
			t.Errorf("transition %q->%q failed: %v", c.from, c.to, err)
			continue
		}
		if !c.ok && !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("transition %q->%q err = %v, want ErrInvalidTransition", c.from, c.to, err)
		}
		if c.ok {
			o, _ := st.Get(testEnv, testNode, "svc")
			if o.Owner != c.to {
				t.Errorf("owner = %q, want %q", o.Owner, c.to)
			}
		}
	}
}

func TestTransitionWrongCurrentOwner(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "svc", OwnerServerCLI)
	err := st.Transition(testEnv, testNode, "svc", OwnerLegacyInit, OwnerMigrationFrozen)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestCanOperate(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "servercli-svc", OwnerServerCLI)
	mustSet(t, st, "adopting-svc", OwnerAdopting)
	mustSet(t, st, "legacy-svc", OwnerLegacyInit)
	mustSet(t, st, "rollback-svc", OwnerRollbackPending)

	if err := st.CanOperate(testEnv, testNode, "servercli-svc"); err != nil {
		t.Fatalf("servercli owner blocked: %v", err)
	}
	for _, svc := range []string{"adopting-svc", "legacy-svc", "rollback-svc"} {
		if err := st.CanOperate(testEnv, testNode, svc); !errors.Is(err, ErrBlocked) {
			t.Errorf("CanOperate(%s) = %v, want ErrBlocked", svc, err)
		}
	}
	if err := st.CanOperate(testEnv, testNode, "missing"); !errors.Is(err, ErrNoOwnership) {
		t.Errorf("CanOperate(missing) = %v, want ErrNoOwnership", err)
	}
}

func TestServiceLockExcludesConcurrentOp(t *testing.T) {
	st := newTestStore(t)
	unlock1, err := st.Lock(testEnv, testNode, "postgres", "update")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()
	if _, err := st.Lock(testEnv, testNode, "postgres", "backup"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock err = %v, want ErrLocked", err)
	}
	// Different service is not blocked.
	unlock2, err := st.Lock(testEnv, testNode, "gitea", "update")
	if err != nil {
		t.Fatalf("different service blocked: %v", err)
	}
	unlock2()

	unlock1()
	// After release the lock is free again.
	unlock3, err := st.Lock(testEnv, testNode, "postgres", "backup")
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock3()
}

func TestNodeLockExcludesConcurrentOp(t *testing.T) {
	st := newTestStore(t)
	unlock1, err := st.LockAll(testEnv, testNode)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()
	if _, err := st.LockAll(testEnv, testNode); !errors.Is(err, ErrLocked) {
		t.Fatalf("second node lock err = %v, want ErrLocked", err)
	}
}

func TestAdoptPlanIsReadOnly(t *testing.T) {
	st := newTestStore(t)
	dir := filepath.Dir(st.Path())
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.AdoptPlan(context.Background(), testEnv, testNode, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.AlreadyAdopted {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("plan has no steps")
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("AdoptPlan wrote to disk: before=%d after=%d", len(before), len(after))
	}
	o, ok := st.Get(testEnv, testNode, "gitea")
	if ok {
		t.Fatalf("AdoptPlan created ownership: %+v", o)
	}
}

func TestAdoptPlanAlreadyAdopted(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "gitea", OwnerServerCLI)
	plan, err := st.AdoptPlan(context.Background(), testEnv, testNode, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.AlreadyAdopted || plan.Owner != OwnerServerCLI {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestFreezeMarkServerCLIRoundTrip(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "gitea", OwnerLegacyInit)

	if err := st.FreezeLegacy(testEnv, testNode, "gitea"); err != nil {
		t.Fatal(err)
	}
	o, _ := st.Get(testEnv, testNode, "gitea")
	if o.Owner != OwnerMigrationFrozen {
		t.Fatalf("owner after freeze = %q", o.Owner)
	}
	if o.LockedUntil.IsZero() {
		t.Fatal("LockedUntil not set")
	}
	marker := st.freezeMarkerPath("gitea")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("freeze marker missing: %v", err)
	}
	fi, err := os.Stat(marker)
	if err == nil && fi.Mode().Perm() != 0o600 {
		t.Fatalf("marker perms = %o, want 600", fi.Mode().Perm())
	}

	// Freeze from a non-legacy owner must fail.
	if err := st.FreezeLegacy(testEnv, testNode, "gitea"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double freeze err = %v", err)
	}

	if err := st.Transition(testEnv, testNode, "gitea", OwnerMigrationFrozen, OwnerAdopting); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkServerCLI(testEnv, testNode, "gitea", "digest-1", "gitea.token"); err != nil {
		t.Fatal(err)
	}
	o, _ = st.Get(testEnv, testNode, "gitea")
	if o.Owner != OwnerServerCLI || o.ConfigDigest != "digest-1" || o.SecretRef != "gitea.token" {
		t.Fatalf("after mark: %+v", o)
	}
	if o.AdoptCompletedAt.IsZero() {
		t.Fatal("AdoptCompletedAt not set")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("freeze marker not removed: %v", err)
	}
}

func TestMarkServerCLIRequiresAdopting(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "gitea", OwnerLegacyInit)
	if err := st.MarkServerCLI(testEnv, testNode, "gitea", "d", "s"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestRollbackAdopt(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "gitea", OwnerLegacyInit)
	if err := st.Transition(testEnv, testNode, "gitea", OwnerLegacyInit, OwnerMigrationFrozen); err != nil {
		t.Fatal(err)
	}
	if err := st.Transition(testEnv, testNode, "gitea", OwnerMigrationFrozen, OwnerAdopting); err != nil {
		t.Fatal(err)
	}
	if err := st.RollbackAdopt(testEnv, testNode, "gitea"); err != nil {
		t.Fatal(err)
	}
	o, _ := st.Get(testEnv, testNode, "gitea")
	if o.Owner != OwnerLegacyInit {
		t.Fatalf("owner after rollback = %q", o.Owner)
	}
	if !o.AdoptStartedAt.IsZero() || !o.AdoptCompletedAt.IsZero() {
		t.Fatalf("adopt timestamps not cleared: %+v", o)
	}
}

func TestServicesList(t *testing.T) {
	st := newTestStore(t)
	mustSet(t, st, "b", OwnerServerCLI)
	mustSet(t, st, "a", OwnerServerCLI)
	mustSet(t, st, "c", OwnerServerCLI)
	got := st.Services()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("Services = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Services = %v, want %v", got, want)
		}
	}
}

func TestInvalidServiceNamesRejected(t *testing.T) {
	st := newTestStore(t)
	for _, svc := range []string{"", "..", "a/b", ".", "../etc"} {
		if err := st.Set(testEnv, testNode, svc, Ownership{Owner: OwnerServerCLI}); !errors.Is(err, ErrInvalidService) {
			t.Errorf("Set(%q) err = %v, want ErrInvalidService", svc, err)
		}
		if _, err := st.Lock(testEnv, testNode, svc, "update"); !errors.Is(err, ErrInvalidService) {
			t.Errorf("Lock(%q) err = %v, want ErrInvalidService", svc, err)
		}
	}
}
