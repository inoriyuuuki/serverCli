package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// LeaseService manages AI SSH lease requests and leases.
//
// Auto-approval rules let an admin exempt a device (ai_agent_id) from manual
// approval for a specific node for up to MaxAutoApprovalDays.
type LeaseService struct {
	store    *store.Store
	cfg      *config.Config
	log      *slog.Logger
	auditor  *Auditor
	nodes    *NodeService
	settings *SettingsService
	envID    string
}

// MaxAutoApprovalDays is the ceiling for device-node auto-approval rules.
const MaxAutoApprovalDays = 15

// NewLeaseService builds the service.
func NewLeaseService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, nodes *NodeService, settings *SettingsService) *LeaseService {
	return &LeaseService{store: st, cfg: cfg, log: log, auditor: auditor, nodes: nodes, settings: settings, envID: nodes.EnvID()}
}

// LeaseRequestInput is the AI's lease application.
type LeaseRequestInput struct {
	NodeSelector          string `json:"node_selector"`
	PublicKey             string `json:"public_key"`
	PermissionProfile     string `json:"permission_profile"`
	RequestedDurationSecs int    `json:"requested_duration_seconds"`
	Purpose               string `json:"purpose"`
	ClientRequestID       string `json:"client_request_id"`
	AIAgentID             string `json:"ai_agent_id"`
	AIAgentName           string `json:"ai_agent_name"`
}

// LeaseRequestResult is returned to the AI.
type LeaseRequestResult struct {
	LeaseRequest *model.AILeaseRequest `json:"lease_request"`
	Lease        *model.AILease        `json:"lease,omitempty"`
	RenewalToken string                `json:"renewal_token,omitempty"`
	Host         string                `json:"host,omitempty"`
	Port         int                   `json:"port,omitempty"`
}

