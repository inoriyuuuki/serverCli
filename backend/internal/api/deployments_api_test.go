package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"servercli/internal/config"
	"servercli/internal/model"
	"servercli/internal/store"
)

// TestDeploymentRoutesUnauthenticated (验收 1): every read path is behind
// adminOrToken, so an unauthenticated request is rejected with 401.
func TestDeploymentRoutesUnauthenticated(t *testing.T) {
	env := setupAPI(t)
	status, out := env.serve("GET", "/api/v1/deployments/features", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated features list should be 401, got %d: %s", status, out)
	}
	status, out = env.serve("GET", "/api/v1/deployments/backups", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated backups list should be 401, got %d: %s", status, out)
	}
}

// TestDeploymentCreateFeatureDuplicate (验收 2/3): admin can list (empty
// array), create a feature (201) and duplicate feature_key is rejected (409).
func TestDeploymentCreateFeatureDuplicate(t *testing.T) {
	env := setupAPI(t)

	// Fresh environment: empty list is an array, not null.
	status, out := env.serve("GET", "/api/v1/deployments/features", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list status %d: %s", status, out)
	}
	var listResp struct {
		Features []model.DeploymentFeature `json:"features"`
	}
	if err := json.Unmarshal(out, &listResp); err != nil {
		t.Fatalf("decode list %s: %v", out, err)
	}
	if listResp.Features == nil || len(listResp.Features) != 0 {
		t.Fatalf("expected empty features array, got %s", out)
	}

	body := map[string]any{
		"feature_key": "my-app",
		"name":        "My App",
		"backup_mode": "none",
	}
	status, out = env.post("/api/v1/deployments/features", body, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create feature status %d: %s", status, out)
	}
	var created struct {
		Feature model.DeploymentFeature `json:"feature"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatalf("decode created %s: %v", out, err)
	}
	if created.Feature.ID == "" || created.Feature.FeatureKey != "my-app" {
		t.Fatalf("unexpected created feature: %s", out)
	}

	// Duplicate feature_key -> 409.
	status, out = env.post("/api/v1/deployments/features", body, env.adminHeaders())
	if status != http.StatusConflict {
		t.Fatalf("duplicate feature should be 409, got %d: %s", status, out)
	}

	// List now returns exactly one.
	status, out = env.serve("GET", "/api/v1/deployments/features", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list status %d: %s", status, out)
	}
	_ = json.Unmarshal(out, &listResp)
	if len(listResp.Features) != 1 {
		t.Fatalf("expected 1 feature, got %d: %s", len(listResp.Features), out)
	}
}

// TestDeploymentOSSProfilesOmitAK (验收 4): the access key material is
// write-only; neither the create response nor the list response (nor audit
// details) may contain the plaintext AK/SK or the encrypted fields.
func TestDeploymentOSSProfilesOmitAK(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()
	const (
		ak = "LTAI5tTestAccessKeyId000"
		sk = "TestAccessKeySecretVerySecretValue"
	)
	body := map[string]any{
		"name":              "primary",
		"endpoint":          "https://oss-cn-hangzhou.aliyuncs.com",
		"region":            "cn-hangzhou",
		"bucket":            "test-bucket",
		"access_key_id":     ak,
		"access_key_secret": sk,
	}
	status, out := env.post("/api/v1/deployments/oss-profiles", body, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create oss profile status %d: %s", status, out)
	}
	assertNoDeploymentLeak(t, out, ak, sk)

	status, out = env.serve("GET", "/api/v1/deployments/oss-profiles", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list oss profiles status %d: %s", status, out)
	}
	assertNoDeploymentLeak(t, out, ak, sk)
	var listResp struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(out, &listResp); err != nil {
		t.Fatalf("decode profiles %s: %v", out, err)
	}
	if len(listResp.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d: %s", len(listResp.Profiles), out)
	}
	for _, p := range listResp.Profiles {
		for _, k := range []string{"access_key_id_enc", "access_key_secret_enc", "access_key_id", "access_key_secret"} {
			if _, ok := p[k]; ok {
				t.Fatalf("profile response leaked key %q: %s", k, out)
			}
		}
	}

	// Audit trail must not contain the credentials either.
	events, err := env.st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev.Summary, ak) || strings.Contains(ev.Summary, sk) ||
			strings.Contains(ev.DetailsJSON, ak) || strings.Contains(ev.DetailsJSON, sk) {
			t.Fatalf("audit event leaked credentials: %s / %s", ev.Summary, ev.DetailsJSON)
		}
	}
}

// TestDeploymentOverwriteSecretNoLeak (验收 5): the overwrite request body is
// never echoed in the response and never reaches audit events, even when the
// write fails (the value must not appear anywhere).
func TestDeploymentOverwriteSecretNoLeak(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()
	const secretValue = "S3CR3T-VALUE-zzz-987654321"
	status, out := env.post("/api/v1/deployments/secrets/does-not-exist/overwrite",
		map[string]any{"value": secretValue, "reason": "rotate"}, env.adminHeaders())
	if status != http.StatusNotFound {
		t.Fatalf("overwrite unknown secret should be 404, got %d: %s", status, out)
	}
	if strings.Contains(string(out), secretValue) {
		t.Fatalf("overwrite response leaked secret value: %s", out)
	}
	events, err := env.st.ListAuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev.Summary, secretValue) || strings.Contains(ev.DetailsJSON, secretValue) {
			t.Fatalf("audit event leaked overwrite value: %s / %s", ev.Summary, ev.DetailsJSON)
		}
	}
}

// TestDeploymentBootstrapSessionLifecycle (验收 6): create returns the
// one-time token plus a command containing the primary URL; the token drives
// status reports until the session is revoked, after which reports fail.
func TestDeploymentBootstrapSessionLifecycle(t *testing.T) {
	env := setupAPI(t)
	// The generated command pins scripts/deployment-bootstrap.sh by SHA-256
	// (read relative to the process working directory), so seed it in the
	// package test directory and clean it up afterwards.
	if err := os.MkdirAll("scripts", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll("scripts") })
	if err := os.WriteFile("scripts/deployment-bootstrap.sh", []byte("#!/usr/bin/env bash\n# test bootstrap script\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	createBody := map[string]any{
		"node_id": env.nodeID,
		"bucket":  "bootstrap-bucket",
		"prefix":  "deployment-repository/",
		"region":  "cn-hangzhou",
	}
	status, out := env.post("/api/v1/deployments/bootstrap-sessions", createBody, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create bootstrap session status %d: %s", status, out)
	}
	var created struct {
		Session model.BootstrapSession `json:"session"`
		Command string                 `json:"command"`
		Token   string                 `json:"token"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatalf("decode bootstrap create %s: %v", out, err)
	}
	if created.Token == "" || !strings.HasPrefix(created.Token, "bst_") {
		t.Fatalf("expected one-time token, got %s", out)
	}
	if created.Session.TokenHash != "" {
		t.Fatalf("token hash must never be returned: %s", out)
	}
	if !strings.Contains(created.Command, config.Default().PrimaryBackendURL) {
		t.Fatalf("bootstrap command missing primary URL: %s", created.Command)
	}
	if !strings.Contains(created.Command, "/deployment-bootstrap.sh") {
		t.Fatalf("bootstrap command missing script path: %s", created.Command)
	}

	// Report with the one-time token (no agent auth required).
	reportBody := map[string]any{
		"session_token": created.Token,
		"state":         "repository_syncing",
		"message":       "oss sync ok, 42 objects",
	}
	status, out = env.serve("POST", "/api/v1/agent/deployments/bootstrap/report", reportBody, nil)
	if status != http.StatusOK {
		t.Fatalf("bootstrap report status %d: %s", status, out)
	}
	var reported struct {
		Status  string                 `json:"status"`
		Session model.BootstrapSession `json:"session"`
	}
	if err := json.Unmarshal(out, &reported); err != nil {
		t.Fatalf("decode report %s: %v", out, err)
	}
	if reported.Status != "ok" || reported.Session.Status != model.BootstrapStatusRepositorySyncing {
		t.Fatalf("unexpected report response: %s", out)
	}

	// Revoke, then reports must fail with 403.
	status, out = env.post("/api/v1/deployments/bootstrap-sessions/"+created.Session.ID+"/revoke",
		map[string]any{"reason": "manual"}, env.adminHeaders())
	if status != http.StatusNoContent {
		t.Fatalf("revoke status %d: %s", status, out)
	}
	status, out = env.serve("POST", "/api/v1/agent/deployments/bootstrap/report", reportBody, nil)
	if status != http.StatusForbidden {
		t.Fatalf("report after revoke should be 403, got %d: %s", status, out)
	}

	// Unknown token -> 404.
	status, out = env.serve("POST", "/api/v1/agent/deployments/bootstrap/report",
		map[string]any{"session_token": "bst_unknown", "state": "created"}, nil)
	if status != http.StatusNotFound {
		t.Fatalf("report with unknown token should be 404, got %d: %s", status, out)
	}
}

