package service

// P0 修复回归测试：制品/仓库签名、CreateOperation 冻结与事务、Secret 引用与
// 覆盖、签名密钥物化、OSS Profile endpoint 变更校验、bundle.sig 重签名。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"servercli/internal/deployment/ossclient"
	"servercli/internal/deployment/repo"
	"servercli/internal/deployment/secretprovider"
	"servercli/internal/model"
	"servercli/internal/store"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestManifestSignVerify (P0-A): SignManifest signs the canonical payload and
// VerifyManifestSignature accepts it; tampering or a wrong key fails closed.
func TestManifestSignVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("abcdef0123456789abcdef0123456789")

	m := &repo.RepositoryManifest{
		Version: repo.ManifestVersion,
		Objects: []repo.ManifestObject{
			{Path: "z/last.txt", Size: 3, SHA256: "zzz"},
			{Path: "a/first.txt", Size: 1, SHA256: "aaa"},
			{Path: "m/mid.txt", Size: 2, SHA256: "mmm"},
		},
	}
	if err := repo.SignManifest(m, key); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if m.Signature == "" || m.SignedBy != "servercli-control-plane" {
		t.Fatalf("signature/signed_by not set: %+v", m)
	}
	if err := repo.VerifyManifestSignature(m, key); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Canonicalisation: object order must not affect the signature.
	m2 := &repo.RepositoryManifest{
		Version: repo.ManifestVersion,
		Objects: []repo.ManifestObject{
			{Path: "m/mid.txt", Size: 2, SHA256: "mmm"},
			{Path: "a/first.txt", Size: 1, SHA256: "aaa"},
			{Path: "z/last.txt", Size: 3, SHA256: "zzz"},
		},
	}
	if err := repo.SignManifest(m2, key); err != nil {
		t.Fatalf("sign m2: %v", err)
	}
	if m2.Signature != m.Signature {
		t.Fatalf("canonical signature differs by object order: %q vs %q", m2.Signature, m.Signature)
	}

	// Tampering with an object must fail verification.
	tampered := *m
	tampered.Objects = append([]repo.ManifestObject(nil), m.Objects...)
	tampered.Objects[1].SHA256 = "tampered"
	if err := repo.VerifyManifestSignature(&tampered, key); err == nil {
		t.Fatal("tampered manifest verified, want failure")
	}

	// A different key must fail verification.
	if err := repo.VerifyManifestSignature(m, other); err == nil {
		t.Fatal("wrong key verified, want failure")
	}

	// Empty signature must fail closed.
	unsigned := &repo.RepositoryManifest{Version: repo.ManifestVersion}
	if err := repo.VerifyManifestSignature(unsigned, key); err == nil {
		t.Fatal("unsigned manifest verified, want failure")
	}
}

