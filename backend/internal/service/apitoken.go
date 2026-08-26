package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/security"
	"servercli/internal/store"
)

// TokenService manages Access Tokens (sct_*) that authenticate the external
// AI self-service API. Tokens carry a versioned permission set. New tokens
// start with zero permissions; grants are assigned explicitly against the
// static permission catalog (PermissionCatalog). The historical first version
// granted everything via a canonical wildcard; that wildcard is expanded at
// parse time into the explicit old AI credential surface (never notifications
// or any future resource), and stored rows are rewritten by migration 0006.
type TokenService struct {
	store   *store.Store
	cfg     *config.Config
	log     *slog.Logger
	auditor *Auditor
	envID   string
}

// NewTokenService builds the service.
func NewTokenService(st *store.Store, cfg *config.Config, log *slog.Logger, auditor *Auditor, nodes *NodeService) *TokenService {
	return &TokenService{store: st, cfg: cfg, log: log, auditor: auditor, envID: nodes.EnvID()}
}

// TokenTTLs is the fixed set of supported lifetimes.
var TokenTTLs = []string{model.TokenTTL15m, model.TokenTTL1h, model.TokenTTL6h, model.TokenTTL1d, model.TokenTTL1w, model.TokenTTLNever}

// tokenTTLDuration resolves a TTL enum to a duration. permanent=true means the
// token never expires.
func tokenTTLDuration(ttl string) (d time.Duration, permanent bool, err error) {
	switch ttl {
	case model.TokenTTL15m:
		return 15 * time.Minute, false, nil
	case model.TokenTTL1h:
		return time.Hour, false, nil
	case model.TokenTTL6h:
		return 6 * time.Hour, false, nil
	case model.TokenTTL1d:
		return 24 * time.Hour, false, nil
	case model.TokenTTL1w:
		return 7 * 24 * time.Hour, false, nil
	case model.TokenTTLNever:
		return 0, true, nil
	default:
		return 0, false, fmt.Errorf("%w: ttl must be one of %s", ErrBadRequest, strings.Join(TokenTTLs, ", "))
	}
}

// defaultPermissionsJSON is the zero-permission default for newly created
// tokens: version 1 with no grants. Tokens are granted permissions explicitly
// afterwards via UpdatePermissions.
const defaultPermissionsJSON = `{"version":1,"grants":[]}`

// legacyWildcardPermissionsJSON is the explicit expansion of the historical
// canonical wildcard grant: the old AI credentials surface (node discovery,
// lease requests, lease lifecycle) without notifications or any future
// resource. Migration 0006 rewrites stored wildcard rows to this exact JSON.
const legacyWildcardPermissionsJSON = `{"version":1,"grants":[{"resource":"nodes","actions":["read"]},{"resource":"ai.lease_requests","actions":["create","read"]},{"resource":"ai.leases","actions":["renew","heartbeat","disconnect"]}]}`

// Permission resources and actions used by the AI self-service routes. These
// are the stable identifiers per-token grants key on; they mirror the static
// permission catalog.
const (
	ResourceLeaseRequests = "ai.lease_requests"
	ResourceLeases        = "ai.leases"
	ResourceNodes         = "nodes"
	ResourceNotifications = "notifications"

	// Deployment management resources (deployments, deployment secrets and
	// node bootstrap sessions) used by the AI self-service / operator grants.
	ResourceDeployments       = "deployments"
	ResourceDeploymentSecrets = "deployment_secrets"
	ResourceBootstrapSessions = "bootstrap_sessions"

	ActionCreate     = "create"
	ActionRead       = "read"
	ActionRenew      = "renew"
	ActionHeartbeat  = "heartbeat"
	ActionDisconnect = "disconnect"
	ActionSend       = "send"

	// Deployment action verbs (skipped when already defined above).
	ActionInstall   = "install"
	ActionUpdate    = "update"
	ActionBackup    = "backup"
	ActionRollback  = "rollback"
	ActionConfigure = "configure"
	ActionManage    = "manage"
	ActionRevoke    = "revoke"
)

// TokenPrincipal is the authenticated identity for a request carrying a
// valid access token.
type TokenPrincipal struct {
	TokenID     string
	Name        string
	TokenPrefix string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	Permissions PermissionSet
}

// PermissionSet is the persisted permission model of a token.
type PermissionSet struct {
	Version int               `json:"version"`
	Grants  []PermissionGrant `json:"grants"`
}