// TestDeploymentAgentUploadAuthorizeUnauthenticated (验收 7): the agent
// upload authorization endpoint requires an agent credential + signature.
func TestDeploymentAgentUploadAuthorizeUnauthenticated(t *testing.T) {
	env := setupAPI(t)
	status, out := env.post("/api/v1/agent/deployments/upload-authorize",
		map[string]any{"operation_id": "op-1", "target_id": "tg-1", "feature_key": "f"}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("upload-authorize without agent credential should be 401, got %d: %s", status, out)
	}
}

// TestDeploymentBootstrapScriptRoute (验收 8): the public bootstrap script
// route is registered and served from the mux (not swallowed by the SPA
// fallback). In the test working directory the relative script is absent, so
// the route answers with a JSON 404 envelope rather than the frontend hint.
func TestDeploymentBootstrapScriptRoute(t *testing.T) {
	env := setupAPI(t)
	status, out := env.serve("GET", "/deployment-bootstrap.sh", nil, nil)
	switch status {
	case http.StatusOK:
		if !strings.HasPrefix(string(out), "#!/usr/bin/env bash") {
			t.Fatalf("bootstrap script should be a shell script, got: %s", out)
		}
	case http.StatusNotFound:
		if !strings.Contains(string(out), "NOT_FOUND") {
			t.Fatalf("missing script should produce JSON 404, got: %s", out)
		}
	default:
		t.Fatalf("unexpected bootstrap script status %d: %s", status, out)
	}
}

