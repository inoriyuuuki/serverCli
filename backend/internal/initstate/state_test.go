package initstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTransitionValidation(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{StateNotInitialized, StateInitializing, true},
		{StateNotInitialized, StateReady, false},
		{StateInitializing, StateCoreReady, true},
		{StateInitializing, StateDegraded, true},
		{StateCoreReady, StateReady, true},
		{StateCoreReady, StateFailed, true},
		{StateReady, StateInitializing, true},
		{StateReady, StateNotInitialized, false},
		{StateBlocked, StateInitializing, true},
		{StateBlocked, StateReady, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%q,%q)=%v want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := st.State()
	if s.Overall != StateNotInitialized {
		t.Fatalf("fresh state overall = %q", s.Overall)
	}
	if err := s.SetOverall(StateInitializing); err != nil {
		t.Fatal(err)
	}
	s.OperationID = "op-1"
	s.BundleID = "bundle-1"
	s.UpsertStep(Step{ModuleID: "docker", Operation: "install", Status: StepRunning, Attempt: 1})
	s.SetCommitPoint("docker", "digest=abc")
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2 := st2.State()
	if s2.Overall != StateInitializing || s2.BundleID != "bundle-1" {
		t.Fatalf("roundtrip mismatch: %+v", s2)
	}
	if s2.Step("docker") == nil || s2.Step("docker").Status != StepRunning {
		t.Fatalf("step missing: %+v", s2.Steps)
	}
	if s2.CommitPoints["docker"] != "digest=abc" {
		t.Fatalf("commit point missing: %v", s2.CommitPoints)
	}
}

func TestChecksumCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Save()
	st.Close()
	// Tamper with the state body.
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"overall":"ready"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected corruption error")
	}
}

func TestConcurrentLockRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st1.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("expected ErrConcurrent")
	}
}

func TestReconcileAfterCrash(t *testing.T) {
	s := New("op-1", "b-1", "d-1", "v1")
	s.SetOverall(StateInitializing)
	s.UpsertStep(Step{ModuleID: "docker", Status: StepSucceeded})
	s.UpsertStep(Step{ModuleID: "postgres", Status: StepRunning, Retryable: true})
	ReconcileAfterCrash(s)
	if s.Step("postgres").Status != StepFailed {
		t.Fatalf("running step not failed after reconcile: %+v", s.Step("postgres"))
	}
	if !s.Step("postgres").Retryable {
		t.Fatal("postgres step should be retryable")
	}
	if s.Overall != StateFailed {
		t.Fatalf("overall = %q want failed", s.Overall)
	}
}
