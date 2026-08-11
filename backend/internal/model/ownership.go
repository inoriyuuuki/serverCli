package model

// ServiceOwnership is one service's owner state on a node, as reported by the
// node agent and stored on the control plane. Owner values follow the
// ownership package state machine (legacy-init / migration-frozen / adopting /
// servercli / rollback-pending).
type ServiceOwnership struct {
	NodeID       string `json:"node_id"`
	Service      string `json:"service"`
	Owner        string `json:"owner"`
	ConfigDigest string `json:"config_digest,omitempty"`
	Environment  string `json:"environment,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}
