package service

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
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
	// New tokens default to zero permissions.
	if res.Token.PermissionsJSON != defaultPermissionsJSON {
		t.Fatalf("new token must carry zero permissions, got %s", res.Token.PermissionsJSON)
	}
	if res.Token.PermissionVersion != 1 {
		t.Fatalf("new token permission version must be 1, got %d", res.Token.PermissionVersion)
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
	// Zero-permission default denies every resource/action.
	if err := svc.Authorize(principal, ResourceLeaseRequests, ActionCreate, nil); err == nil {
		t.Fatal("zero-permission token must not authorize lease requests")
	}
	if err := svc.Authorize(principal, ResourceNodes, ActionRead, nil); err == nil {
		t.Fatal("zero-permission token must not authorize node discovery")
	}
	authErr := svc.Authorize(principal, ResourceNotifications, ActionSend, nil)
	if authErr == nil {
		t.Fatal("zero-permission token must not authorize notifications")
	}
	if !errors.Is(authErr, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", authErr)
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

func TestPermissionCatalog(t *testing.T) {
	cat := PermissionCatalog()
	if len(cat) != 7 {
		t.Fatalf("catalog must have 7 permissions, got %d", len(cat))
	}
	cats := map[string]int{}
	for _, d := range cat {
		cats[d.Category]++
		if d.Resource == "" || d.Action == "" || d.Label == "" {
			t.Fatalf("catalog entry incomplete: %+v", d)
		}
		// Every catalog entry must be a valid permission set on its own.
		p := PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: d.Resource, Actions: []string{d.Action}}}}
		if err := ValidatePermissionSet(p); err != nil {
			t.Fatalf("catalog entry %s:%s fails validation: %v", d.Resource, d.Action, err)
		}
	}
	if cats["notifications"] != 1 || cats["ai_credentials"] != 6 {
		t.Fatalf("unexpected category distribution: %v", cats)
	}
	categories := PermissionCategories()
	if len(categories) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(categories))
	}
	byCat := map[string]string{}
	for _, c := range categories {
		byCat[c.Category] = c.Label
	}
	if byCat["notifications"] != "通知" || byCat["ai_credentials"] != "AI 凭证" {
		t.Fatalf("unexpected category labels: %v", byCat)
	}
	// The legacy wildcard constant must match the expansion struct exactly.
	var want PermissionSet
	want.Version = 1
	want.Grants = legacyAICredentialGrants()
	var got PermissionSet
	if err := json.Unmarshal([]byte(legacyWildcardPermissionsJSON), &got); err != nil {
		t.Fatal(err)
	}
	gotRaw, _ := json.Marshal(got)
	wantRaw, _ := json.Marshal(want)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("legacy constant drift: got %s want %s", gotRaw, wantRaw)
	}
}

func TestParsePermissions(t *testing.T) {
	// Empty input yields a zero-grant version-1 set without error.
	p, err := parsePermissions("")
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if p.Version != 1 || len(p.Grants) != 0 {
		t.Fatalf("empty should be zero-grant v1, got %+v", p)
	}
	// Explicit zero-permission JSON.
	p, err = parsePermissions(`{"version":1,"grants":[]}`)
	if err != nil || len(p.Grants) != 0 {
		t.Fatalf("zero json: %v %+v", err, p)
	}
	// Canonical wildcard (with constraints) expands to the legacy AI set.
	for _, raw := range []string{
		`{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{}}]}`,
		`{"version":1,"grants":[{"resource":"*","actions":["*"]}]}`,
	} {
		p, err := parsePermissions(raw)
		if err != nil {
			t.Fatalf("canonical wildcard %q: %v", raw, err)
		}
		want := legacyAICredentialGrants()
		if len(p.Grants) != len(want) {
			t.Fatalf("canonical wildcard %q: got %d grants want %d", raw, len(p.Grants), len(want))
		}
		for i, g := range want {
			if p.Grants[i].Resource != g.Resource || len(p.Grants[i].Actions) != len(g.Actions) {
				t.Fatalf("canonical wildcard %q grant %d mismatch: %+v vs %+v", raw, i, p.Grants[i], g)
			}
			for j, a := range g.Actions {
				if p.Grants[i].Actions[j] != a {
					t.Fatalf("canonical wildcard %q grant %d action mismatch", raw, i)
				}
			}
		}
		// The expansion must never grant notifications.
		if err := (&TokenService{}).Authorize(&TokenPrincipal{Permissions: p}, ResourceNotifications, ActionSend, nil); !errors.Is(err, ErrForbidden) {
			t.Fatalf("canonical wildcard must never authorize notifications, got %v", err)
		}
	}
	// Non-canonical wildcards fail closed.
	for _, raw := range []string{
		`{"version":1,"grants":[{"resource":"nodes","actions":["*"]}]}`,
		`{"version":1,"grants":[{"resource":"*","actions":["read"]}]}`,
		`{"version":1,"grants":[{"resource":"*","actions":["*"],"constraints":{"x":"y"}}]}`,
		`{"version":1,"grants":[{"resource":"*","actions":["*","read"]}]}`,
	} {
		p, err := parsePermissions(raw)
		if err == nil {
			t.Fatalf("non-canonical wildcard %q should fail, got %+v", raw, p)
		}
		if len(p.Grants) != 0 {
			t.Fatalf("non-canonical wildcard %q must fail closed with empty grants", raw)
		}
	}
	// Unsupported version fails closed.
	if _, err := parsePermissions(`{"version":2,"grants":[]}`); err == nil {
		t.Fatal("version 2 should fail")
	}
	// Malformed JSON fails closed.
	if _, err := parsePermissions(`not json`); err == nil {
		t.Fatal("malformed json should fail")
	}
}