// fingerprint returns the SHA-256 fingerprint of an SSH public key line.
func fingerprint(pubkey string) string {
	fields := strings.Fields(pubkey)
	if len(fields) < 2 {
		sum := sha256.Sum256([]byte(pubkey))
		return "sha256:" + hex.EncodeToString(sum[:16])
	}
	sum := sha256.Sum256([]byte(fields[1]))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CreateLeaseRequest validates an application and applies the approval policy.
func (s *LeaseService) CreateLeaseRequest(ctx context.Context, in LeaseRequestInput, sourceIP string) (*LeaseRequestResult, error) {
	newEnabled, _, scope := s.settings.AIAccess(ctx)
	if !newEnabled {
		if scope == "global" {
			return nil, ErrDisabled
		}
	}
	if in.ClientRequestID != "" {
		if existing, err := s.store.LeaseRequestByIdempotency(ctx, s.envID, in.ClientRequestID); err == nil {
			// Re-run decision for the existing request.
			return s.finishRequest(ctx, existing, sourceIP)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if in.NodeSelector == "" || in.PublicKey == "" {
		return nil, ErrBadRequest
	}
	switch in.PermissionProfile {
	case model.ProfileReadOnly, model.ProfileOperator, model.ProfileAdmin:
	default:
		return nil, ErrBadRequest
	}
	if in.PermissionProfile == model.ProfileAdmin {
		// admin profile requires manual approval and is rejected in test.
		// Keep it pending for manual review; policy mode never auto-approves it.
	}
	node, err := s.nodes.ResolveNodeSelector(ctx, in.NodeSelector)
	if err != nil {
		return nil, err
	}
	if !node.Enabled {
		return nil, ErrDisabled
	}
	defaultMinutes := s.settings.Int(ctx, KeyLeaseDefaultMinutes, s.cfg.AILeaseDefaultMinutes)
	duration := in.RequestedDurationSecs
	if duration <= 0 || duration > defaultMinutes*60 {
		duration = defaultMinutes * 60
	}
	req := &model.AILeaseRequest{
		ID:                       model.NewUUID(),
		ClientRequestID:          in.ClientRequestID,
		EnvironmentID:            s.envID,
		AIAgentID:                in.AIAgentID,
		AIAgentName:              in.AIAgentName,
		NodeID:                   node.ID,
		RequestedProfile:         in.PermissionProfile,
		RequestedDurationSeconds: duration,
		PublicKey:                in.PublicKey,
		PublicKeyFingerprint:     fingerprint(in.PublicKey),
		Purpose:                  in.Purpose,
		Status:                   model.LeaseRequestPending,
		SourceIP:                 sourceIP,
	}
	if err := s.store.CreateLeaseRequest(ctx, req); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAI, ActorID: in.AIAgentID, NodeID: node.ID, Action: "ai.lease_request",
		ResourceType: "ai_lease_request", ResourceID: req.ID, SourceIP: sourceIP,
		Summary: "AI lease requested",
		Details: map[string]any{"profile": in.PermissionProfile, "duration_seconds": duration,
			"public_key_fingerprint": req.PublicKeyFingerprint, "client_request_id": in.ClientRequestID},
	})
	return s.finishRequest(ctx, req, sourceIP)
}

// finishRequest applies the approval policy and, if approved, issues a lease.
func (s *LeaseService) finishRequest(ctx context.Context, req *model.AILeaseRequest, sourceIP string) (*LeaseRequestResult, error) {
	mode := s.settings.str(ctx, KeyApprovalMode, "manual")
	approved := false
	reason := ""
	switch mode {
	case "disabled":
		reason = "lease requests disabled"
	case "policy":
		if req.RequestedProfile == model.ProfileReadOnly && req.Status == model.LeaseRequestPending {
			approved = true
			reason = "auto-approved by policy (read-only)"
		} else if req.Status == model.LeaseRequestPending {
			// operator/admin pending manual review
			approved = false
			reason = "requires manual approval"
		}
	case "manual":
		if req.Status == model.LeaseRequestPending {
			approved = false
			reason = "requires manual approval"
		}
	}
	// Device+node auto-approval rule overrides manual/policy approval, but
	// never bypasses the global "disabled" gate handled above.
	byRule := false
	if !approved && mode != "disabled" && req.Status == model.LeaseRequestPending {
		if rule, err := s.store.AutoApprovalByAgentNode(ctx, s.envID, req.AIAgentID, req.NodeID); err == nil && rule != nil && rule.ExpiresAt.After(time.Now().UTC()) {
			approved = true
			byRule = true
			reason = "auto-approved by device-node rule"
		}
	}
	if req.Status != model.LeaseRequestPending {
		// Idempotent replay: reflect existing decision. The disabled branch
		// below already persisted a rejection for brand-new requests.
		return s.resultForRequest(ctx, req, sourceIP)
	}
	if mode == "disabled" {
		// Persist the rejection before returning so the request cannot be
		// revived later (e.g. by a new rule or a manual approve).
		now := time.Now().UTC()
		req.Status = model.LeaseRequestRejected
		req.DecisionReason = reason
		req.DecidedAt = &now
		if err := s.store.UpdateLeaseRequest(ctx, req); err != nil {
			return nil, err
		}
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAI, NodeID: req.NodeID, Action: "ai.lease_denied",
			ResourceType: "ai_lease_request", ResourceID: req.ID, SourceIP: sourceIP,
			Summary: reason, RiskLevel: RiskMedium,
		})
		return s.resultForRequest(ctx, req, sourceIP)
	}
	if approved {
		now := time.Now().UTC()
		req.Status = model.LeaseRequestApproved
		req.DecisionReason = reason
		req.DecidedAt = &now
		if err := s.store.ApproveLeaseRequestIfPending(ctx, req); err != nil {
			if errors.Is(err, store.ErrStateTransition) {
				return nil, ErrTerminal
			}
			return nil, err
		}
		if byRule {
			s.auditor.OK(ctx, AuditInput{
				ActorType: model.ActorSystem, ActorID: "auto-approval", NodeID: req.NodeID,
				Action: "ai.lease_auto_approved", ResourceType: "ai_lease_request", ResourceID: req.ID,
				SourceIP: sourceIP, Summary: "lease request auto-approved by device-node rule",
				Details:   map[string]any{"ai_agent_id": req.AIAgentID, "node_id": req.NodeID},
				RiskLevel: riskForProfile(req.RequestedProfile),
			})
		}
		return s.issueLease(ctx, req, sourceIP, model.ActorSystem, "system")
	}
	return s.resultForRequest(ctx, req, sourceIP)
}

func (s *LeaseService) resultForRequest(ctx context.Context, req *model.AILeaseRequest, sourceIP string) (*LeaseRequestResult, error) {
	out := &LeaseRequestResult{LeaseRequest: req}
	if req.Status == model.LeaseRequestApproved {
		lease, err := s.store.LeaseByID(ctx, s.leaseForRequest(ctx, req.ID))
		if err == nil && lease != nil {
			out.Lease = lease
		}
	}
	return out, nil
}

// leaseForRequest finds the lease id for a request.
func (s *LeaseService) leaseForRequest(ctx context.Context, requestID string) string {
	l, err := s.store.LeaseByRequestID(ctx, requestID)
	if err != nil {
		return ""
	}
	return l.ID
}

