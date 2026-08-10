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
// AI self-service API. Tokens carry a permission set; the first version grants
// everything, but the Authorize surface is the extension point for per-resource
// and per-action constraints (e.g. "this token may only request read-only
// leases").
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

// defaultPermissions is the first-version permission set: full access to every
// resource and action. Future versions narrow grants per token.
const defaultPermissionsJSON = `{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{}}]}`

// Permission resources and actions used by the AI self-service routes. These
// are the stable identifiers future per-token constraints will key on.
const (
	ResourceLeaseRequests = "ai.lease_requests"
	ResourceLeases        = "ai.leases"
	ResourceNodes         = "nodes"

	ActionCreate    = "create"
	ActionRead      = "read"
	ActionRenew     = "renew"
	ActionHeartbeat = "heartbeat"
	ActionDisconnect = "disconnect"
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

// Create issues a new access token. The plaintext is returned exactly once.
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
		Summary: "access token created",
		Details: map[string]any{"name": tok.Name, "token_prefix": tok.TokenPrefix, "ttl": in.TTL, "expires_at": tok.ExpiresAt},
		RiskLevel: RiskHigh,
	})
	return &CreateTokenResult{Token: tok, Plaintext: plaintext}, nil
}

// Resolve authenticates a bearer token. On success it returns the principal
// (state=valid). When the token exists but is expired/revoked it returns the
// token row (for usage logging) with the matching state alongside the error.
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
		return nil, nil, "", fmt.Errorf("parse permissions: %w", err)
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

func parsePermissions(raw string) (PermissionSet, error) {
	var p PermissionSet
	if raw == "" {
		p = PermissionSet{Version: 1}
		return p, nil
	}
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}

// Authorize checks a token principal against a resource/action pair. This is
// the unified permission surface future per-token constraints plug into; v1
// tokens carry a wildcard grant so authorization always succeeds.
func (s *TokenService) Authorize(p *TokenPrincipal, resource, action string, _ map[string]any) error {
	for _, g := range p.Permissions.Grants {
		if g.Resource != "*" && g.Resource != resource {
			continue
		}
		for _, a := range g.Actions {
			if a == "*" || a == action {
				return nil
			}
		}
	}
	return ErrForbidden
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
