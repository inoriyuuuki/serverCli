package agent

import (
	"os"
	"path/filepath"
	"testing"

	"servercli/internal/ownership"
)

// TestCollectOwnershipReport verifies the agent maps the local ownership store
// into heartbeat payload entries, and reports empty (non-nil) when absent.
func TestCollectOwnershipReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ownership.json")
	st := ownership.NewStore(path)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("prod", "node-1", "docker", ownership.Ownership{
		Environment: "prod", Node: "node-1", Service: "docker", Owner: ownership.OwnerServerCLI, ConfigDigest: "abc",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	reports := collectOwnershipReport(path)
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	r := reports[0]
	if r["service"] != "docker" || r["owner"] != ownership.OwnerServerCLI || r["environment"] != "prod" {
		t.Fatalf("report = %v", r)
	}
	// Missing file -> empty non-nil (so control plane can reconcile).
	empty := collectOwnershipReport(filepath.Join(dir, "missing.json"))
	if empty == nil || len(empty) != 0 {
		t.Fatalf("missing store should yield empty non-nil, got %#v", empty)
	}
	// Corrupt file -> same.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o600)
	if got := collectOwnershipReport(bad); got == nil || len(got) != 0 {
		t.Fatalf("corrupt store should yield empty non-nil, got %#v", got)
	}
}