// newLease builds an active lease and its renewal token for an approved
// request without persisting anything.
func (s *LeaseService) newLease(ctx context.Context, req *model.AILeaseRequest) (*model.AILease, string, error) {
	maxHours := s.settings.Int(ctx, KeyLeaseMaxHours, s.cfg.AILeaseMaxHours)
	now := time.Now().UTC()
	renewalToken, err := security.NewToken(32)
	if err != nil {
		return nil, "", err
	}
	absolute := now.Add(time.Duration(maxHours) * time.Hour)
	expires := now.Add(time.Duration(req.RequestedDurationSeconds) * time.Second)
	if expires.After(absolute) {
		expires = absolute
	}
	lease := &model.AILease{
		ID:                   model.NewUUID(),
		RequestID:            req.ID,
		NodeID:               req.NodeID,
		AIAgentID:            req.AIAgentID,
		PermissionProfile:    req.RequestedProfile,
		PublicKey:            req.PublicKey,
		PublicKeyFingerprint: req.PublicKeyFingerprint,
		ExpiresAt:            expires,
		AbsoluteExpiresAt:    absolute,
		RenewCount:           0,
		Status:               model.LeaseActive,
		RenewalTokenHash:     security.HashToken(renewalToken),
		RenewalTokenPrefix:   security.Prefix(renewalToken, 8),
	}
	return lease, renewalToken, nil
}

func (s *LeaseService) issueLease(ctx context.Context, req *model.AILeaseRequest, sourceIP, actorType, actorID string) (*LeaseRequestResult, error) {
	lease, renewalToken, err := s.newLease(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.store.AppendLeaseEvent(ctx, &model.AILeaseEvent{
		LeaseID: lease.ID, EventType: "issued", ActorType: actorType, ActorID: actorID,
		DetailsJSON: `{"duration_seconds":` + itoa(req.RequestedDurationSeconds) + `,"expires_at":"` + lease.ExpiresAt.Format(time.RFC3339) + `","absolute_expires_at":"` + lease.AbsoluteExpiresAt.Format(time.RFC3339) + `"}`,
	})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorSystem, NodeID: lease.NodeID, Action: "ai.lease_issued",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, SourceIP: sourceIP,
		Summary: "AI lease issued",
		Details: map[string]any{"profile": lease.PermissionProfile, "expires_at": lease.ExpiresAt,
			"absolute_expires_at": lease.AbsoluteExpiresAt, "public_key_fingerprint": lease.PublicKeyFingerprint,
			"renewal_token_prefix": lease.RenewalTokenPrefix},
		RiskLevel: riskForProfile(lease.PermissionProfile),
	})
	return &LeaseRequestResult{LeaseRequest: req, Lease: lease, RenewalToken: renewalToken}, nil
}

func riskForProfile(p string) string {
	switch p {
	case model.ProfileAdmin:
		return RiskCritical
	case model.ProfileOperator:
		return RiskHigh
	}
	return RiskMedium
}

// LeaseRequest returns a lease request.
func (s *LeaseService) LeaseRequest(ctx context.Context, id string) (*model.AILeaseRequest, error) {
	req, err := s.store.LeaseRequestByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return req, nil
}

// ListLeaseRequests lists requests.
func (s *LeaseService) ListLeaseRequests(ctx context.Context, scopeNodeID, nodeID, status string, limit, offset int) ([]*model.AILeaseRequest, error) {
	if scopeNodeID != "" {
		nodeID = scopeNodeID
	}
	return s.store.ListLeaseRequests(ctx, nodeID, status, limit, offset)
}

// ApproveLeaseRequest manually approves a pending request.
func (s *LeaseService) ApproveLeaseRequest(ctx context.Context, id, adminID string) (*LeaseRequestResult, error) {
	req, err := s.store.LeaseRequestByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if req.Status != model.LeaseRequestPending {
		return nil, ErrTerminal
	}
	now := time.Now().UTC()
	req.Status = model.LeaseRequestApproved
	req.DecisionReason = "approved by admin"
	req.DecidedAt = &now
	if err := s.store.ApproveLeaseRequestIfPending(ctx, req); err != nil {
		if errors.Is(err, store.ErrStateTransition) {
			return nil, ErrTerminal
		}
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: req.NodeID, Action: "ai.lease_approve",
		ResourceType: "ai_lease_request", ResourceID: req.ID, Summary: "lease request approved by admin",
	})
	return s.issueLease(ctx, req, "", model.ActorAdmin, adminID)
}

// RejectLeaseRequest rejects a pending request.
func (s *LeaseService) RejectLeaseRequest(ctx context.Context, id, adminID, reason string) error {
	req, err := s.store.LeaseRequestByID(ctx, id)
	if err != nil {
		return ErrNotFound
	}
	if req.Status != model.LeaseRequestPending {
		return ErrTerminal
	}
	now := time.Now().UTC()
	req.Status = model.LeaseRequestRejected
	req.DecisionReason = reason
	req.DecidedAt = &now
	if err := s.store.UpdateLeaseRequest(ctx, req); err != nil {
		return err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: req.NodeID, Action: "ai.lease_reject",
		ResourceType: "ai_lease_request", ResourceID: req.ID, Summary: "lease request rejected by admin",
		Details: map[string]any{"reason": reason},
	})
	return nil
}

