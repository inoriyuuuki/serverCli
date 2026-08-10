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

// LeaseService manages AI SSH lease requests and leases. Requests are
// auto-approved by a valid Access Token; lease expiry is bounded by the token.
type LeaseService struct {
	store    *store.Store
	cfg      *config.Config
	log      *slog.Logger
	auditor  *Auditor
	nodes    *NodeService
	settings *SettingsService
	envID    string
}

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
	Host         string                `json:"host,omitempty"`
	Port         int                   `json:"port,omitempty"`
	Replayed     bool                  `json:"-"`
}

// validatePublicKey rejects public keys that could inject extra
// authorized_keys options/lines (auto-approval means the key is attacker
// supplied). Accepts a single line of "type base64 [comment]".
func validatePublicKey(pubkey string) error {
	if pubkey == "" || strings.ContainsAny(pubkey, "\n\r,") || strings.Contains(pubkey, `"`) {
		return fmt.Errorf("%w: invalid public key", ErrBadRequest)
	}
	fields := strings.Fields(pubkey)
	if len(fields) < 2 || len(fields) > 3 {
		return fmt.Errorf("%w: invalid public key", ErrBadRequest)
	}
	switch fields[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
	default:
		return fmt.Errorf("%w: unsupported public key type", ErrBadRequest)
	}
	if len(fields[1]) < 16 {
		return fmt.Errorf("%w: invalid public key encoding", ErrBadRequest)
	}
	for _, c := range fields[1] {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			return fmt.Errorf("%w: invalid public key encoding", ErrBadRequest)
		}
	}
	return nil
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