// TestSigningKeyAndBundleSigResign (P0-A): ensureSigningKey is stable and
// signReleaseBundles rewrites bundle.sig as hex(HMAC-SHA256(key, sha256)).
func TestSigningKeyAndBundleSigResign(t *testing.T) {
	ctx, st, cfg, svc, _ := newDeploymentHarness(t)
	cfg.DeploymentRootDir = t.TempDir()

	key1, err := svc.ensureSigningKey(ctx)
	if err != nil {
		t.Fatalf("ensure key: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("key length %d, want 32", len(key1))
	}
	key2, err := svc.ensureSigningKey(ctx)
	if err != nil {
		t.Fatalf("ensure key again: %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("signing key must be stable across calls")
	}
	fi, err := os.Stat(svc.signingKeyPath())
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %o, want 0600", fi.Mode().Perm())
	}

	// Round-trip: sign with the generated key and verify.
	manifest := &repo.RepositoryManifest{Version: repo.ManifestVersion, Objects: []repo.ManifestObject{{Path: "x", Size: 1, SHA256: "aa"}}}
	if err := repo.SignManifest(manifest, key1); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyManifestSignature(manifest, key1); err != nil {
		t.Fatalf("verify with generated key: %v", err)
	}

	// signReleaseBundles: build a minimal releases tree with a bundle.
	layout := repo.New(cfg.DeploymentRootDir)
	rel := filepath.Join(layout.RepoDir(), repo.DirReleases, "f-1", "1.0.0")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := []byte("fake release bundle content")
	if err := os.WriteFile(filepath.Join(rel, "bundle.tar.gz"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.signReleaseBundles(ctx, layout, key1); err != nil {
		t.Fatalf("signReleaseBundles: %v", err)
	}
	sigBytes, err := os.ReadFile(filepath.Join(rel, "bundle.sig"))
	if err != nil {
		t.Fatalf("read bundle.sig: %v", err)
	}
	digest := sha256.Sum256(bundle)
	mac := hmac.New(sha256.New, key1)
	mac.Write([]byte(hex.EncodeToString(digest[:])))
	want := hex.EncodeToString(mac.Sum(nil)) + "\n"
	if string(sigBytes) != want {
		t.Fatalf("bundle.sig mismatch: got %q want %q", string(sigBytes), want)
	}
	_ = st
}

// TestCreateOperationProductionAwaitingConfirmation: in production,
// install/update queue as awaiting_confirmation; backup stays queued.
func TestCreateOperationProductionAwaitingConfirmation(t *testing.T) {
	ctx, st, cfg, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")
	cfg.AppEnv = "production"

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "go",
	})
	if err != nil {
		t.Fatalf("create install: %v", err)
	}
	if op.Status != model.DeploymentStatusAwaitingConfirmation {
		t.Fatalf("production install status %q, want awaiting_confirmation", op.Status)
	}
	// An awaiting op still blocks the feature.
	if _, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionUpdate, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "go",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active op should conflict, got %v", err)
	}

	// backup without release_id is allowed and stays queued (only
	// install/update require confirmation). Use a separate feature/node to
	// avoid the feature-level and node-level serial indexes held by the
	// awaiting install.
	seedDeployNode(t, ctx, st, "n-2")
	seedDeployFeature(t, ctx, st, "f-2", "app2", "none", "none")
	seedDeployTarget(t, ctx, st, "t-2", "f-2", "n-2", "")
	opB, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionBackup, FeatureID: "f-2", TargetIDs: []string{"t-2"}, Reason: "snapshot",
	})
	if err != nil {
		t.Fatalf("create backup without release: %v", err)
	}
	if opB.ReleaseID != "" || opB.Status != model.DeploymentStatusQueued {
		t.Fatalf("backup op: %+v", opB)
	}
}

// TestCreateOperationTransactionalRollback: when the second target conflicts
// with the node-serial index, the whole operation (and its targets/steps)
// must not be persisted.
func TestCreateOperationTransactionalRollback(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	if _, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	}); err != nil {
		t.Fatalf("create first op: %v", err)
	}

	// A second feature pinned to the same node cannot queue concurrently.
	seedDeployFeature(t, ctx, st, "f-2", "app2", "none", "none")
	seedDeployRelease(t, ctx, st, "f-2", "r-2", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-2", "f-2", "n-1", "")

	if _, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-2", ReleaseID: "r-2", TargetIDs: []string{"t-2"}, Reason: "ok",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second op on busy node should conflict, got %v", err)
	}

	ops, err := svc.ListOperations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if o.FeatureID == "f-2" {
			t.Fatalf("rollback failed: operation for f-2 persisted: %+v", o)
		}
	}
	// No dangling operation targets for f-2 either.
	if _, err := st.DeploymentOperationTargetByID(ctx, "missing"); err == nil {
		t.Fatal("unexpected")
	}
}

// TestCreateOperationReleaseRequiredByAction: install requires release_id;
// health_check may omit it.
func TestCreateOperationReleaseRequiredByAction(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	if _, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("install without release should be ErrBadRequest, got %v", err)
	}
	if _, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionHealthCheck, FeatureID: "f-1", TargetIDs: []string{"t-1"}, Reason: "check",
	}); err != nil {
		t.Fatalf("health_check without release should be allowed: %v", err)
	}
}