// authLease resolves a lease by renewal token.
func (s *LeaseService) authLease(ctx context.Context, leaseID, token string) (*model.AILease, error) {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return nil, ErrNotFound
	}
	if lease.RenewalTokenHash == "" || !security.ConstantTimeEqual(security.HashToken(token), lease.RenewalTokenHash) {
		return nil, ErrForbidden
	}
	return lease, nil
}

// Renew extends an active lease without changing issued_at or the absolute cap.
func (s *LeaseService) Renew(ctx context.Context, leaseID, token string, requestedSeconds int) (*model.AILease, error) {
	lease, err := s.authLease(ctx, leaseID, token)
	if err != nil {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAI, Action: "ai.lease_renew", ResourceType: "ai_lease", ResourceID: leaseID,
			LeaseID: leaseID, Summary: "renewal denied: bad token", RiskLevel: RiskHigh,
		})
		return nil, err
	}
	if lease.Status != model.LeaseActive {
		_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "lease not active"})
		return nil, ErrTerminal
	}
	if lease.RenewalDisabled {
		_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "renewals disabled for lease"})
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAI, NodeID: lease.NodeID, Action: "ai.lease_renew_denied",
			ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, Summary: "renewal denied: disabled", RiskLevel: RiskMedium,
		})
		return nil, ErrForbidden
	}
	_, renewals, scope := s.settings.AIAccess(ctx)
	if !renewals {
		if scope == "global" {
			_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "global renewals disabled"})
			s.auditor.Denied(ctx, AuditInput{
				ActorType: model.ActorAI, NodeID: lease.NodeID, Action: "ai.lease_renew_denied",
				ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, Summary: "renewal denied: global gate", RiskLevel: RiskMedium,
			})
			return nil, ErrForbidden
		}
	}
	now := time.Now().UTC()
	if !lease.ExpiresAt.After(now) {
		_ = s.store.ExpireLease(ctx, lease.ID)
		_ = s.appendLeaseEvent(ctx, lease, "expired", model.ActorSystem, "scheduler", map[string]any{"reason": "expired before renewal"})
		return nil, ErrTerminal
	}
	if !lease.AbsoluteExpiresAt.After(now) {
		_ = s.store.ExpireLease(ctx, lease.ID)
		_ = s.appendLeaseEvent(ctx, lease, "expired", model.ActorSystem, "scheduler", map[string]any{"reason": "absolute cap reached"})
		return nil, ErrTerminal
	}
	defaultMinutes := s.settings.Int(ctx, KeyLeaseDefaultMinutes, s.cfg.AILeaseDefaultMinutes)
	ext := requestedSeconds
	if ext <= 0 || ext > defaultMinutes*60 {
		ext = defaultMinutes * 60
	}
	newExpires := now.Add(time.Duration(ext) * time.Second)
	if newExpires.After(lease.AbsoluteExpiresAt) {
		newExpires = lease.AbsoluteExpiresAt
	}
	if !newExpires.After(lease.ExpiresAt) {
		_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "no extension available"})
		return nil, ErrForbidden
	}
	lease.ExpiresAt = newExpires
	lease.LastRenewedAt = &now
	lease.RenewCount++
	lease.LastHeartbeatAt = &now
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.appendLeaseEvent(ctx, lease, "renewed", model.ActorAI, lease.AIAgentID, map[string]any{
		"new_expires_at": lease.ExpiresAt, "absolute_expires_at": lease.AbsoluteExpiresAt, "renew_count": lease.RenewCount,
	})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAI, ActorID: lease.AIAgentID, NodeID: lease.NodeID, Action: "ai.lease_renew",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
		Summary: "AI lease renewed", Details: map[string]any{"new_expires_at": lease.ExpiresAt},
	})
	return lease, nil
}

// Heartbeat refreshes lease liveness.
func (s *LeaseService) Heartbeat(ctx context.Context, leaseID, token string) (*model.AILease, error) {
	lease, err := s.authLease(ctx, leaseID, token)
	if err != nil {
		return nil, err
	}
	if lease.Status != model.LeaseActive {
		return nil, ErrTerminal
	}
	now := time.Now().UTC()
	if !lease.ExpiresAt.After(now) {
		_ = s.store.ExpireLease(ctx, lease.ID)
		_ = s.appendLeaseEvent(ctx, lease, "expired", model.ActorSystem, "scheduler", map[string]any{"reason": "expired"})
		return nil, ErrTerminal
	}
	lease.LastHeartbeatAt = &now
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	return lease, nil
}

