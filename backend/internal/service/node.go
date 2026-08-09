package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/store"
)

// ClaimTTL is how long a one-time claim token is valid.
const ClaimTTL = 30 * time.Minute

// NodeService implements node enrollment, claim, heartbeat and management.
type NodeService struct {
	store    *store.Store
	cfg      *config.Config
	log      *slog.Logger
	auditor  *Auditor
	settings *SettingsService
	envID    string
	master   []byte
}

// NewNodeService builds the service.
func NewNodeService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, settings *SettingsService) (*NodeService, error) {
	master, err := MasterKey(cfg)
	if err != nil {
		return nil, err
	}
	return &NodeService{
		store:    st,
		cfg:      cfg,
		log:      log,
		auditor:  auditor,
		settings: settings,
		envID:    cfg.InstanceName + "-env",
		master:   master,
	}, nil
}

// EnvID returns the environment identifier.
func (s *NodeService) EnvID() string { return s.envID }

// claimTokenFor deterministically derives the one-time claim token for an
// enrollment. Only hashes are stored in the database.
func (s *NodeService) claimTokenFor(enrollmentID string) string {
	return claimTokenFor(s.master, enrollmentID)
}

// AddressInput is a reported node address.
type AddressInput struct {
	Address     string `json:"address"`
	AddressType string `json:"address_type"`
	ServicePort int    `json:"service_port"`
}

// EnrollmentInput is the agent's registration request.
type EnrollmentInput struct {
	InstanceRequestID string         `json:"instance_request_id"`
	Hostname          string         `json:"hostname"`
	InstanceName      string         `json:"instance_name"`
	RequestedRole     string         `json:"requested_role"`
	AgentVersion      string         `json:"agent_version"`
	OSName            string         `json:"os_name"`
	OSVersion         string         `json:"os_version"`
	Arch              string         `json:"arch"`
	ReportedAddresses []AddressInput `json:"reported_addresses"`
	FrontendPort      int            `json:"frontend_port"`
	BackendPort       int            `json:"backend_port"`
	InstancePublicKey string         `json:"instance_public_key"`
}

// CreateEnrollment registers a node enrollment idempotently.
func (s *NodeService) CreateEnrollment(ctx context.Context, in EnrollmentInput, sourceIP string) (*model.NodeEnrollment, error) {
	if in.InstanceRequestID == "" {
		return nil, ErrBadRequest
	}
	if in.RequestedRole != "primary" && in.RequestedRole != "child" {
		return nil, ErrBadRequest
	}
	if existing, err := s.store.EnrollmentByInstanceRequest(ctx, s.envID, in.InstanceRequestID); err == nil {
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if in.RequestedRole == "primary" {
		if existing, err := s.store.FindEnabledPrimary(ctx, s.envID); err == nil && existing != nil {
			return nil, ErrConflict
		}
	}
	addrs, err := json.Marshal(in.ReportedAddresses)
	if err != nil {
		return nil, ErrBadRequest
	}
	e := &model.NodeEnrollment{
		ID:                    model.NewUUID(),
		InstanceRequestID:     in.InstanceRequestID,
		EnvironmentID:         s.envID,
		RequestedRole:         in.RequestedRole,
		Hostname:              in.Hostname,
		InstanceName:          in.InstanceName,
		SourceIP:              sourceIP,
		ReportedAddressesJSON: string(addrs),
		AgentVersion:          in.AgentVersion,
		OSName:                in.OSName,
		OSVersion:             in.OSVersion,
		Arch:                  in.Arch,
		FrontendPort:          in.FrontendPort,
		BackendPort:           in.BackendPort,
		Status:                model.EnrollmentPending,
		InstancePublicKey:     in.InstancePublicKey,
	}
	if err := s.store.CreateEnrollment(ctx, e); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorNode, Action: "node.enrollment", ResourceType: "node_enrollment",
		ResourceID: e.ID, SourceIP: sourceIP, Summary: "node enrollment requested",
	})
	return e, nil
}

