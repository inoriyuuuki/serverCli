package service

import (
	"io"
	"testing"

	"servercli/internal/logger"
	"servercli/internal/model"
	"servercli/internal/security"
)

func TestTokenTTLDuration(t *testing.T) {
	cases := []struct {
		ttl       string
		permanent bool
		wantErr   bool
	}{
		{model.TokenTTL15m, false, false},
		{model.TokenTTL1h, false, false},
		{model.TokenTTL6h, false, false},
		{model.TokenTTL1d, false, false},
		{model.TokenTTL1w, false, false},
		{model.TokenTTLNever, true, false},
		{"10m", false, true},
		{"", false, true},
	}
	for _, c := range cases {
		_, permanent, err := tokenTTLDuration(c.ttl)
		if c.wantErr && err == nil {
			t.Fatalf("ttl %q: expected error", c.ttl)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("ttl %q: unexpected error %v", c.ttl, err)
		}
		if permanent != c.permanent {
			t.Fatalf("ttl %q: permanent=%v want %v", c.ttl, permanent, c.permanent)
		}
	}
}

func TestTokenServiceCreateResolveRevoke(t *testing.T) {
	ctx, st, cfg, _, nodes, _, leases, _, _ := testServices(t)
	log := logger.New(io.Discard, "error")
	auditor := NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	svc := NewTokenService(st, cfg, log, auditor, nodes)

	res, err := svc.Create(ctx, CreateTokenInput{Name: "agent-x", TTL: model.TokenTTL1h}, "admin-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Plaintext == "" || len(res.Plaintext) <= 4 || res.Plaintext[:4] != "sct_" {
		t.Fatalf("plaintext format wrong: %q", res.Plaintext)
	}
	if res.Token.ExpiresAt == nil {
		t.Fatal("1h token must have expires_at")
	}
	// Store only holds the hash.
	byID, err := svc.Token(ctx, res.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.TokenHash == "" || byID.TokenHash == res.Plaintext {
		t.Fatal("token hash not stored securely")
	}

	// Resolve works for a valid token.
	principal, tok, state, err := svc.Resolve(ctx, res.Plaintext)
	if err != nil || state != model.TokenStateValid || tok == nil {
		t.Fatalf("resolve: %v state=%s", err, state)
	}
	if principal.TokenID != res.Token.ID || principal.Name != "agent-x" {
		t.Fatalf("principal mismatch: %+v", principal)
	}
	// Wildcard grant authorizes every resource/action.
	if err := svc.Authorize(principal, ResourceLeaseRequests, ActionCreate, nil); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := svc.Authorize(principal, ResourceLeases, ActionRenew, nil); err != nil {
		t.Fatalf("authorize renew: %v", err)
	}

	// Unknown and malformed tokens fail without leaking.
	unknown, _ := security.NewToken(32)
	if _, _, _, err := svc.Resolve(ctx, "sct_"+unknown); err == nil {
		t.Fatal("unknown token should fail")
	}
	if _, _, _, err := svc.Resolve(ctx, "bearer-token"); err == nil {
		t.Fatal("non-sct token should fail")
	}

	// Revoke (token + cascade, single transaction) makes it unusable; the token
	// row keeps the revoked state and a second revoke is a no-op (idempotent).
	affected, err := leases.RevokeTokenCascade(ctx, res.Token.ID, "admin-1", "test revoke")
	if err != nil {
		t.Fatalf("revoke cascade: %v", err)
	}
	if len(affected) != 0 {
		t.Fatalf("expected no leases for fresh token, got %d", len(affected))
	}
	byID, err = svc.Token(ctx, res.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.RevokedAt == nil {
		t.Fatal("revoked_at not set")
	}
	_, tok2, state2, err := svc.Resolve(ctx, res.Plaintext)
	if err == nil {
		t.Fatal("revoked token should fail resolve")
	}
	if state2 != model.TokenStateRevoked || tok2 == nil {
		t.Fatalf("expected revoked state with token row, got state=%s", state2)
	}

	// Idempotent: a second revoke returns no leases and emits no duplicates.
	affected2, err := leases.RevokeTokenCascade(ctx, res.Token.ID, "admin-1", "test revoke")
	if err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if len(affected2) != 0 {
		t.Fatalf("idempotent revoke should return no leases, got %d", len(affected2))
	}
}

func TestTokenServicePermanentAndExpiry(t *testing.T) {
	ctx, st, cfg, _, nodes, _, _, _, _ := testServices(t)
	log := logger.New(io.Discard, "error")
	auditor := NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	svc := NewTokenService(st, cfg, log, auditor, nodes)

	perm, err := svc.Create(ctx, CreateTokenInput{Name: "perm", TTL: model.TokenTTLNever}, "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if perm.Token.ExpiresAt != nil {
		t.Fatal("permanent token must have null expires_at")
	}
	if _, _, state, err := svc.Resolve(ctx, perm.Plaintext); err != nil || state != model.TokenStateValid {
		t.Fatalf("permanent token resolve: %v %s", err, state)
	}
}