// Disconnect marks a lease disconnected (terminal).
func (s *LeaseService) Disconnect(ctx context.Context, leaseID, token string) (*model.AILease, error) {
	lease, err := s.authLease(ctx, leaseID, token)
	if err != nil {
		return nil, err
	}
	if lease.Status != model.LeaseActive {
		return nil, ErrTerminal
	}
	now := time.Now().UTC()
	lease.Status = model.LeaseDisconnected
	lease.LastHeartbeatAt = &now
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.appendLeaseEvent(ctx, lease, "disconnected", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "client disconnect"})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAI, ActorID: lease.AIAgentID, NodeID: lease.NodeID, Action: "ai.lease_disconnect",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, Summary: "AI lease disconnected",
	})
	return lease, nil
}

// Revoke revokes a lease (admin action).
func (s *LeaseService) Revoke(ctx context.Context, leaseID, adminID, reason string, terminateSessions bool) (*model.AILease, error) {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return nil, ErrNotFound
	}
	if lease.Status != model.LeaseActive {
		return nil, ErrTerminal
	}
	now := time.Now().UTC()
	lease.Status = model.LeaseRevoked
	lease.RevokedAt = &now
	lease.RevokeReason = reason
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	details := map[string]any{"reason": reason, "terminate_sessions": terminateSessions}
	_ = s.appendLeaseEvent(ctx, lease, "revoked", model.ActorAdmin, adminID, details)
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: lease.NodeID, Action: "ai.lease_revoke",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
		Summary: "AI lease revoked", Details: details, RiskLevel: RiskHigh,
	})
	return lease, nil
}

// ListLeases lists leases within scope.
func (s *LeaseService) ListLeases(ctx context.Context, scopeNodeID, nodeID, status string, limit, offset int) ([]*model.AILease, error) {
	if scopeNodeID != "" {
		nodeID = scopeNodeID
	}
	return s.store.ListLeases(ctx, nodeID, status, limit, offset)
}

// Lease returns one lease within scope.
func (s *LeaseService) Lease(ctx context.Context, scopeNodeID, id string) (*model.AILease, error) {
	l, err := s.store.LeaseByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if scopeNodeID != "" && scopeNodeID != l.NodeID {
		return nil, ErrNotFound
	}
	return l, nil
}

// SetAIAccess updates request/renewal gates and scope.
func (s *LeaseService) SetAIAccess(ctx context.Context, newRequests, renewals *bool, scope string, adminID string) error {
	if scope != "" && scope != "global" {
		// scope may be a node id
		if _, err := s.store.NodeByID(ctx, scope); err != nil {
			return ErrNotFound
		}
	}
	if newRequests != nil {
		if err := s.store.SetSetting(ctx, KeyNewRequestsEnabled, boolStr(*newRequests)); err != nil {
			return err
		}
	}
	if renewals != nil {
		if err := s.store.SetSetting(ctx, KeyRenewalsEnabled, boolStr(*renewals)); err != nil {
			return err
		}
	}
	if scope != "" {
		if err := s.store.SetSetting(ctx, KeyAIAccessScope, scope); err != nil {
			return err
		}
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "ai.access_update",
		Summary: "AI access settings updated", RiskLevel: RiskHigh,
		Details: map[string]any{"new_requests_enabled": newRequests, "renewals_enabled": renewals, "scope": scope},
	})
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (s *LeaseService) appendLeaseEvent(ctx context.Context, lease *model.AILease, eventType, actorType, actorID string, details map[string]any) error {
	raw := ""
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			raw = string(b)
		}
	}
	return s.store.AppendLeaseEvent(ctx, &model.AILeaseEvent{
		LeaseID: lease.ID, EventType: eventType, ActorType: actorType, ActorID: actorID, DetailsJSON: raw,
	})
}