// Enrollment returns an enrollment; when approved and unclaimed it includes
// the one-time claim token so the owning agent can claim its identity.
func (s *NodeService) Enrollment(ctx context.Context, id string) (*model.NodeEnrollment, string, error) {
	e, err := s.store.EnrollmentByID(ctx, id)
	if err != nil {
		return nil, "", ErrNotFound
	}
	claimToken := ""
	if e.Status == model.EnrollmentApproved && e.ClaimTokenHash != "" {
		if e.ClaimExpiresAt != nil && e.ClaimExpiresAt.After(time.Now().UTC()) {
			claimToken = s.claimTokenFor(e.ID)
		}
	}
	return e, claimToken, nil
}

// ApproveEnrollment approves a pending enrollment, creating the node and a
// one-time claim token.
func (s *NodeService) ApproveEnrollment(ctx context.Context, id, adminID, note string) (*model.NodeEnrollment, error) {
	e, err := s.store.EnrollmentByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if e.Status != model.EnrollmentPending {
		return nil, ErrTerminal
	}
	if e.RequestedRole == "primary" {
		if existing, err := s.store.FindEnabledPrimary(ctx, s.envID); err == nil && existing != nil {
			return nil, ErrConflict
		}
	}
	claimToken := s.claimTokenFor(e.ID)
	credential := credentialFor(claimToken)
	now := time.Now().UTC()
	expires := now.Add(ClaimTTL)
	instanceName := e.InstanceName
	if instanceName == "" {
		instanceName = e.Hostname
	}
	if instanceName == "" {
		instanceName = e.RequestedRole + "-" + e.ID[:8]
	}
	node := &model.Node{
		ID:                model.NewUUID(),
		EnvironmentID:     s.envID,
		InstanceName:      instanceName,
		Role:              e.RequestedRole,
		Hostname:          e.Hostname,
		Status:            model.NodeStatusPending,
		Enabled:           true,
		AgentVersion:      e.AgentVersion,
		OSName:            e.OSName,
		OSVersion:         e.OSVersion,
		Arch:              e.Arch,
		FrontendPort:      e.FrontendPort,
		BackendPort:       e.BackendPort,
		CredentialHash:    security.HashToken(credential),
		CredentialPrefix:  security.Prefix(credential, 8),
		CredentialVersion: 1,
	}
	if err := s.store.CreateNode(ctx, node); err != nil {
		return nil, err
	}
	e.Status = model.EnrollmentApproved
	e.ReviewedBy = adminID
	e.ReviewedAt = &now
	e.ReviewNote = note
	e.ClaimTokenHash = security.HashToken(claimToken)
	e.ClaimExpiresAt = &expires
	e.NodeID = node.ID
	if err := s.store.UpdateEnrollment(ctx, e); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "node.approve",
		ResourceType: "node", ResourceID: node.ID, Summary: "enrollment approved",
		Details: map[string]any{"enrollment_id": e.ID, "role": e.RequestedRole},
	})
	return e, nil
}

// RejectEnrollment rejects a pending enrollment.
func (s *NodeService) RejectEnrollment(ctx context.Context, id, adminID, note string) error {
	e, err := s.store.EnrollmentByID(ctx, id)
	if err != nil {
		return ErrNotFound
	}
	if e.Status != model.EnrollmentPending {
		return ErrTerminal
	}
	now := time.Now().UTC()
	e.Status = model.EnrollmentRejected
	e.ReviewedBy = adminID
	e.ReviewedAt = &now
	e.ReviewNote = note
	if err := s.store.UpdateEnrollment(ctx, e); err != nil {
		return err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "node.reject",
		ResourceType: "node_enrollment", ResourceID: e.ID, Summary: "enrollment rejected",
		Details: map[string]any{"note": note},
	})
	return nil
}

// ClaimResult is returned on successful enrollment claim.
type ClaimResult struct {
	NodeID         string `json:"node_id"`
	NodeCredential string `json:"node_credential"`
	InstanceName   string `json:"instance_name"`
}