// PermissionGrant grants a set of actions on a resource, optionally narrowed
// by constraints (e.g. {"permission_profiles":["read-only"]}).
type PermissionGrant struct {
	Resource    string         `json:"resource"`
	Actions     []string       `json:"actions"`
	Constraints map[string]any `json:"constraints,omitempty"`
}

// PermissionDef is one entry of the static permission catalog. Category is the
// machine name of a top-level group ("notifications" or "ai_credentials");
// Label is the human-facing label of the individual permission.
type PermissionDef struct {
	Category    string `json:"category"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// PermissionCategory is a top-level permission group with its display label.
type PermissionCategory struct {
	Category string `json:"category"`
	Label    string `json:"label"`
}

// PermissionCategories returns the two top-level permission categories.
func PermissionCategories() []PermissionCategory {
	return []PermissionCategory{
		{Category: "notifications", Label: "通知"},
		{Category: "ai_credentials", Label: "AI 凭证"},
	}
}

// PermissionCatalog returns the static permission catalog. The result is a
// fresh slice so callers cannot mutate the shared definition.
func PermissionCatalog() []PermissionDef {
	return []PermissionDef{
		{Category: "notifications", Resource: ResourceNotifications, Action: ActionSend, Label: "发送通知", Description: "允许发送通知（如 Server 酱等渠道消息）"},
		{Category: "ai_credentials", Resource: ResourceNodes, Action: ActionRead, Label: "节点发现（只读）", Description: "允许读取节点发现信息（只读）"},
		{Category: "ai_credentials", Resource: ResourceLeaseRequests, Action: ActionCreate, Label: "申请 AI Lease", Description: "允许申请新的 AI Lease"},
		{Category: "ai_credentials", Resource: ResourceLeaseRequests, Action: ActionRead, Label: "查询本人申请", Description: "允许查询本人发起的 Lease 申请"},
		{Category: "ai_credentials", Resource: ResourceLeases, Action: ActionRenew, Label: "续期 Lease", Description: "允许续期本人持有的 Lease"},
		{Category: "ai_credentials", Resource: ResourceLeases, Action: ActionHeartbeat, Label: "Lease 心跳", Description: "允许对本人持有的 Lease 发送心跳"},
		{Category: "ai_credentials", Resource: ResourceLeases, Action: ActionDisconnect, Label: "断开 Lease", Description: "允许断开本人持有的 Lease"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionRead, Label: "部署管理（只读）", Description: "允许读取部署管理信息（只读）"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionConfigure, Label: "配置部署", Description: "允许配置部署（Feature、配置 Profile、仓库同步等）"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionInstall, Label: "安装部署", Description: "允许执行安装部署操作"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionUpdate, Label: "更新部署", Description: "允许执行更新部署操作"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionBackup, Label: "备份部署", Description: "允许执行备份部署操作"},
		{Category: "部署管理", Resource: ResourceDeployments, Action: ActionRollback, Label: "回滚部署", Description: "允许执行回滚部署操作"},
		{Category: "部署管理", Resource: ResourceDeploymentSecrets, Action: ActionManage, Label: "部署 Secret 管理", Description: "允许覆盖/管理部署 Secret"},
		{Category: "部署管理", Resource: ResourceBootstrapSessions, Action: ActionCreate, Label: "节点引导会话（创建）", Description: "允许创建节点引导会话"},
		{Category: "部署管理", Resource: ResourceBootstrapSessions, Action: ActionRead, Label: "节点引导会话（查看）", Description: "允许查看节点引导会话"},
		{Category: "部署管理", Resource: ResourceBootstrapSessions, Action: ActionRevoke, Label: "节点引导会话（撤销）", Description: "允许撤销节点引导会话"},
	}
}

// legacyAICredentialGrants returns the explicit expansion of the historical
// canonical wildcard grant: the old AI credentials surface, never
// notifications or any future resource.
func legacyAICredentialGrants() []PermissionGrant {
	return []PermissionGrant{
		{Resource: ResourceNodes, Actions: []string{ActionRead}},
		{Resource: ResourceLeaseRequests, Actions: []string{ActionCreate, ActionRead}},
		{Resource: ResourceLeases, Actions: []string{ActionRenew, ActionHeartbeat, ActionDisconnect}},
	}
}

// permissionKey joins a resource and action for catalog lookups.
func permissionKey(resource, action string) string { return resource + "\x00" + action }

// permissionCatalogIndex builds a resource/action lookup map from the static
// catalog.
func permissionCatalogIndex() map[string]PermissionDef {
	idx := make(map[string]PermissionDef, len(PermissionCatalog()))
	for _, d := range PermissionCatalog() {
		idx[permissionKey(d.Resource, d.Action)] = d
	}
	return idx
}

// GenerateAccessToken returns a new sct_ token with 32 random bytes.
func GenerateAccessToken() (string, error) {
	raw, err := security.NewToken(32)
	if err != nil {
		return "", err
	}
	return "sct_" + raw, nil
}

// CreateTokenInput is the admin request to create a token.
type CreateTokenInput struct {
	Name string `json:"name"`
	TTL  string `json:"ttl"`
}

// CreateTokenResult carries the created token plus the one-time plaintext.
type CreateTokenResult struct {
	Token     *model.APIAccessToken `json:"api_token"`
	Plaintext string                `json:"token"`
}

// Create issues a new access token with zero permissions. The plaintext is
// returned exactly once. The create endpoint never grants permissions; grants
// are assigned later via UpdatePermissions.
func (s *TokenService) Create(ctx context.Context, in CreateTokenInput, adminID string) (*CreateTokenResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrBadRequest)
	}
	if len(name) > 100 {
		return nil, fmt.Errorf("%w: name too long (max 100)", ErrBadRequest)
	}
	ttl, permanent, err := tokenTTLDuration(in.TTL)
	if err != nil {
		return nil, err
	}
	plaintext, err := GenerateAccessToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var expiresAt *time.Time
	if !permanent {
		e := now.Add(ttl)
		expiresAt = &e
	}
	tok := &model.APIAccessToken{
		ID:                model.NewUUID(),
		EnvironmentID:     s.envID,
		Name:              name,
		TokenHash:         security.HashToken(plaintext),
		TokenPrefix:       security.Prefix(plaintext, 16),
		CreatedBy:         adminID,
		CreatedAt:         now,
		ExpiresAt:         expiresAt,
		PermissionVersion: 1,
		PermissionsJSON:   defaultPermissionsJSON,
	}
	if err := s.store.CreateAccessToken(ctx, tok); err != nil {
		return nil, err
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "api_token.create",
		ResourceType: "api_access_token", ResourceID: tok.ID,
		Summary:   "access token created",
		Details:   map[string]any{"name": tok.Name, "token_prefix": tok.TokenPrefix, "ttl": in.TTL, "expires_at": tok.ExpiresAt},
		RiskLevel: RiskHigh,
	})
	return &CreateTokenResult{Token: tok, Plaintext: plaintext}, nil
}

// Resolve authenticates a bearer token. On success it returns the principal
// (state=valid). When the token exists but is expired/revoked it returns the
// token row (for usage logging) with the matching state alongside the error.
// A token whose permission JSON cannot be parsed still authenticates: the
// principal carries an empty permission set (fail closed) so subsequent
// Authorize calls deny, and the warning is logged without sensitive data.
func (s *TokenService) Resolve(ctx context.Context, plaintext string) (*TokenPrincipal, *model.APIAccessToken, string, error) {
	if !strings.HasPrefix(plaintext, "sct_") {
		return nil, nil, "", ErrNotAuthenticated
	}
	tok, err := s.store.AccessTokenByHash(ctx, security.HashToken(plaintext))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, "", ErrNotAuthenticated
		}
		return nil, nil, "", err
	}
	now := time.Now().UTC()
	if tok.RevokedAt != nil {
		return nil, tok, model.TokenStateRevoked, ErrNotAuthenticated
	}
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(now) {
		return nil, tok, model.TokenStateExpired, ErrNotAuthenticated
	}
	perms, err := parsePermissions(tok.PermissionsJSON)
	if err != nil {
		s.log.Warn("invalid permission json, token denied", "token_id", tok.ID)
		perms = PermissionSet{Version: 1}
	}
	return &TokenPrincipal{
		TokenID:     tok.ID,
		Name:        tok.Name,
		TokenPrefix: tok.TokenPrefix,
		ExpiresAt:   tok.ExpiresAt,
		RevokedAt:   tok.RevokedAt,
		Permissions: perms,
	}, tok, model.TokenStateValid, nil
}

// ParsePermissions parses a persisted permission JSON. It is the exported
// wrapper around parsePermissions for the API layer.
func ParsePermissions(raw string) (PermissionSet, error) {
	return parsePermissions(raw)
}

// parsePermissions parses a persisted permission JSON. Empty input yields a
// zero-grant version-1 set (fail closed). Malformed JSON, non-version-1 sets
// and any non-canonical wildcard yield an empty set plus an error (fail
// closed). The historical canonical wildcard (with or without an empty
// constraints object) expands to the explicit old AI credentials grants and
// can never authorize notifications or future resources.
func parsePermissions(raw string) (PermissionSet, error) {
	if strings.TrimSpace(raw) == "" {
		return PermissionSet{Version: 1}, nil
	}
	var p PermissionSet
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return PermissionSet{}, err
	}
	if p.Version != 1 {
		return PermissionSet{}, fmt.Errorf("unsupported permission version %d", p.Version)
	}
	if wildcardIn(p) {
		if !isCanonicalWildcard(p) {
			return PermissionSet{}, errors.New("wildcard permission grants are not allowed")
		}
		return PermissionSet{Version: 1, Grants: legacyAICredentialGrants()}, nil
	}
	return p, nil
}

// wildcardIn reports whether any grant uses a "*" resource or action.
func wildcardIn(p PermissionSet) bool {
	for _, g := range p.Grants {
		if g.Resource == "*" {
			return true
		}
		for _, a := range g.Actions {
			if a == "*" {
				return true
			}
		}
	}
	return false
}

// isCanonicalWildcard reports whether p is exactly the historical canonical
// wildcard grant: a single "*"/["*"] grant with no constraints.
func isCanonicalWildcard(p PermissionSet) bool {
	if len(p.Grants) != 1 {
		return false
	}
	g := p.Grants[0]
	return g.Resource == "*" && len(g.Actions) == 1 && g.Actions[0] == "*" && len(g.Constraints) == 0
}

// Authorize checks a token principal against a resource/action pair with exact
// matching: a grant authorizes only when its resource equals resource and its
// actions contain action. Wildcards are never authorized here — the historical
// canonical wildcard is expanded at parse time into the old AI credentials
// set, so it can never authorize notifications.
func (s *TokenService) Authorize(p *TokenPrincipal, resource, action string, _ map[string]any) error {
	for _, g := range p.Permissions.Grants {
		if g.Resource != resource {
			continue
		}
		for _, a := range g.Actions {
			if a == action {
				return nil
			}
		}
	}
	return ErrForbidden
}

// ValidatePermissionSet validates a permission set for storage via the admin
// update API: the version must be 1; every grant resource/action must not
// contain "*" and must exactly match the static catalog; a grant's actions
// must be non-empty and free of duplicates; constraints are always a JSON
// object (map) in this model — a non-object JSON value cannot be represented
// and would be rejected at JSON decode time.
func ValidatePermissionSet(p PermissionSet) error {
	if p.Version != 1 {
		return fmt.Errorf("%w: permission version must be 1", ErrBadRequest)
	}
	if p.Grants == nil {
		return nil
	}
	idx := permissionCatalogIndex()
	seen := make(map[string]bool)
	for i, g := range p.Grants {
		if g.Resource == "" || strings.Contains(g.Resource, "*") {
			return fmt.Errorf("%w: grant %d resource must not be empty or contain wildcard", ErrBadRequest, i)
		}
		if len(g.Actions) == 0 {
			return fmt.Errorf("%w: grant %d actions must not be empty", ErrBadRequest, i)
		}
		for _, a := range g.Actions {
			if a == "" || strings.Contains(a, "*") {
				return fmt.Errorf("%w: grant %d action %q must not be empty or contain wildcard", ErrBadRequest, i, a)
			}
			key := permissionKey(g.Resource, a)
			if seen[key] {
				return fmt.Errorf("%w: duplicate permission %s:%s", ErrBadRequest, g.Resource, a)
			}
			seen[key] = true
			if _, ok := idx[key]; !ok {
				return fmt.Errorf("%w: unknown permission %s:%s", ErrBadRequest, g.Resource, a)
			}
		}
	}
	return nil
}

// UpdatePermissions replaces a token's permission set under an optimistic
// lock: the store only updates when the current permission_version equals
// expectedRevision, and bumps the revision by one. Revoked or expired tokens
// are rejected with ErrConflict. On success an audit event is written with
// admin, token prefix, before/after grants and the new revision only.
func (s *TokenService) UpdatePermissions(ctx context.Context, tokenID string, expectedRevision int, perms PermissionSet, adminID string) (*model.APIAccessToken, error) {
	t, err := s.store.AccessTokenByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	now := time.Now().UTC()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
		return nil, ErrConflict
	}
	if err := ValidatePermissionSet(perms); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(perms)
	if err != nil {
		return nil, err
	}
	updated, err := s.store.UpdateAccessTokenPermissions(ctx, tokenID, expectedRevision, string(raw))
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, ErrConflict
	}
	latest, err := s.store.AccessTokenByID(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	var before []PermissionGrant
	if bp, perr := parsePermissions(t.PermissionsJSON); perr == nil {
		before = bp.Grants
	}
	s.auditor.OK(ctx, AuditInput{
		ActorType: model.ActorAdmin, ActorID: adminID, Action: "api_token.permissions.update",
		ResourceType: "api_access_token", ResourceID: tokenID,
		Summary: "access token permissions updated",
		Details: map[string]any{
			"admin":         adminID,
			"token_prefix":  t.TokenPrefix,
			"before_grants": before,
			"after_grants":  perms.Grants,
			"revision":      latest.PermissionVersion,
		},
		RiskLevel: RiskHigh,
	})
	return latest, nil
}

// PermissionRevision returns the current permission revision of a token.
func PermissionRevision(t *model.APIAccessToken) int {
	if t == nil {
		return 0
	}
	return t.PermissionVersion
}

// ScanInvalidPermissions is a startup hygiene scan (to be wired by main): it
// lists every access token and warns (log + high-risk audit event) about rows
// whose permission JSON is missing, empty, invalid, or still carries a
// residual wildcard. Valid zero-permission or normal grant sets are skipped.
func (s *TokenService) ScanInvalidPermissions(ctx context.Context) error {
	tokens, err := s.store.ListAccessTokens(ctx, 0, 0)
	if err != nil {
		return err
	}
	for _, t := range tokens {
		reason := ""
		raw := strings.TrimSpace(t.PermissionsJSON)
		switch {
		case raw == "":
			reason = "missing or empty permission json"
		default:
			if _, perr := parsePermissions(raw); perr != nil {
				reason = "invalid permission json"
			} else if residualWildcard(raw) {
				reason = "residual wildcard permission"
			}
		}
		if reason == "" {
			continue
		}
		s.log.Warn("invalid permission json, token denied", "token_id", t.ID, "reason", reason)
		_ = s.auditor.Failure(ctx, AuditInput{
			ActorType: model.ActorSystem, Action: "api_token.permissions.invalid",
			ResourceType: "api_access_token", ResourceID: t.ID,
			Summary: "invalid token permissions",
			Details: map[string]any{
				"token_id":     t.ID,
				"token_prefix": t.TokenPrefix,
				"reason":       reason,
			},
			RiskLevel: RiskHigh,
		})
	}
	return nil
}

// residualWildcard reports whether a permission JSON still contains a "*"
// resource or action grant (i.e. a canonical wildcard the 0006 migration
// should have rewritten, or a non-canonical variant).
func residualWildcard(raw string) bool {
	var probe struct {
		Grants []struct {
			Resource string   `json:"resource"`
			Actions  []string `json:"actions"`
		} `json:"grants"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false
	}
	for _, g := range probe.Grants {
		if g.Resource == "*" {
			return true
		}
		for _, a := range g.Actions {
			if a == "*" {
				return true
			}
		}
	}
	return false
}

// Token returns one token (no hash/plaintext).
func (s *TokenService) Token(ctx context.Context, id string) (*model.APIAccessToken, error) {
	t, err := s.store.AccessTokenByID(ctx, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return t, nil
}

// List returns tokens newest first.
func (s *TokenService) List(ctx context.Context, limit, offset int) ([]*model.APIAccessToken, error) {
	return s.store.ListAccessTokens(ctx, limit, offset)
}

// UsageLogs returns the usage log for a token.
func (s *TokenService) UsageLogs(ctx context.Context, tokenID, outcome string, limit, offset int) ([]*model.APITokenUsageLog, error) {
	if _, err := s.store.AccessTokenByID(ctx, tokenID); err != nil {
		return nil, ErrNotFound
	}
	return s.store.ListTokenUsageLogs(ctx, tokenID, outcome, limit, offset)
}