// AgentLeaseEvent handles agent-reported lease lifecycle events.
func (s *LeaseService) AgentLeaseEvent(ctx context.Context, nodeID, leaseID string, in AgentLeaseEventInput) error {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return ErrNotFound
	}
	if lease.NodeID != nodeID {
		return ErrNotFound
	}
	now := time.Now().UTC()
	switch in.EventType {
	case "installed":
		lease.KeyInstalled = true
		lease.KeyInstalledAt = &now
		if err := s.store.UpdateLease(ctx, lease); err != nil {
			return err
		}
		_ = s.appendLeaseEvent(ctx, lease, "installed", model.ActorNode, nodeID, map[string]any{"fingerprint": in.Fingerprint})
	case "install_failed":
		if lease.Status == model.LeaseActive {
			lease.Status = model.LeaseFailed
			if err := s.store.UpdateLease(ctx, lease); err != nil {
				return err
			}
		}
		_ = s.appendLeaseEvent(ctx, lease, "install_failed", model.ActorNode, nodeID, map[string]any{"error": in.Message})
		s.auditor.Failure(ctx, AuditInput{
			ActorType: model.ActorNode, ActorID: nodeID, NodeID: nodeID, Action: "ai.lease_install_failed",
			ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
			Summary: "lease key install failed", RiskLevel: RiskHigh, Details: map[string]any{"message": in.Message},
		})
	case "removed":
		lease.KeyInstalled = false
		lease.KeyInstalledAt = &now
		if err := s.store.UpdateLease(ctx, lease); err != nil {
			return err
		}
		_ = s.appendLeaseEvent(ctx, lease, "removed", model.ActorNode, nodeID, map[string]any{"reason": in.Message})
	case "session_started":
		sess := &model.AISSHSession{
			LeaseID:       leaseID,
			NodeID:        nodeID,
			RemoteAddress: in.RemoteAddress,
			ConnectionID:  in.ConnectionID,
			OSPid:         in.OSPid,
		}
		if err := s.store.CreateSSHSession(ctx, sess); err != nil {
			return err
		}
		lease.ActiveSessionCount++
		if err := s.store.UpdateLease(ctx, lease); err != nil {
			return err
		}
		_ = s.appendLeaseEvent(ctx, lease, "session_started", model.ActorNode, nodeID, map[string]any{"session_id": sess.ID})
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorAI, ActorID: lease.AIAgentID, NodeID: nodeID, Action: "ai.ssh_session_started",
			ResourceType: "ai_ssh_session", ResourceID: sess.ID, LeaseID: lease.ID, SessionID: sess.ID,
			Summary: "AI SSH session started", RiskLevel: riskForProfile(lease.PermissionProfile),
		})
	case "session_ended":
		lease.ActiveSessionCount--
		if lease.ActiveSessionCount < 0 {
			lease.ActiveSessionCount = 0
		}
		if err := s.store.UpdateLease(ctx, lease); err != nil {
			return err
		}
		if in.SessionID != "" {
			_ = s.store.EndSSHSession(ctx, in.SessionID, now, in.Message, in.ExitCode)
		}
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorAI, ActorID: lease.AIAgentID, NodeID: nodeID, Action: "ai.ssh_session_ended",
			ResourceType: "ai_ssh_session", ResourceID: in.SessionID, LeaseID: lease.ID, SessionID: in.SessionID,
			Summary: "AI SSH session ended", Details: map[string]any{"reason": in.Message},
		})
	case "session_activity":
		if in.SessionID != "" {
			// best-effort last-seen update
		}
	default:
		return ErrBadRequest
	}
	return nil
}

// AgentLeaseEventInput is an agent lease event payload.
type AgentLeaseEventInput struct {
	EventType     string `json:"event_type"`
	Message       string `json:"message"`
	SessionID     string `json:"session_id"`
	RemoteAddress string `json:"remote_address"`
	ConnectionID  string `json:"connection_id"`
	OSPid         int64  `json:"os_pid"`
	ExitCode      *int   `json:"exit_code"`
	Fingerprint   string `json:"fingerprint"`
}