// assertNoDeploymentLeak fails when any of the secret values appears in out.
func assertNoDeploymentLeak(t *testing.T, out []byte, values ...string) {
	t.Helper()
	for _, v := range values {
		if strings.Contains(string(out), v) {
			t.Fatalf("response leaked secret value %q: %s", v, out)
		}
	}
}

// TestDeploymentCreateSecretReferenceRoute (P0): admin creates a secret
// reference (metadata only); unauthenticated access is rejected and invalid
// scope is 400.
func TestDeploymentCreateSecretReferenceRoute(t *testing.T) {
	env := setupAPI(t)

	status, out := env.post("/api/v1/deployments/secrets/references",
		map[string]any{"name": "db", "feature_id": "missing", "scope_type": "shared"}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create should be 401, got %d: %s", status, out)
	}

	// Create a feature first so the reference FK is satisfied.
	status, out = env.post("/api/v1/deployments/features",
		map[string]any{"feature_key": "app", "name": "App", "backup_mode": "none"}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create feature %d: %s", status, out)
	}
	var feat struct {
		Feature model.DeploymentFeature `json:"feature"`
	}
	_ = json.Unmarshal(out, &feat)

	status, out = env.post("/api/v1/deployments/secrets/references",
		map[string]any{"name": "db", "feature_id": feat.Feature.ID, "scope_type": "shared"}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create secret reference %d: %s", status, out)
	}
	var created struct {
		Secret model.DeploymentSecretReference `json:"secret"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if created.Secret.ObjectKey != "deployment-repository/secrets/shared/db.secrets.yaml" {
		t.Fatalf("object_key %q", created.Secret.ObjectKey)
	}
	if created.Secret.ContentHash != "" || created.Secret.Version != 0 {
		t.Fatalf("reference must start empty: %+v", created.Secret)
	}

	// Invalid scope -> 400.
	status, out = env.post("/api/v1/deployments/secrets/references",
		map[string]any{"name": "db2", "feature_id": feat.Feature.ID, "scope_type": "team"}, env.adminHeaders())
	if status != http.StatusBadRequest {
		t.Fatalf("invalid scope should be 400, got %d: %s", status, out)
	}
}

// TestDeploymentBootstrapMaterializeRoute (P0): the agent bootstrap
// materialize endpoint returns the signing key (base64) for a valid one-time
// token and rejects unknown/revoked tokens.
func TestDeploymentBootstrapMaterializeRoute(t *testing.T) {
	env := setupAPI(t)
	ctx := context.Background()
	token := "bst_apitesttoken123456"
	if err := env.st.CreateBootstrapSession(ctx, &model.BootstrapSession{
		NodeID:    env.nodeID,
		Status:    model.BootstrapStatusCreated,
		TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token))),
		Bucket:    "b",
	}); err != nil {
		t.Fatal(err)
	}

	status, out := env.serve("POST", "/api/v1/agent/deployments/bootstrap/materialize",
		map[string]any{"session_token": token}, nil)
	if status != http.StatusOK {
		t.Fatalf("materialize status %d: %s", status, out)
	}
	var resp struct {
		SigningKey string `json:"signing_key"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	key, err := base64.StdEncoding.DecodeString(resp.SigningKey)
	if err != nil || len(key) != 32 {
		t.Fatalf("signing_key must decode to 32 bytes: %v", err)
	}

	// Unknown token -> 404.
	status, out = env.serve("POST", "/api/v1/agent/deployments/bootstrap/materialize",
		map[string]any{"session_token": "bst_nope"}, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unknown token should be 404, got %d: %s", status, out)
	}
	// Empty token -> 400.
	status, out = env.serve("POST", "/api/v1/agent/deployments/bootstrap/materialize",
		map[string]any{"session_token": ""}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("empty token should be 400, got %d: %s", status, out)
	}
}