// ClaimEnrollment verifies the claim token and proof of key possession, then
// returns the node identity.
func (s *NodeService) ClaimEnrollment(ctx context.Context, id, claimToken, proofSignature, proofTimestamp, publicKey string) (*ClaimResult, error) {
	e, err := s.store.EnrollmentByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if e.Status != model.EnrollmentApproved {
		return nil, ErrForbidden
	}
	if e.ClaimExpiresAt == nil || e.ClaimExpiresAt.Before(time.Now().UTC()) {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorNode, Action: "node.claim", ResourceType: "node_enrollment",
			ResourceID: e.ID, Summary: "claim rejected: token expired", RiskLevel: RiskHigh,
		})
		return nil, ErrForbidden
	}
	expected := s.claimTokenFor(e.ID)
	if !security.ConstantTimeEqual(claimToken, expected) ||
		!security.ConstantTimeEqual(security.HashToken(claimToken), e.ClaimTokenHash) {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorNode, Action: "node.claim", ResourceType: "node_enrollment",
			ResourceID: e.ID, Summary: "claim rejected: bad or used token", RiskLevel: RiskHigh,
		})
		return nil, ErrForbidden
	}
	if e.InstancePublicKey == "" || !verifyClaimProof(e.InstancePublicKey, proofTimestamp, id, proofSignature) {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorNode, Action: "node.claim", ResourceType: "node_enrollment",
			ResourceID: e.ID, Summary: "claim rejected: bad proof", RiskLevel: RiskHigh,
		})
		return nil, ErrForbidden
	}
	node, err := s.store.NodeByID(ctx, e.NodeID)
	if err != nil {
		return nil, ErrNotFound
	}
	credential := credentialFor(claimToken)
	if !security.ConstantTimeEqual(security.HashToken(credential), node.CredentialHash) {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorNode, Action: "node.claim", ResourceType: "node_enrollment",
			ResourceID: e.ID, Summary: "claim rejected: credential mismatch", RiskLevel: RiskHigh,
		})
		return nil, ErrForbidden
	}
	now := time.Now().UTC()
	e.Status = model.EnrollmentClaimed
	e.ClaimedAt = &now
	e.ClaimTokenHash = ""
	e.ClaimExpiresAt = nil
	if err := s.store.UpdateEnrollment(ctx, e); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorNode, ActorID: node.ID, Action: "node.claim",
		ResourceType: "node", ResourceID: node.ID, Summary: "node identity claimed",
	})
	return &ClaimResult{NodeID: node.ID, NodeCredential: credential, InstanceName: node.InstanceName}, nil
}

// verifyClaimProof verifies the ed25519 signature over "ts|enrollment_id".
func verifyClaimProof(publicKeyB64, ts, enrollmentID, signatureB64 string) bool {
	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return false
	}
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := ts + "|" + enrollmentID
	return ed25519.Verify(ed25519.PublicKey(pub), []byte(msg), sig)
}

// HeartbeatInput is the agent heartbeat payload.
type HeartbeatInput struct {
	Hostname         string         `json:"hostname"`
	AgentVersion     string         `json:"agent_version"`
	OSName           string         `json:"os_name"`
	OSVersion        string         `json:"os_version"`
	Arch             string         `json:"arch"`
	Addresses        []AddressInput `json:"addresses"`
	CPUUsagePercent  float64        `json:"cpu_usage_percent"`
	MemoryTotalBytes int64          `json:"memory_total_bytes"`
	MemoryUsedBytes  int64          `json:"memory_used_bytes"`
	DiskTotalBytes   int64          `json:"disk_total_bytes"`
	DiskUsedBytes    int64          `json:"disk_used_bytes"`
	Load1            float64        `json:"load_1"`
	Load5            float64        `json:"load_5"`
	Load15           float64        `json:"load_15"`
	UptimeSeconds    int64          `json:"uptime_seconds"`
	TimeOffsetMS     int64          `json:"time_offset_ms"`
	Summary          map[string]any `json:"summary"`
	CommandsHash     string         `json:"commands_hash"`
}