// ExpireStaleLeases marks expired or heartbeat-stale leases terminal.
func (s *LeaseService) ExpireStaleLeases(ctx context.Context) ([]string, error) {
	var affected []string
	expired, err := s.store.ExpiredLeases(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, l := range expired {
		if err := s.store.ExpireLease(ctx, l.ID); err == nil {
			_ = s.appendLeaseEvent(ctx, l, "expired", model.ActorSystem, "scheduler", map[string]any{"reason": "expires_at reached"})
			s.auditor.OK(ctx, AuditInput{
				ActorType: model.ActorSystem, NodeID: l.NodeID, Action: "ai.lease_expired",
				ResourceType: "ai_lease", ResourceID: l.ID, LeaseID: l.ID, Summary: "AI lease expired",
			})
			affected = append(affected, l.ID)
		}
	}
	// Disconnect grace: active leases whose heartbeat is older than the grace
	// period and that have heartbeated at least once.
	grace := s.settings.Int(ctx, KeyLeaseGraceSeconds, s.cfg.AILeaseDisconnectGraceSecs)
	all, err := s.store.ListLeases(ctx, "", model.LeaseActive, 0, 0)
	if err != nil {
		return affected, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(grace) * time.Second)
	for _, l := range all {
		if l.LastHeartbeatAt != nil && l.LastHeartbeatAt.Before(cutoff) {
			l.Status = model.LeaseDisconnected
			if err := s.store.UpdateLease(ctx, l); err == nil {
				_ = s.appendLeaseEvent(ctx, l, "disconnected", model.ActorSystem, "scheduler", map[string]any{"reason": "heartbeat timeout"})
				s.auditor.OK(ctx, AuditInput{
					ActorType: model.ActorSystem, NodeID: l.NodeID, Action: "ai.lease_disconnected",
					ResourceType: "ai_lease", ResourceID: l.ID, LeaseID: l.ID,
					Summary: "AI lease disconnected (heartbeat timeout)", RiskLevel: RiskMedium,
				})
				affected = append(affected, l.ID)
			}
		}
	}
	return affected, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// DisableRenewal disables renewal for an active lease (admin action).
func (s *LeaseService) DisableRenewal(ctx context.Context, leaseID, adminID, reason string) (*model.AILease, error) {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return nil, ErrNotFound
	}
	if lease.Status != model.LeaseActive {
		return nil, ErrTerminal
	}
	lease.RenewalDisabled = true
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.appendLeaseEvent(ctx, lease, "renewal_disabled", model.ActorAdmin, adminID, map[string]any{"reason": reason})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: lease.NodeID, Action: "ai.lease_disable_renewal",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
		Summary: "renewal disabled for lease", Details: map[string]any{"reason": reason}, RiskLevel: RiskMedium,
	})
	return lease, nil
}

// ProtectLease marks a lease as important so automatic cleanup skips it.
func (s *LeaseService) ProtectLease(ctx context.Context, leaseID, adminID string) (*model.AILease, error) {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	lease.IsProtected = true
	lease.ProtectedAt = &now
	if err := s.store.UpdateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.appendLeaseEvent(ctx, lease, "protected", model.ActorAdmin, adminID, map[string]any{})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: lease.NodeID, Action: "ai.lease_protect",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
		Summary: "lease marked as important", RiskLevel: RiskLow,
	})
	return lease, nil
}

