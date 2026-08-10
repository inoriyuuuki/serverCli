package modman

import "fmt"

// DepGraph orders modules by dependency (phase order first, then explicit
// depends_on edges). Cycles are rejected.
type DepGraph struct {
	mods map[string]*ModuleManifest
}

// NewDepGraph builds the graph from module manifests.
func NewDepGraph(mods []*ModuleManifest) (*DepGraph, error) {
	g := &DepGraph{mods: map[string]*ModuleManifest{}}
	for _, m := range mods {
		g.mods[m.ID] = m
	}
	for _, m := range mods {
		for _, dep := range m.DependsOn {
			if _, ok := g.mods[dep]; !ok {
				return nil, fmt.Errorf("module %s depends on unknown module %s", m.ID, dep)
			}
		}
	}
	return g, nil
}

// Ordered returns module ids in execution order.
func (g *DepGraph) Ordered() ([]string, error) {
	// Kahn's algorithm with phase as a secondary key.
	inDegree := map[string]int{}
	adj := map[string][]string{}
	for id := range g.mods {
		inDegree[id] = 0
	}
	for _, m := range g.mods {
		for _, dep := range m.DependsOn {
			adj[dep] = append(adj[dep], m.ID)
			inDegree[m.ID]++
		}
	}
	queue := []string{}
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	// Deterministic ordering: phase priority, then id.
	phaseRank := map[string]int{
		PhaseFoundationCore:     0,
		PhaseFoundationServices: 1,
		PhaseServices:           2,
	}
	less := func(a, b string) bool {
		pa, pb := phaseRank[g.mods[a].Phase], phaseRank[g.mods[b].Phase]
		if pa != pb {
			return pa < pb
		}
		return a < b
	}
	// simple insertion sort on queue
	for i := 1; i < len(queue); i++ {
		for j := i; j > 0 && less(queue[j], queue[j-1]); j-- {
			queue[j], queue[j-1] = queue[j-1], queue[j]
		}
	}
	var order []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		for _, next := range adj[n] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
				// re-sort queue after insertion
				for i := len(queue) - 1; i > 0 && less(queue[i], queue[i-1]); i-- {
					queue[i], queue[i-1] = queue[i-1], queue[i]
				}
			}
		}
	}
	if len(order) != len(g.mods) {
		return nil, fmt.Errorf("module dependency cycle detected (%d/%d ordered)", len(order), len(g.mods))
	}
	return order, nil
}

// Required returns the closure of dependencies for the given module (including
// the module itself), in execution order.
func (g *DepGraph) Required(moduleID string) ([]string, error) {
	order, err := g.Ordered()
	if err != nil {
		return nil, err
	}
	need := map[string]bool{moduleID: true}
	var frontier = []string{moduleID}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		for _, dep := range g.mods[cur].DependsOn {
			if !need[dep] {
				need[dep] = true
				frontier = append(frontier, dep)
			}
		}
	}
	var out []string
	for _, id := range order {
		if need[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// Module returns the manifest for id.
func (g *DepGraph) Module(id string) (*ModuleManifest, bool) {
	m, ok := g.mods[id]
	return m, ok
}