// LeaseInstallOp is a pending key install instruction.
type LeaseInstallOp struct {
	LeaseID           string    `json:"lease_id"`
	PublicKey         string    `json:"public_key"`
	PermissionProfile string    `json:"permission_profile"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// LeaseRemoveOp is a pending key removal instruction.
type LeaseRemoveOp struct {
	LeaseID string `json:"lease_id"`
	Reason  string `json:"reason"`
}

// HeartbeatResponse is returned to the agent.
type HeartbeatResponse struct {
	NodeID     string           `json:"node_id"`
	Status     string           `json:"status"`
	ServerTime time.Time        `json:"server_time"`
	Install    []LeaseInstallOp `json:"install"`
	Remove     []LeaseRemoveOp  `json:"remove"`
}

// Heartbeat processes an authenticated agent heartbeat.
func (s *NodeService) Heartbeat(ctx context.Context, nodeID string, in HeartbeatInput, sourceIP string) (*HeartbeatResponse, error) {
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !node.Enabled {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorNode, ActorID: node.ID, Action: "node.heartbeat",
			ResourceType: "node", ResourceID: node.ID, SourceIP: sourceIP,
			Summary: "heartbeat from disabled node rejected", RiskLevel: RiskHigh,
		})
		return nil, ErrForbidden
	}
	now := time.Now().UTC()
	hb := &model.NodeHeartbeat{
		NodeID:           node.ID,
		CPUUsagePercent:  in.CPUUsagePercent,
		MemoryTotalBytes: in.MemoryTotalBytes,
		MemoryUsedBytes:  in.MemoryUsedBytes,
		DiskTotalBytes:   in.DiskTotalBytes,
		DiskUsedBytes:    in.DiskUsedBytes,
		Load1:            in.Load1,
		Load5:            in.Load5,
		Load15:           in.Load15,
		UptimeSeconds:    in.UptimeSeconds,
		TimeOffsetMS:     in.TimeOffsetMS,
	}
	if in.Summary != nil {
		if raw, err := json.Marshal(in.Summary); err == nil {
			hb.SummaryJSON = string(raw)
		}
	}
	if err := s.store.CreateHeartbeat(ctx, hb); err != nil {
		return nil, err
	}
	if node.Status == model.NodeStatusOffline || node.Status == model.NodeStatusPending {
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorNode, ActorID: node.ID, Action: "node.online",
			ResourceType: "node", ResourceID: node.ID, SourceIP: sourceIP, Summary: "node back online",
		})
	}
	node.Hostname = in.Hostname
	node.AgentVersion = in.AgentVersion
	node.OSName = in.OSName
	node.OSVersion = in.OSVersion
	node.Arch = in.Arch
	node.Status = model.NodeStatusOnline
	node.LastHeartbeatAt = &now
	node.LastOnlineAt = &now
	if err := s.store.UpdateNode(ctx, node); err != nil {
		return nil, err
	}
	addrs := make([]*model.NodeAddress, 0, len(in.Addresses))
	for i, a := range in.Addresses {
		atype := a.AddressType
		if atype == "" {
			atype = "reported"
		}
		addrs = append(addrs, &model.NodeAddress{
			Address:     a.Address,
			AddressType: atype,
			ServicePort: a.ServicePort,
			IsPreferred: i == 0,
		})
	}
	if len(addrs) > 0 {
		_ = s.store.ReplaceAddresses(ctx, node.ID, addrs)
	}
	install, remove, err := s.leaseOps(ctx, node.ID)
	if err != nil {
		s.log.Warn("lease ops computation failed", "error", err, "node_id", node.ID)
	}
	return &HeartbeatResponse{
		NodeID:     node.ID,
		Status:     node.Status,
		ServerTime: now,
		Install:    install,
		Remove:     remove,
	}, nil
}

// leaseOps returns keys to install (active leases not yet installed) and keys
// to remove (terminal leases still installed).
func (s *NodeService) leaseOps(ctx context.Context, nodeID string) ([]LeaseInstallOp, []LeaseRemoveOp, error) {
	all, err := s.store.ListLeases(ctx, nodeID, "", 0, 0)
	if err != nil {
		return nil, nil, err
	}
	var install []LeaseInstallOp
	var remove []LeaseRemoveOp
	for _, l := range all {
		switch {
		case l.Status == model.LeaseActive && !l.KeyInstalled:
			install = append(install, LeaseInstallOp{
				LeaseID:           l.ID,
				PublicKey:         l.PublicKey,
				PermissionProfile: l.PermissionProfile,
				ExpiresAt:         l.ExpiresAt,
			})
		case l.KeyInstalled && (l.Status == model.LeaseRevoked || l.Status == model.LeaseExpired ||
			l.Status == model.LeaseDisconnected || l.Status == model.LeaseFailed):
			reason := "lease " + l.Status
			if l.RevokeReason != "" {
				reason = l.RevokeReason
			}
			remove = append(remove, LeaseRemoveOp{LeaseID: l.ID, Reason: reason})
		}
	}
	return install, remove, nil
}

// CommandsSnapshotInput is one command record from the agent.
type CommandsSnapshotInput struct {
	CommandID           string `json:"command_id"`
	CommandVersion      string `json:"command_version"`
	CapabilityID        string `json:"capability_id"`
	Category            string `json:"category"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	ParameterSchemaJSON string `json:"parameter_schema_json"`
	PermissionProfile   string `json:"permission_profile"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	MaxOutputBytes      int64  `json:"max_output_bytes"`
	Enabled             bool   `json:"enabled"`
	ManifestHash        string `json:"manifest_hash"`
	ExecutableHash      string `json:"executable_hash"`
}

// CommandsSnapshot replaces the node's command registry.
func (s *NodeService) CommandsSnapshot(ctx context.Context, nodeID string, commands []CommandsSnapshotInput) (int64, error) {
	keep := map[string]bool{}
	var added int64
	for _, c := range commands {
		if c.CommandID == "" || c.CommandVersion == "" {
			continue
		}
		if c.PermissionProfile == "" {
			c.PermissionProfile = model.ProfileReadOnly
		}
		if c.TimeoutSeconds <= 0 {
			c.TimeoutSeconds = 60
		}
		if c.MaxOutputBytes <= 0 {
			c.MaxOutputBytes = 262144
		}
		keep[c.CommandID+"\x00"+c.CommandVersion] = true
		rec := &model.NodeCommand{
			NodeID:              nodeID,
			CommandID:           c.CommandID,
			CommandVersion:      c.CommandVersion,
			CapabilityID:        c.CapabilityID,
			Category:            c.Category,
			Title:               c.Title,
			Description:         c.Description,
			ParameterSchemaJSON: c.ParameterSchemaJSON,
			PermissionProfile:   c.PermissionProfile,
			TimeoutSeconds:      c.TimeoutSeconds,
			MaxOutputBytes:      c.MaxOutputBytes,
			Enabled:             c.Enabled,
			ManifestHash:        c.ManifestHash,
			ExecutableHash:      c.ExecutableHash,
		}
		if err := s.store.UpsertNodeCommand(ctx, rec); err != nil {
			return added, err
		}
		added++
	}
	deleted, err := s.store.DeleteCommandsNotIn(ctx, nodeID, keep)
	if err != nil {
		return added, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorNode, ActorID: nodeID, Action: "node.commands_snapshot",
		ResourceType: "node", ResourceID: nodeID,
		Summary: fmt.Sprintf("command snapshot: %d upserted, %d removed", added, deleted),
	})
	return added, nil
}

// ListNodes returns nodes within the caller's scope.
func (s *NodeService) ListNodes(ctx context.Context, scopeNodeID string, role, status string, enabled *bool, keyword string, limit, offset int) ([]*model.Node, error) {
	if scopeNodeID != "" {
		n, err := s.store.NodeByID(ctx, scopeNodeID)
		if err != nil {
			return nil, err
		}
		return []*model.Node{n}, nil
	}
	return s.store.ListNodes(ctx, role, status, enabled, keyword, limit, offset)
}

// Node returns a single node within scope (child sees only itself).
func (s *NodeService) Node(ctx context.Context, scopeNodeID, id string) (*model.Node, error) {
	if scopeNodeID != "" && scopeNodeID != id {
		return nil, ErrNotFound
	}
	n, err := s.store.NodeByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return n, nil
}

// NodePatch describes allowed PATCH /nodes/{id} updates.
type NodePatch struct {
	Alias    *string        `json:"alias"`
	Labels   map[string]any `json:"labels"`
	Enabled  *bool          `json:"enabled"`
	Status   *string        `json:"status"`
	Metadata map[string]any `json:"metadata"`
}

// PatchNode updates node metadata within scope.
func (s *NodeService) PatchNode(ctx context.Context, scopeNodeID, id string, adminID string, patch NodePatch) (*model.Node, error) {
	if scopeNodeID != "" && scopeNodeID != id {
		return nil, ErrNotFound
	}
	n, err := s.store.NodeByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if patch.Alias != nil {
		n.Alias = *patch.Alias
	}
	if patch.Enabled != nil {
		n.Enabled = *patch.Enabled
		if !n.Enabled && n.Status != model.NodeStatusDisabled {
			n.Status = model.NodeStatusDisabled
		}
	}
	if patch.Status != nil {
		if *patch.Status == model.NodeStatusDisabled && n.Enabled {
			n.Enabled = false
		}
		n.Status = *patch.Status
	}
	if patch.Labels != nil {
		if raw, err := json.Marshal(patch.Labels); err == nil {
			n.LabelsJSON = string(raw)
		}
	}
	if patch.Metadata != nil {
		if raw, err := json.Marshal(patch.Metadata); err == nil {
			n.MetadataJSON = string(raw)
		}
	}
	if err := s.store.UpdateNode(ctx, n); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "node.update",
		ResourceType: "node", ResourceID: n.ID, Summary: "node updated",
	})
	return n, nil
}

// Metrics returns heartbeat history and aggregates for a node.
func (s *NodeService) Metrics(ctx context.Context, scopeNodeID, id string, limit int) (map[string]any, error) {
	if scopeNodeID != "" && scopeNodeID != id {
		return nil, ErrNotFound
	}
	if _, err := s.store.NodeByID(ctx, id); err != nil {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	hbs, err := s.store.RecentHeartbeats(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(hbs))
	var cpuSum, load1Sum float64
	var memTotal, memUsed, diskTotal, diskUsed, uptime int64
	for _, h := range hbs {
		cpuSum += h.CPUUsagePercent
		load1Sum += h.Load1
		memTotal = h.MemoryTotalBytes
		memUsed = h.MemoryUsedBytes
		diskTotal = h.DiskTotalBytes
		diskUsed = h.DiskUsedBytes
		uptime = h.UptimeSeconds
		out = append(out, map[string]any{
			"recorded_at":        h.RecordedAt,
			"cpu_usage_percent":  h.CPUUsagePercent,
			"memory_total_bytes": h.MemoryTotalBytes,
			"memory_used_bytes":  h.MemoryUsedBytes,
			"disk_total_bytes":   h.DiskTotalBytes,
			"disk_used_bytes":    h.DiskUsedBytes,
			"load_1":             h.Load1,
			"load_5":             h.Load5,
			"load_15":            h.Load15,
			"uptime_seconds":     h.UptimeSeconds,
			"time_offset_ms":     h.TimeOffsetMS,
		})
	}
	summary := map[string]any{
		"count":                 len(hbs),
		"avg_cpu_usage_percent": safeDiv(cpuSum, float64(len(hbs))),
		"avg_load_1":            safeDiv(load1Sum, float64(len(hbs))),
		"memory_total_bytes":    memTotal,
		"memory_used_bytes":     memUsed,
		"disk_total_bytes":      diskTotal,
		"disk_used_bytes":       diskUsed,
		"uptime_seconds":        uptime,
	}
	return map[string]any{"summary": summary, "heartbeats": out}, nil
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// NodeCommands returns a node's command registry.
func (s *NodeService) NodeCommands(ctx context.Context, scopeNodeID, id string) ([]*model.NodeCommand, error) {
	if scopeNodeID != "" && scopeNodeID != id {
		return nil, ErrNotFound
	}
	return s.store.NodeCommands(ctx, id)
}

// MarkOfflineNodes flips stale online nodes to offline and returns their IDs.
func (s *NodeService) MarkOfflineNodes(ctx context.Context) ([]string, error) {
	threshold := s.settings.Int(ctx, KeyOfflineThreshold, s.cfg.OfflineThresholdSeconds)
	before := time.Now().UTC().Add(-time.Duration(threshold) * time.Second)
	ids, err := s.store.MarkNodesOffline(ctx, s.envID, before)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorSystem, NodeID: id, Action: "node.offline",
			ResourceType: "node", ResourceID: id, Summary: "node marked offline (heartbeat timeout)",
			RiskLevel: RiskMedium,
		})
	}
	return ids, nil
}

// ResolveNodeSelector resolves a lease target from UUID, alias or IP[:port].
func (s *NodeService) ResolveNodeSelector(ctx context.Context, selector string) (*model.Node, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, ErrBadRequest
	}
	if model.NewUUIDIsValid(selector) {
		return s.store.NodeByID(ctx, selector)
	}
	if n, err := s.store.NodeByAlias(ctx, s.envID, selector); err == nil {
		return n, nil
	}
	if n, err := s.store.NodeByInstanceName(ctx, s.envID, selector); err == nil {
		return n, nil
	}
	host, port := splitHostPort(selector)
	if host != "" {
		if n, err := s.store.FindNodeByAddress(ctx, s.envID, host, port); err == nil {
			return n, nil
		} else if errors.Is(err, store.ErrConflict) {
			return nil, ErrAmbiguous
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func splitHostPort(sel string) (string, int) {
	if i := strings.LastIndex(sel, ":"); i > 0 {
		if p, err := strconv.Atoi(sel[i+1:]); err == nil && p > 0 && p < 65536 {
			return sel[:i], p
		}
	}
	return sel, 0
}

// CredentialForNode recomputes the raw node credential from the enrollment
// (only hashes are persisted) so the control plane can sign task payloads.
func (s *NodeService) CredentialForNode(ctx context.Context, nodeID string) (string, error) {
	e, err := s.store.EnrollmentByNodeID(ctx, nodeID)
	if err != nil {
		return "", err
	}
	return credentialFor(s.claimTokenFor(e.ID)), nil
}

// DeleteNode permanently deletes an offline or disabled child node together
// with all of its associated data. The admin must confirm the node's
// immutable instance_name exactly. The primary node can never be deleted and
// online nodes must be stopped/disabled first.
func (s *NodeService) DeleteNode(ctx context.Context, scopeNodeID, id, adminID, confirmInstanceName string) error {
	if scopeNodeID != "" || s.cfg.NodeRole != "primary" {
		return ErrForbidden
	}
	n, err := s.store.NodeByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if n.Role == "primary" {
		return ErrForbidden
	}
	if n.Status != model.NodeStatusOffline && n.Status != model.NodeStatusDisabled {
		return ErrConflict
	}
	if confirmInstanceName == "" || confirmInstanceName != n.InstanceName {
		return ErrBadRequest
	}
	if err := s.store.DeleteNodeCascade(ctx, n.ID); err != nil {
		if errors.Is(err, store.ErrStateTransition) {
			return ErrConflict
		}
		return err
	}
	// Recorded after the transaction so it survives the cascade (node_id is
	// intentionally empty; the deleted identity lives in the details).
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "node.delete",
		ResourceType: "node", ResourceID: n.ID,
		Summary:   "node permanently deleted with all data",
		Details:   map[string]any{"node_id": n.ID, "instance_name": n.InstanceName, "role": n.Role},
		RiskLevel: RiskCritical,
	})
	return nil
}