// RevokeAll revokes active leases globally or for a single node (admin action).
// Returns the number of leases revoked.
func (s *LeaseService) RevokeAll(ctx context.Context, nodeID, adminID, reason string, terminateSessions bool) (int, error) {
	var active []*model.AILease
	var err error
	if nodeID != "" {
		if _, err := s.store.NodeByID(ctx, nodeID); err != nil {
			return 0, ErrNotFound
		}
		active, err = s.store.ActiveLeasesOnNode(ctx, nodeID)
	} else {
		active, err = s.store.ListLeases(ctx, "", model.LeaseActive, 0, 0)
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, l := range active {
		if l.Status != model.LeaseActive {
			continue
		}
		if _, err := s.Revoke(ctx, l.ID, adminID, reason, terminateSessions); err == nil {
			count++
		}
	}
	return count, nil
}

// AutoApprovalResult is returned when an admin approves a request and creates
// a device-node auto-approval rule at the same time.
type AutoApprovalResult struct {
	AutoApproval *model.AIAutoApproval `json:"auto_approval"`
	LeaseRequest *model.AILeaseRequest `json:"lease_request"`
	Lease        *model.AILease        `json:"lease,omitempty"`
}

// validateAutoApprovalDays normalizes duration_days into a duration capped at
// MaxAutoApprovalDays.
func validateAutoApprovalDays(days int) (time.Duration, error) {
	if days < 1 || days > MaxAutoApprovalDays {
		return 0, fmt.Errorf("%w: duration_days must be between 1 and %d", ErrBadRequest, MaxAutoApprovalDays)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// AutoApproveWithDuration approves a pending request and creates (or extends)
// the device-node auto-approval rule atomically.
func (s *LeaseService) AutoApproveWithDuration(ctx context.Context, requestID, adminID string, durationDays int) (*AutoApprovalResult, error) {
	dur, err := validateAutoApprovalDays(durationDays)
	if err != nil {
		return nil, err
	}
	req, err := s.store.LeaseRequestByID(ctx, requestID)
	if err != nil {
		return nil, ErrNotFound
	}
	if req.Status != model.LeaseRequestPending {
		return nil, ErrTerminal
	}
	now := time.Now().UTC()
	rule := &model.AIAutoApproval{
		EnvironmentID:   s.envID,
		AIAgentID:       req.AIAgentID,
		AIAgentName:     req.AIAgentName,
		NodeID:          req.NodeID,
		SourceRequestID: req.ID,
		CreatedBy:       adminID,
		ExpiresAt:       now.Add(dur),
	}
	// Never silently shorten an existing exemption: extend from the later of
	// now and the current expiry, still capped at now + MaxAutoApprovalDays.
	if existing, err := s.store.AutoApprovalByAgentNode(ctx, s.envID, req.AIAgentID, req.NodeID); err == nil && existing != nil {
		base := existing.ExpiresAt
		if base.Before(now) {
			base = now
		}
		capped := now.Add(time.Duration(MaxAutoApprovalDays) * 24 * time.Hour)
		rule.ExpiresAt = base.Add(dur)
		if rule.ExpiresAt.After(capped) {
			rule.ExpiresAt = capped
		}
	}
	req.Status = model.LeaseRequestApproved
	req.DecisionReason = "auto-approved by admin with device-node rule"
	req.DecidedAt = &now
	lease, _, err := s.newLease(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.store.ApproveRequestWithAutoApproval(ctx, rule, req, lease); err != nil {
		if errors.Is(err, store.ErrStateTransition) {
			return nil, ErrTerminal
		}
		return nil, err
	}
	// Re-read so the response carries accurate timestamps for updates too.
	if refreshed, err := s.store.AutoApprovalByID(ctx, rule.ID); err == nil && refreshed != nil {
		rule = refreshed
	}
	_ = s.store.AppendLeaseEvent(ctx, &model.AILeaseEvent{
		LeaseID: lease.ID, EventType: "issued", ActorType: model.ActorAdmin, ActorID: adminID,
		DetailsJSON: `{"duration_seconds":` + itoa(req.RequestedDurationSeconds) + `,"expires_at":"` + lease.ExpiresAt.Format(time.RFC3339) + `","absolute_expires_at":"` + lease.AbsoluteExpiresAt.Format(time.RFC3339) + `"}`,
	})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: req.NodeID, Action: "ai.lease_auto_approval_create",
		ResourceType: "ai_auto_approval", ResourceID: rule.ID,
		Summary: "device-node auto-approval rule created and request approved",
		Details: map[string]any{"ai_agent_id": req.AIAgentID, "node_id": req.NodeID,
			"duration_days": durationDays, "expires_at": rule.ExpiresAt,
			"request_id": req.ID, "lease_id": lease.ID},
		RiskLevel: RiskHigh,
	})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorSystem, NodeID: lease.NodeID, Action: "ai.lease_issued",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID,
		Summary: "AI lease issued (auto-approval rule)",
		Details: map[string]any{"profile": lease.PermissionProfile, "expires_at": lease.ExpiresAt,
			"absolute_expires_at": lease.AbsoluteExpiresAt, "public_key_fingerprint": lease.PublicKeyFingerprint,
			"renewal_token_prefix": lease.RenewalTokenPrefix},
		RiskLevel: riskForProfile(lease.PermissionProfile),
	})
	return &AutoApprovalResult{AutoApproval: rule, LeaseRequest: req, Lease: lease}, nil
}

// ListAutoApprovals lists device-node auto-approval rules.
func (s *LeaseService) ListAutoApprovals(ctx context.Context, scopeNodeID, nodeID, status string, limit, offset int) ([]*model.AIAutoApproval, error) {
	if scopeNodeID != "" {
		return nil, ErrForbidden
	}
	return s.store.ListAutoApprovals(ctx, nodeID, status, limit, offset)
}

// ExtendAutoApproval extends an existing rule by durationDays, accumulating
// from the current expiry but never beyond now + MaxAutoApprovalDays.
func (s *LeaseService) ExtendAutoApproval(ctx context.Context, id, adminID string, durationDays int) (*model.AIAutoApproval, error) {
	dur, err := validateAutoApprovalDays(durationDays)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.AutoApprovalByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	oldExpiry := rule.ExpiresAt
	now := time.Now().UTC()
	base := oldExpiry
	if base.Before(now) {
		base = now
	}
	capped := now.Add(time.Duration(MaxAutoApprovalDays) * 24 * time.Hour)
	newExpiry := base.Add(dur)
	if newExpiry.After(capped) {
		newExpiry = capped
	}
	if !newExpiry.After(oldExpiry) {
		return nil, fmt.Errorf("%w: extension yields no additional time", ErrBadRequest)
	}
	rule.ExpiresAt = newExpiry
	updated, err := s.store.UpsertAutoApproval(ctx, rule)
	if err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, NodeID: rule.NodeID, Action: "ai.auto_approval_extend",
		ResourceType: "ai_auto_approval", ResourceID: rule.ID,
		Summary: "device-node auto-approval rule extended",
		Details: map[string]any{"ai_agent_id": rule.AIAgentID, "node_id": rule.NodeID,
			"duration_days": durationDays, "old_expires_at": oldExpiry, "new_expires_at": newExpiry},
		RiskLevel: RiskMedium,
	})
	return updated, nil
}
