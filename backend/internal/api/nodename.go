package api

import (
	"context"

	"servercli/internal/model"
)

// nodeDisplayName returns the alias-first display name for a node, matching
// the UI convention: 别名 > 实例名 > 主机名 > ID.
func nodeDisplayName(n *model.Node) string {
	if n == nil {
		return ""
	}
	if n.Alias != "" {
		return n.Alias
	}
	if n.InstanceName != "" {
		return n.InstanceName
	}
	if n.Hostname != "" {
		return n.Hostname
	}
	return n.ID
}

// nodeDisplayNames resolves alias-first display names for a batch of node IDs.
func (s *Server) nodeDisplayNames(ctx context.Context, nodeIDs []string) map[string]string {
	names := map[string]string{}
	unique := make([]string, 0, len(nodeIDs))
	seen := map[string]bool{}
	for _, id := range nodeIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return names
	}
	nodes, err := s.store.NodesByIDs(ctx, unique)
	if err != nil {
		return names
	}
	for id, n := range nodes {
		names[id] = nodeDisplayName(n)
	}
	return names
}

func (s *Server) enrichAuditNames(ctx context.Context, events ...*model.AuditEvent) {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		if e != nil {
			ids = append(ids, e.NodeID)
		}
	}
	names := s.nodeDisplayNames(ctx, ids)
	for _, e := range events {
		if e != nil {
			e.NodeName = names[e.NodeID]
		}
	}
}