func TestValidatePermissionSet(t *testing.T) {
	valid := []PermissionSet{
		{Version: 1},
		{Version: 1, Grants: nil},
		{Version: 1, Grants: []PermissionGrant{{Resource: ResourceNotifications, Actions: []string{ActionSend}}}},
		{Version: 1, Grants: []PermissionGrant{{Resource: ResourceLeaseRequests, Actions: []string{ActionCreate, ActionRead}}}},
		{Version: 1, Grants: legacyAICredentialGrants()},
	}
	for i, p := range valid {
		if err := ValidatePermissionSet(p); err != nil {
			t.Fatalf("valid set %d should pass: %v", i, err)
		}
	}
	invalid := []struct {
		name string
		p    PermissionSet
	}{
		{"version 2", PermissionSet{Version: 2, Grants: []PermissionGrant{{Resource: ResourceNodes, Actions: []string{ActionRead}}}}},
		{"wildcard resource", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: "*", Actions: []string{ActionRead}}}}},
		{"wildcard action", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: ResourceNodes, Actions: []string{"*"}}}}},
		{"unknown permission", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: "secret_stuff", Actions: []string{ActionRead}}}}},
		{"unknown action", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: ResourceNodes, Actions: []string{"delete"}}}}},
		{"duplicate action", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: ResourceLeaseRequests, Actions: []string{ActionRead, ActionRead}}}}},
		{"cross-grant duplicate", PermissionSet{Version: 1, Grants: []PermissionGrant{
			{Resource: ResourceNodes, Actions: []string{ActionRead}},
			{Resource: ResourceNodes, Actions: []string{ActionRead}},
		}}},
		{"empty actions", PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: ResourceNodes, Actions: []string{}}}}},
	}
	for _, c := range invalid {
		if err := ValidatePermissionSet(c.p); err == nil {
			t.Fatalf("%s should fail validation", c.name)
		}
	}
}

func TestUpdatePermissions(t *testing.T) {
	ctx, st, cfg, _, nodes, _, _, _, _ := testServices(t)
	log := logger.New(io.Discard, "error")
	auditor := NewAuditor(st, log, cfg.InstanceName+"-env", cfg.InstanceName)
	svc := NewTokenService(st, cfg, log, auditor, nodes)

	res, err := svc.Create(ctx, CreateTokenInput{Name: "perm-update", TTL: model.TokenTTL1h}, "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	tokID := res.Token.ID
	if got := PermissionRevision(res.Token); got != 1 {
		t.Fatalf("initial revision must be 1, got %d", got)
	}

	sendPerms := PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: ResourceNotifications, Actions: []string{ActionSend}}}}

	// First update with the current revision succeeds and bumps to 2.
	updated, err := svc.UpdatePermissions(ctx, tokID, 1, sendPerms, "admin-1")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.PermissionVersion != 2 {
		t.Fatalf("expected revision 2 after update, got %d", updated.PermissionVersion)
	}
	if updated.PermissionsJSON == "" || !strings.Contains(updated.PermissionsJSON, `"notifications"`) {
		t.Fatalf("updated permissions json missing notifications: %s", updated.PermissionsJSON)
	}

	// Resolve and authorize against the new grants.
	principal, _, state, err := svc.Resolve(ctx, res.Plaintext)
	if err != nil || state != model.TokenStateValid {
		t.Fatalf("resolve after update: %v %s", err, state)
	}
	if err := svc.Authorize(principal, ResourceNotifications, ActionSend, nil); err != nil {
		t.Fatalf("send should be authorized: %v", err)
	}
	if err := svc.Authorize(principal, ResourceNodes, ActionRead, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nodes read should stay forbidden, got %v", err)
	}

	// A stale revision conflicts.
	if _, err := svc.UpdatePermissions(ctx, tokID, 1, sendPerms, "admin-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision should conflict, got %v", err)
	}

	// Changing the JSON directly through the store (simulating a concurrent
	// writer) then updating with the stale revision must also conflict.
	aiPerms := PermissionSet{Version: 1, Grants: legacyAICredentialGrants()}
	raw, err := json.Marshal(aiPerms)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := st.UpdateAccessTokenPermissions(ctx, tokID, 2, string(raw))
	if err != nil || !ok {
		t.Fatalf("store direct update: ok=%v err=%v", ok, err)
	}
	if _, err := svc.UpdatePermissions(ctx, tokID, 2, sendPerms, "admin-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("update after concurrent store change should conflict, got %v", err)
	}

	// Unknown token -> not found.
	if _, err := svc.UpdatePermissions(ctx, "missing", 1, sendPerms, "admin-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token should be not found, got %v", err)
	}

	// Invalid permission set -> bad request, nothing changed.
	if _, err := svc.UpdatePermissions(ctx, tokID, 3, PermissionSet{Version: 1, Grants: []PermissionGrant{{Resource: "nope", Actions: []string{ActionRead}}}}, "admin-1"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("invalid permission set should be bad request, got %v", err)
	}
	after, err := svc.Token(ctx, tokID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PermissionVersion != 3 {
		t.Fatalf("failed update must not bump revision, got %d", after.PermissionVersion)
	}
}
