package bundle

import (
	"testing"

	"servercli/internal/initstate"
)

func TestResumeGuardMatches(t *testing.T) {
	st := initstate.New("op-1", "bundle-abc", "digest-1", "1.5.0")
	if err := ResumeGuard(st, "bundle-abc", "digest-1"); err != nil {
		t.Fatalf("ResumeGuard matching state: %v", err)
	}
}

func TestResumeGuardRejectsDifferentBundle(t *testing.T) {
	st := initstate.New("op-1", "bundle-abc", "digest-1", "1.5.0")
	if err := ResumeGuard(st, "bundle-xyz", "digest-1"); err == nil {
		t.Fatal("expected error for different bundle_id")
	}
}

func TestResumeGuardRejectsDifferentDigest(t *testing.T) {
	st := initstate.New("op-1", "bundle-abc", "digest-1", "1.5.0")
	if err := ResumeGuard(st, "bundle-abc", "digest-2"); err == nil {
		t.Fatal("expected error for different input digest")
	}
}

func TestResumeGuardAllowsFreshState(t *testing.T) {
	st := initstate.New("", "", "", "")
	if err := ResumeGuard(st, "bundle-abc", "digest-1"); err != nil {
		t.Fatalf("fresh state should allow resume: %v", err)
	}
}

func TestResumeGuardRejectsPartialState(t *testing.T) {
	// Recorded bundle_id without input digest must not silently pass.
	st := initstate.New("op-1", "bundle-abc", "", "1.5.0")
	if err := ResumeGuard(st, "bundle-abc", "digest-1"); err == nil {
		t.Fatal("expected error for partial state")
	}
	if err := ResumeGuard(nil, "bundle-abc", "digest-1"); err == nil {
		t.Fatal("expected error for nil state")
	}
}
