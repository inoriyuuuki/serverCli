package modman

import "testing"

func testMod(id, phase string, deps ...string) *ModuleManifest {
	return &ModuleManifest{ID: id, Version: "1.0.0", Phase: phase, DependsOn: deps, Delivery: DeliveryEnv, Operations: map[string]Operation{}}
}

func TestTopoOrderAndCycle(t *testing.T) {
	g, err := NewDepGraph([]*ModuleManifest{
		testMod("gitea", PhaseFoundationServices, "controlplane"),
		testMod("controlplane", PhaseFoundationCore, "postgres"),
		testMod("postgres", PhaseFoundationCore, "docker"),
		testMod("docker", PhaseFoundationCore),
		testMod("v2ray", PhaseFoundationCore),
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.Ordered()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["docker"] > pos["postgres"] || pos["postgres"] > pos["controlplane"] || pos["controlplane"] > pos["gitea"] {
		t.Fatalf("bad order: %v", order)
	}
	// Cycle detection.
	g2, err := NewDepGraph([]*ModuleManifest{
		testMod("a", PhaseFoundationCore, "b"),
		testMod("b", PhaseFoundationCore, "a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g2.Ordered(); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestRequiredClosure(t *testing.T) {
	g, _ := NewDepGraph([]*ModuleManifest{
		testMod("gitea", PhaseFoundationServices, "controlplane"),
		testMod("controlplane", PhaseFoundationCore, "postgres"),
		testMod("postgres", PhaseFoundationCore, "docker"),
		testMod("docker", PhaseFoundationCore),
	})
	req, err := g.Required("gitea")
	if err != nil {
		t.Fatal(err)
	}
	if len(req) != 4 {
		t.Fatalf("closure = %v want 4 modules", req)
	}
	if req[len(req)-1] != "gitea" {
		t.Fatalf("gitea should be last: %v", req)
	}
}