// CreateLeaseRequest validates an application and auto-approves it: a valid
// access token is the credential, so no manual approval or device-node rule is
// consulted. The lease expiry is the earliest of the requested duration, the
// access token expiry and the system absolute lease cap.
func (s *LeaseService) CreateLeaseRequest(ctx context.Context, in LeaseRequestInput, sourceIP string, principal *TokenPrincipal) (*LeaseRequestResult, error) {
	newEnabled, _, scope := s.settings.AIAccess(ctx)
	if !newEnabled && scope == "global" {
		return nil, ErrDisabled
	}
	if in.ClientRequestID != "" {
		if existing, err := s.store.LeaseRequestByIdempotency(ctx, s.envID, in.ClientRequestID); err == nil {
			// Idempotency replay must not leak another token's request/lease:
			// a key owned by a different token is treated as not found.
			if existing.AccessTokenID != principal.TokenID {
				return nil, ErrNotFound
			}
			res, err := s.resultForRequest(ctx, existing)
			if err != nil {
				return nil, err
			}
			res.Replayed = true
			return res, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if in.NodeSelector == "" || in.PublicKey == "" {
		return nil, ErrBadRequest
	}
	// 使用原因必填：AI 每次申请都必须说明用途，便于审计与责任追溯。
	if strings.TrimSpace(in.Purpose) == "" {
		return nil, ErrBadRequest
	}
	if err := validatePublicKey(in.PublicKey); err != nil {
		return nil, err
	}
	switch in.PermissionProfile {
	case model.ProfileReadOnly, model.ProfileOperator, model.ProfileAdmin:
	default:
		return nil, ErrBadRequest
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
	now := time.Now().UTC()
	req := &model.AILeaseRequest{
		ID:                       model.NewUUID(),
		ClientRequestID:          in.ClientRequestID,
		EnvironmentID:            s.envID,
		AccessTokenID:            principal.TokenID,
		AccessTokenName:          principal.Name,
		AccessTokenPrefix:        principal.TokenPrefix,
		AIAgentID:                in.AIAgentID,
		AIAgentName:              in.AIAgentName,
		NodeID:                   node.ID,
		RequestedProfile:         in.PermissionProfile,
		RequestedDurationSeconds: duration,
		PublicKey:                in.PublicKey,
		PublicKeyFingerprint:     fingerprint(in.PublicKey),
		Purpose:                  strings.TrimSpace(in.Purpose),
		Status:                   model.LeaseRequestApproved,
		DecisionReason:           "auto-approved by access token",
		SourceIP:                 sourceIP,
		DecidedAt:                &now,
	}
	if err := s.store.CreateLeaseRequest(ctx, req); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAI, ActorID: in.AIAgentID, NodeID: node.ID, Action: "ai.lease_request",
		ResourceType: "ai_lease_request", ResourceID: req.ID, SourceIP: sourceIP,
		Summary: "AI lease requested (access token)",
		Details: map[string]any{"profile": in.PermissionProfile, "duration_seconds": duration,
			"public_key_fingerprint": req.PublicKeyFingerprint, "client_request_id": in.ClientRequestID,
			"access_token_id": principal.TokenID, "access_token_name": principal.Name},
	})
	return s.issueLease(ctx, req, sourceIP, principal)
}

// resultForRequest reflects the stored decision for a lease request.
func (s *LeaseService) resultForRequest(ctx context.Context, req *model.AILeaseRequest) (*LeaseRequestResult, error) {
	out := &LeaseRequestResult{LeaseRequest: req}
	if req.Status == model.LeaseRequestApproved {
		if lease, err := s.store.LeaseByRequestID(ctx, req.ID); err == nil && lease != nil {
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

// newLease builds an active lease for an approved request without persisting
// anything. Expiry is the earliest of the requested duration, the access token
// expiry and the system absolute cap; a renewal token hash is still stored for
// schema compatibility but is never returned to the client (client management
// uses the access token).
func (s *LeaseService) newLease(ctx context.Context, req *model.AILeaseRequest, tokenExpiresAt *time.Time) (*model.AILease, string, error) {
	maxHours := s.settings.Int(ctx, KeyLeaseMaxHours, s.cfg.AILeaseMaxHours)
	if maxHours <= 0 {
		maxHours = s.cfg.AILeaseMaxHours
	}
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
	if tokenExpiresAt != nil && expires.After(*tokenExpiresAt) {
		expires = *tokenExpiresAt
	}
	if expires.Before(now) {
		// Token already at/past expiry at issue time: never mint an already
		// expired active lease.
		expires = now
	}
	lease := &model.AILease{
		ID:                   model.NewUUID(),
		RequestID:            req.ID,
		AccessTokenID:        req.AccessTokenID,
		AccessTokenName:      req.AccessTokenName,
		AccessTokenPrefix:    req.AccessTokenPrefix,
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

func (s *LeaseService) issueLease(ctx context.Context, req *model.AILeaseRequest, sourceIP string, principal *TokenPrincipal) (*LeaseRequestResult, error) {
	lease, _, err := s.newLease(ctx, req, principal.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateLease(ctx, lease); err != nil {
		return nil, err
	}
	_ = s.store.AppendLeaseEvent(ctx, &model.AILeaseEvent{
		LeaseID: lease.ID, EventType: "issued", ActorType: model.ActorAI, ActorID: req.AIAgentID,
		DetailsJSON: `{"duration_seconds":` + itoa(req.RequestedDurationSeconds) + `,"expires_at":"` + lease.ExpiresAt.Format(time.RFC3339) + `","absolute_expires_at":"` + lease.AbsoluteExpiresAt.Format(time.RFC3339) + `"}`,
	})
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAI, ActorID: req.AIAgentID, NodeID: lease.NodeID, Action: "ai.lease_issued",
		ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, SourceIP: sourceIP,
		Summary: "AI lease issued (auto-approved by access token)",
		Details: map[string]any{"profile": lease.PermissionProfile, "expires_at": lease.ExpiresAt,
			"absolute_expires_at": lease.AbsoluteExpiresAt, "public_key_fingerprint": lease.PublicKeyFingerprint,
			"access_token_id": principal.TokenID, "access_token_name": principal.Name},
		RiskLevel: riskForProfile(lease.PermissionProfile),
	})
	return &LeaseRequestResult{LeaseRequest: req, Lease: lease}, nil
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

// LeaseRequest returns a lease request owned by the principal. Requests
// created by another token (or legacy rows) are indistinguishable: 404.
func (s *LeaseService) LeaseRequest(ctx context.Context, id string, principal *TokenPrincipal) (*model.AILeaseRequest, error) {
	req, err := s.store.LeaseRequestByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if req.AccessTokenID != principal.TokenID {
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

// Renew extends an active lease without changing issued_at or the absolute cap.
// The new expiry is clamped to the access token expiry and the absolute cap.
func (s *LeaseService) Renew(ctx context.Context, leaseID string, principal *TokenPrincipal, requestedSeconds int) (*model.AILease, error) {
	lease, err := s.authLeaseForPrincipal(ctx, leaseID, principal)
	if err != nil {
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAI, ActorID: principal.Name, Action: "ai.lease_renew", ResourceType: "ai_lease", ResourceID: leaseID,
			LeaseID: leaseID, Summary: "renewal denied: token does not own lease", RiskLevel: RiskHigh,
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
	if !renewals && scope == "global" {
		_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "global renewals disabled"})
		s.auditor.Denied(ctx, AuditInput{
			ActorType: model.ActorAI, NodeID: lease.NodeID, Action: "ai.lease_renew_denied",
			ResourceType: "ai_lease", ResourceID: lease.ID, LeaseID: lease.ID, Summary: "renewal denied: global gate", RiskLevel: RiskMedium,
		})
		return nil, ErrForbidden
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
	if principal.ExpiresAt != nil && newExpires.After(*principal.ExpiresAt) {
		newExpires = *principal.ExpiresAt
	}
	if !newExpires.After(lease.ExpiresAt) {
		_ = s.appendLeaseEvent(ctx, lease, "renew_denied", model.ActorAI, lease.AIAgentID, map[string]any{"reason": "no extension available"})
		return nil, ErrForbidden
	}
	lease.ExpiresAt = newExpires
	lease.LastRenewedAt = &now
	lease.RenewCount++
	lease.LastHeartbeatAt = &now
	// The authorized_keys line carries a runtime token + expiry marker signed
	// at install time; after renewal the expiry moved, so the node must
	// re-install the key with a fresh token/marker.
	lease.KeyInstalled = false
	lease.KeyInstalledAt = nil
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
func (s *LeaseService) Heartbeat(ctx context.Context, leaseID string, principal *TokenPrincipal) (*model.AILease, error) {
	lease, err := s.authLeaseForPrincipal(ctx, leaseID, principal)
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
func (s *LeaseService) Disconnect(ctx context.Context, leaseID string, principal *TokenPrincipal) (*model.AILease, error) {
	lease, err := s.authLeaseForPrincipal(ctx, leaseID, principal)
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

// authLeaseForPrincipal resolves a lease and verifies the access token owns it.
// Ownership mismatches surface as 404 to avoid leaking whether the lease exists.
func (s *LeaseService) authLeaseForPrincipal(ctx context.Context, leaseID string, principal *TokenPrincipal) (*model.AILease, error) {
	lease, err := s.store.LeaseByID(ctx, leaseID)
	if err != nil {
		return nil, ErrNotFound
	}
	if lease.AccessTokenID != principal.TokenID {
		return nil, ErrNotFound
	}
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
// Returns the revoked leases so the caller can push key-refresh events.
func (s *LeaseService) RevokeAll(ctx context.Context, nodeID, adminID, reason string, terminateSessions bool) (int, []*model.AILease, error) {
	var active []*model.AILease
	var err error
	if nodeID != "" {
		if _, err := s.store.NodeByID(ctx, nodeID); err != nil {
			return 0, nil, ErrNotFound
		}
		active, err = s.store.ActiveLeasesOnNode(ctx, nodeID)
	} else {
		active, err = s.store.ListLeases(ctx, "", model.LeaseActive, 0, 0)
	}
	if err != nil {
		return 0, nil, err
	}
	count := 0
	var revoked []*model.AILease
	for _, l := range active {
		if l.Status != model.LeaseActive {
			continue
		}
		if r, err := s.Revoke(ctx, l.ID, adminID, reason, terminateSessions); err == nil {
			count++
			revoked = append(revoked, r)
		}
	}
	return count, revoked, nil
}

// RevokeTokenCascade revokes an access token and every active lease bound to
// it in a single transaction (called when an admin revokes the token), then
// emits events/audit. Returns the revoked leases so the API layer can push
// key-refresh events to the affected nodes. Idempotent: a second revoke with
// the same token revokes nothing and emits no duplicates.
func (s *LeaseService) RevokeTokenCascade(ctx context.Context, tokenID, adminID, reason string) ([]*model.AILease, error) {
	revoked, err := s.store.RevokeAccessTokenAndLeases(ctx, tokenID, adminID, reason)
	if err != nil {
		return nil, err
	}
	for _, l := range revoked {
		_ = s.appendLeaseEvent(ctx, l, "revoked", model.ActorAdmin, adminID, map[string]any{"reason": reason, "terminate_sessions": false})
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorAdmin, ActorID: adminID, NodeID: l.NodeID, Action: "ai.lease_revoke",
			ResourceType: "ai_lease", ResourceID: l.ID, LeaseID: l.ID,
			Summary: "AI lease revoked (access token revoked)", Details: map[string]any{"reason": reason, "access_token_id": tokenID},
			RiskLevel: RiskHigh,
		})
	}
	return revoked, nil
}

// MigrateLegacyApprovalFlow is a one-time startup maintenance pass for the
// token migration: pending requests from the retired manual-approval flow are
// rejected, active leases without a bound access token are revoked, and leases
// whose token already expired are marked expired. Idempotent.
func (s *LeaseService) MigrateLegacyApprovalFlow(ctx context.Context) error {
	n1, err := s.store.RejectLegacyPendingRequests(ctx, "legacy manual approval flow retired")
	if err != nil {
		return err
	}
	n2, err := s.store.RevokeLegacyUntokenizedLeases(ctx, "legacy lease without access token revoked after token migration")
	if err != nil {
		return err
	}
	n3, err := s.store.ExpireLeasesBeforeAccessToken(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	if n1 > 0 || n2 > 0 || n3 > 0 {
		s.auditor.OK(ctx, AuditInput{
			ActorType: model.ActorSystem, Action: "ai.lease_migrate_legacy",
			Summary: "legacy approval flow migrated to access tokens",
			Details: map[string]any{"rejected_pending_requests": n1, "revoked_untokenized_leases": n2, "expired_token_bound_leases": n3},
			RiskLevel: RiskHigh,
		})
	}
	return nil
}
