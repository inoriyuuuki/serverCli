package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// foundationDeps mirrors the dependency edges of the real module manifests so
// the dependency-graph order equals FoundationCoreOrder.
var foundationDeps = map[string][]string{
	"v2ray":         nil,
	"docker":        {"v2ray"},
	"postgres":      {"docker"},
	"caddy":         {"postgres"},
	"control-plane": {"postgres", "caddy"},
	"agent":         {"control-plane", "caddy"},
	"gitea":         {"control-plane", "postgres", "caddy"},
}

// writeModule creates a minimal valid module below root. preflightCode < 0
// omits the preflight operation/script entirely.
func writeModule(t *testing.T, root, id string, deps []string, preflightCode int, manifestExtra string) string {
	t.Helper()
	modDir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(modDir, "operations"), 0o755); err != nil {
		t.Fatal(err)
	}
	var depYAML strings.Builder
	for _, d := range deps {
		fmt.Fprintf(&depYAML, "    - %s\n", d)
	}
	manifest := fmt.Sprintf(`id: %s
version: 1.0.0
phase: foundation-core
depends_on:
%sdelivery: env
config_fields:
  - name: DUMMY
    type: string
    required: false
secret_fields: []
operations:
  install:
    entry: operations/install.sh
`, id, depYAML.String())
	if preflightCode >= 0 {
		manifest += `  preflight:
    entry: operations/preflight.sh
`
	}
	manifest += `  verify:
    entry: operations/verify.sh
concurrency: node
` + manifestExtra

	if err := os.WriteFile(filepath.Join(modDir, "module.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"install.sh": "#!/bin/sh\nset -euo pipefail\nexit 0\n",
		"verify.sh":  "#!/bin/sh\nset -euo pipefail\nexit 0\n",
	}
	if preflightCode >= 0 {
		scripts["preflight.sh"] = fmt.Sprintf("#!/bin/sh\nset -euo pipefail\necho 'preflight %s'\nexit %d\n", id, preflightCode)
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(modDir, "operations", name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return modDir
}

// writeFoundation creates all 7 foundation modules in dependency order.
func writeFoundation(t *testing.T, root string, preflightCodes map[string]int) {
	t.Helper()
	for _, id := range FoundationCoreOrder() {
		code := 0
		if preflightCodes != nil {
			if c, ok := preflightCodes[id]; ok {
				code = c
			}
		}
		writeModule(t, root, id, foundationDeps[id], code, "")
	}
}

func TestFoundationCoreOrder(t *testing.T) {
	want := []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent", "gitea"}
	if got := FoundationCoreOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("FoundationCoreOrder = %v, want %v", got, want)
	}
}

func TestCoreReadyGate(t *testing.T) {
	want := []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent"}
	if got := CoreReadyGate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CoreReadyGate = %v, want %v", got, want)
	}
	if containsString(CoreReadyGate(), "gitea") {
		t.Fatal("gitea must not be part of the core_ready hard gate")
	}
}

func TestNewRegistryLoadsAllAndModules(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	reg, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Dir() != root {
		t.Fatalf("Dir = %q, want %q", reg.Dir(), root)
	}
	for _, id := range FoundationCoreOrder() {
		m, ok := reg.Module(id)
		if !ok {
			t.Fatalf("module %s not found", id)
		}
		if m.ID != id || m.Dir != filepath.Join(root, id) {
			t.Fatalf("module %s metadata mismatch: %+v", id, m)
		}
	}
	if _, ok := reg.Module("does-not-exist"); ok {
		t.Fatal("unknown module reported as present")
	}
}

func TestOrderedMatchesFoundationOrder(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	reg, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	order, err := reg.Ordered(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, FoundationCoreOrder()) {
		t.Fatalf("Ordered = %v, want %v", order, FoundationCoreOrder())
	}
}

func TestRequiredClosure(t *testing.T) {
	root := t.TempDir()
	writeFoundation(t, root, nil)
	reg, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	req, err := reg.Required("gitea")
	if err != nil {
		t.Fatal(err)
	}
	// gitea's closure excludes agent: gitea has no dependency edge to agent.
	wantGitea := []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "gitea"}
	if !reflect.DeepEqual(req, wantGitea) {
		t.Fatalf("Required(gitea) = %v, want %v", req, wantGitea)
	}
	req, err = reg.Required("agent")
	if err != nil {
		t.Fatal(err)
	}
	wantAgent := []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent"}
	if !reflect.DeepEqual(req, wantAgent) {
		t.Fatalf("Required(agent) = %v, want %v", req, wantAgent)
	}
	req, err = reg.Required("postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"v2ray", "docker", "postgres"}
	if !reflect.DeepEqual(req, want) {
		t.Fatalf("Required(postgres) = %v, want %v", req, want)
	}
}

func TestRegistryRejectsInvalidManifest(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "docker", nil, 0, "")
	path := filepath.Join(root, "docker", "module.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(raw), "phase: foundation-core", "phase: bogus", 1)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(root); err == nil {
		t.Fatal("expected validation error for invalid phase")
	}
}

func TestRegistryRejectsUnknownDependency(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "docker", []string{"ghost"}, 0, "")
	if _, err := NewRegistry(root); err == nil {
		t.Fatal("expected unknown dependency error")
	}
}

func TestRegistryOrderedRejectsCycle(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "a", []string{"b"}, 0, "")
	writeModule(t, root, "b", []string{"a"}, 0, "")
	reg, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Ordered(context.Background()); err == nil {
		t.Fatal("expected dependency cycle error")
	}
}

func TestRegistryMissingDir(t *testing.T) {
	if _, err := NewRegistry(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing modules dir")
	}
}

func TestRealModulesDirConsistent(t *testing.T) {
	root := repoModulesDir(t)
	if root == "" {
		t.Skip("modules dir not found")
	}
	reg, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("real modules dir invalid: %v", err)
	}
	order, err := reg.Ordered(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, FoundationCoreOrder()) {
		t.Fatalf("real modules order = %v, want %v", order, FoundationCoreOrder())
	}
	for _, id := range FoundationCoreOrder() {
		m, ok := reg.Module(id)
		if !ok {
			t.Fatalf("real modules missing %s", id)
		}
		if _, ok := m.Operations["install"]; !ok {
			t.Fatalf("module %s missing required install operation", id)
		}
	}
}

// repoModulesDir locates the repository modules/ dir from the test source path.
func repoModulesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "modules"))
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	return ""
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