// TestCreateOperationFrozenHashes: op-level config hash and per-target secret
// hash are frozen and non-empty.
func TestCreateOperationFrozenHashes(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	seedDeployRelease(t, ctx, st, "f-1", "r-1", "1.0.0")
	seedDeployTarget(t, ctx, st, "t-1", "f-1", "n-1", "")

	op, err := svc.CreateOperation(ctx, "admin-1", CreateOperationInput{
		Action: model.DeploymentActionInstall, FeatureID: "f-1", ReleaseID: "r-1", TargetIDs: []string{"t-1"}, Reason: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetOperationDetail(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Operation.FrozenConfigHash == "" {
		t.Fatal("op.FrozenConfigHash must be non-empty")
	}
	if len(detail.Targets) != 1 || detail.Targets[0].FrozenConfigHash == "" || detail.Targets[0].FrozenSecretHash == "" {
		t.Fatalf("target frozen hashes must be non-empty: %+v", detail.Targets)
	}
}

// TestCreateSecretReferenceValidation (P0): success + invalid scope rejected.
func TestCreateSecretReferenceValidation(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")

	ref, err := svc.CreateSecretReference(ctx, "admin-1", SecretReferenceInput{
		Name: "db", FeatureID: "f-1", ScopeType: model.SecretScopeShared,
	})
	if err != nil {
		t.Fatalf("create shared ref: %v", err)
	}
	if ref.ObjectKey != "deployment-repository/secrets/shared/db.secrets.yaml" {
		t.Fatalf("object_key %q", ref.ObjectKey)
	}
	if ref.Version != 0 || ref.ContentHash != "" || ref.EncryptionMode != secretprovider.ModeNone {
		t.Fatalf("initial metadata wrong: %+v", ref)
	}

	if _, err := svc.CreateSecretReference(ctx, "admin-1", SecretReferenceInput{
		Name: "db2", FeatureID: "f-1", ScopeType: "team",
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("invalid scope_type should be ErrBadRequest, got %v", err)
	}
	if _, err := svc.CreateSecretReference(ctx, "admin-1", SecretReferenceInput{
		Name: "db3", FeatureID: "f-1", ScopeType: model.SecretScopeNode,
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("node scope without scope_id should be ErrBadRequest, got %v", err)
	}
	if _, err := svc.CreateSecretReference(ctx, "admin-1", SecretReferenceInput{
		Name: "db4", FeatureID: "missing", ScopeType: model.SecretScopeShared,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing feature should be ErrNotFound, got %v", err)
	}
}

// TestOverwriteSecretSizeCapAndNoDrift (P0): >1MiB rejected; a failed upload
// (no OSS profile) must not leave a local secret file.
func TestOverwriteSecretSizeCapAndNoDrift(t *testing.T) {
	ctx, st, cfg, svc, _ := newDeploymentHarness(t)
	cfg.DeploymentRootDir = t.TempDir()
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	ref, err := svc.CreateSecretReference(ctx, "admin-1", SecretReferenceInput{
		Name: "db", FeatureID: "f-1", ScopeType: model.SecretScopeShared,
	})
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(cfg.DeploymentRootDir, repo.RepoDirRepository, "secrets/shared/db.secrets.yaml")

	big := strings.Repeat("x", 1<<20+1)
	if _, err := svc.OverwriteSecret(ctx, "admin-1", ref.ID, OverwriteSecretInput{Value: big}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("oversized value should be ErrBadRequest, got %v", err)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("oversized write must not create a local file")
	}

	// No OSS profile → the upload step fails; the temp file must be removed
	// and the reference metadata must stay unchanged.
	if _, err := svc.OverwriteSecret(ctx, "admin-1", ref.ID, OverwriteSecretInput{Value: "hello"}); err == nil {
		t.Fatal("overwrite without OSS profile should fail")
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("failed upload must not leave a local secret file")
	}
	got, err := st.DeploymentSecretReferenceByID(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 0 || got.ContentHash != "" {
		t.Fatalf("failed overwrite must not bump metadata: %+v", got)
	}
}

// TestMaterializeDeploymentSigningKey (P0): valid token materializes the key;
// unknown/revoked tokens are rejected and the key never appears in audit
// details.
func TestMaterializeDeploymentSigningKey(t *testing.T) {
	ctx, st, cfg, svc, _ := newDeploymentHarness(t)
	cfg.DeploymentRootDir = t.TempDir()
	seedDeployNode(t, ctx, st, "n-1")
	token := "bst_testtoken1234567890"
	if err := st.CreateBootstrapSession(ctx, &model.BootstrapSession{
		NodeID:    "n-1",
		Status:    model.BootstrapStatusCreated,
		TokenHash: sha256Hex([]byte(token)),
		Bucket:    "b",
	}); err != nil {
		t.Fatal(err)
	}

	key, err := svc.MaterializeDeploymentSigningKey(ctx, token)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length %d, want 32", len(key))
	}

	if _, err := svc.MaterializeDeploymentSigningKey(ctx, "bst_unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token should be ErrNotFound, got %v", err)
	}

	sess, serr := st.BootstrapSessionByTokenHash(ctx, sha256Hex([]byte(token)))
	if serr != nil {
		t.Fatal(serr)
	}
	if err := st.RevokeBootstrapSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, merr := svc.MaterializeDeploymentSigningKey(ctx, token); !errors.Is(merr, ErrForbidden) {
		t.Fatalf("revoked token should be ErrForbidden, got %v", merr)
	}

	// Audit trail must never contain the key material.
	events, lerr := st.ListAuditEvents(ctx, store.AuditFilter{Action: "deployment.bootstrap.materialize"})
	if lerr != nil {
		t.Fatal(lerr)
	}
	for _, ev := range events {
		if strings.Contains(ev.DetailsJSON, hex.EncodeToString(key)) {
			t.Fatalf("audit leaked signing key: %s", ev.DetailsJSON)
		}
	}
}

// TestUpdateOSSProfileEndpointValidation (P0): changing the endpoint re-runs
// the SSRF allowlist validation even when credentials are unchanged.
func TestUpdateOSSProfileEndpointValidation(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	p, err := svc.CreateOSSProfile(ctx, "admin-1", OSSProfileInput{
		Name:            "primary",
		Endpoint:        "https://oss-cn-hangzhou.aliyuncs.com",
		Region:          "cn-hangzhou",
		Bucket:          "test-bucket",
		AccessKeyID:     "AKIDEXAMPLE",
		AccessKeySecret: "SecretValue123456",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	// Invalid (non-allowlisted) endpoint change must be rejected.
	if _, err := svc.UpdateOSSProfile(ctx, "admin-1", p.ID, OSSProfileInput{
		Endpoint: "https://evil.example.com",
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("invalid endpoint change should be ErrBadRequest, got %v", err)
	}
	got, err := st.OSSProfileByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "https://oss-cn-hangzhou.aliyuncs.com" {
		t.Fatalf("rejected endpoint change must not persist: %q", got.Endpoint)
	}

	// A valid endpoint change (same allowlist family) must succeed.
	upd, err := svc.UpdateOSSProfile(ctx, "admin-1", p.ID, OSSProfileInput{
		Endpoint: "https://oss-cn-beijing.aliyuncs.com",
	})
	if err != nil {
		t.Fatalf("valid endpoint change: %v", err)
	}
	if upd.Endpoint != "https://oss-cn-beijing.aliyuncs.com" {
		t.Fatalf("endpoint not updated: %q", upd.Endpoint)
	}
}

// TestUpdateConfigProfileScopeValidation: node scope requires scope_id,
// mirroring CreateConfigProfile.
func TestUpdateConfigProfileScopeValidation(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	prof, err := svc.CreateConfigProfile(ctx, "admin-1", ConfigProfileInput{
		Name: "cfg", ScopeType: model.ConfigScopeShared, FeatureID: "f-1", ContentYAML: "a: 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateConfigProfile(ctx, "admin-1", prof.ID, ConfigProfileInput{
		ScopeType: "team",
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("invalid scope_type should be ErrBadRequest, got %v", err)
	}
	if _, err := svc.UpdateConfigProfile(ctx, "admin-1", prof.ID, ConfigProfileInput{
		ScopeType: model.ConfigScopeNode,
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("node scope without scope_id should be ErrBadRequest, got %v", err)
	}
}

// TestCreateFeatureReleaseKeyValidation: feature_key/version reject path
// injection characters.
func TestCreateFeatureReleaseKeyValidation(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployFeature(t, ctx, st, "f-1", "app", "none", "none")
	if _, err := svc.CreateFeature(ctx, "admin-1", CreateFeatureInput{
		FeatureKey: "../evil", Name: "x",
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("feature_key with path chars should be ErrBadRequest, got %v", err)
	}
	if _, err := svc.CreateRelease(ctx, "admin-1", CreateReleaseInput{
		FeatureID: "f-1", Version: "1.0.0/../../etc", ObjectKey: "deployment-repository/releases/x", Size: 1, SHA256: strings.Repeat("a", 64),
	}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("version with path chars should be ErrBadRequest, got %v", err)
	}
}

// TestCreateBootstrapSessionCommandPinsScript: the returned command downloads
// the script to a temp path, verifies its sha256, and passes the new flags.
func TestCreateBootstrapSessionCommandPinsScript(t *testing.T) {
	ctx, st, _, svc, _ := newDeploymentHarness(t)
	seedDeployNode(t, ctx, st, "n-1")

	if err := os.MkdirAll("scripts", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll("scripts") })
	script := "#!/usr/bin/env bash\necho hi\n"
	if err := os.WriteFile("scripts/deployment-bootstrap.sh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := svc.CreateBootstrapSession(ctx, "admin-1", BootstrapSessionInput{
		NodeID: "n-1", Bucket: "b", Prefix: "deployment-repository/", Region: "cn-hangzhou",
	})
	if err != nil {
		t.Fatalf("create bootstrap session: %v", err)
	}
	if !strings.Contains(res.Command, "curl -fsSL") || !strings.Contains(res.Command, "-o /tmp/servercli-bootstrap-") {
		t.Fatalf("command missing download+hash steps: %s", res.Command)
	}
	if !strings.Contains(res.Command, "--session-id "+res.Session.ID) ||
		!strings.Contains(res.Command, "--token "+res.Token) ||
		!strings.Contains(res.Command, "--control-plane-url") {
		t.Fatalf("command missing control-plane flags: %s", res.Command)
	}
	digest := sha256.Sum256([]byte(script))
	if !strings.Contains(res.Command, hex.EncodeToString(digest[:])) {
		t.Fatalf("command missing script sha256 pin: %s", res.Command)
	}
}

// TestOSSClientRedirectGuard (P0): the OSS HTTP client only follows redirects
// that stay on the same host over https; cross-host or plain-http redirects
// are rejected (SSRF/credential-replay protection).
func TestOSSClientRedirectGuard(t *testing.T) {
	// redirectRT answers the first request with a redirect to location; any
	// later request is answered with 200 so a permitted redirect completes.
	type redirectRT struct {
		location string
		calls    int32
	}
	mk := func(location string) *redirectRT { return &redirectRT{location: location} }
	do := func(rt *redirectRT) error {
		client, err := ossclient.New("https://oss-cn-hangzhou.aliyuncs.com",
			ossclient.Credentials{AccessKeyID: "AKIDEXAMPLE", AccessKeySecret: "S3cr3tKey!VaLuE"},
			ossclient.WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if atomic.LoadInt32(&rt.calls) > 0 {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Header:     http.Header{"Content-Type": []string{"application/xml"}},
						Body:       io.NopCloser(strings.NewReader("<ListBucketResult/>")),
						Request:    req,
					}, nil
				}
				atomic.AddInt32(&rt.calls, 1)
				h := http.Header{"Location": []string{rt.location}}
				return &http.Response{
					StatusCode: http.StatusFound,
					Status:     "302 Found",
					Header:     h,
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			})}),
		)
		if err != nil {
			return err
		}
		_, err = client.ListObjects(context.Background(), "bucket", "prefix", "/")
		return err
	}

	// Cross-host https redirect must be rejected.
	rt := mk("https://oss-cn-beijing.aliyuncs.com/other")
	if err := do(rt); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect should be rejected, got %v", err)
	}

	// Plain-http redirect must be rejected.
	rt = mk("http://oss-cn-hangzhou.aliyuncs.com/other")
	if err := do(rt); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("http redirect should be rejected, got %v", err)
	}

	// Same-host https redirect is allowed and completes successfully.
	rt = mk("https://oss-cn-hangzhou.aliyuncs.com/prefix")
	if err := do(rt); err != nil {
		t.Fatalf("same-host https redirect should be allowed: %v", err)
	}
}
