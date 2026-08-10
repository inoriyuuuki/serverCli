// Package modules implements the ServerCLI Foundation module registry and the
// init preflight checks.
//
// The registry loads the module manifests from modules/ (id/version/phase/
// depends_on/config_fields/secret_fields/delivery/operations/healthcheck/
// backup/concurrency), builds the dependency graph and exposes the fixed
// Foundation execution order and the core_ready hard gate.
package modules

import (
	"context"

	"servercli/internal/modman"
)

// Registry is the loaded view of the module tree.
type Registry struct {
	graph *modman.DepGraph
	dir   string
}

// NewRegistry loads every module manifest below modulesDir, validates it with
// modman.Validate and builds the dependency graph.
func NewRegistry(modulesDir string) (*Registry, error) {
	mods, err := modman.LoadAll(modulesDir)
	if err != nil {
		return nil, err
	}
	graph, err := modman.NewDepGraph(mods)
	if err != nil {
		return nil, err
	}
	return &Registry{graph: graph, dir: modulesDir}, nil
}

// FoundationCoreOrder is the exact Foundation module execution order:
// v2ray -> docker -> postgres -> caddy(gateway) -> control-plane -> agent -> gitea.
func FoundationCoreOrder() []string {
	return []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent", "gitea"}
}

// CoreReadyGate is the hard core_ready gate: every module here must be ready
// before core_ready is recorded. gitea is intentionally excluded.
func CoreReadyGate() []string {
	return []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent"}
}

// Ordered returns all module ids in dependency-graph order (phase first, then
// depends_on edges; cycles are rejected). ctx is reserved for future
// cancellation-aware ordering.
func (r *Registry) Ordered(ctx context.Context) ([]string, error) {
	return r.graph.Ordered()
}

// Module returns the manifest for id.
func (r *Registry) Module(id string) (*modman.ModuleManifest, bool) {
	return r.graph.Module(id)
}

// Required returns the transitive dependency closure for id (including id
// itself) in execution order.
func (r *Registry) Required(id string) ([]string, error) {
	return r.graph.Required(id)
}

// Dir returns the modules directory the registry was loaded from.
func (r *Registry) Dir() string {
	return r.dir
}
