package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"servercli/internal/bootstrap"
	"servercli/internal/bundle"
	"servercli/internal/initstate"
	"servercli/internal/sigverify"
)

const e2eInventoryYAML = `schema_version: "1.0"
environment: test
node:
  name: n1
  role: primary
  profile: foundation
network:
  public_ip: 203.0.113.10
services: {}
backup:
  enabled: false
update:
  auto_apply: false
restore:
  require_explicit_id: true
`

func writeE2EModule(t *testing.T, modsDir, id, phase string) {
	t.Helper()
	dir := filepath.Join(modsDir, id)
	if err := os.MkdirAll(filepath.Join(dir, "operations"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id: " + id + "\nversion: 1.0.0\nphase: " + phase + "\ndelivery: env\noperations:\n  install:\n    entry: operations/install.sh\n  verify:\n    entry: operations/verify.sh\n"
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	install := "#!/bin/sh\nset -eu\n[ -n \"${SERVERCLI_CFG_ENVIRONMENT:-}\" ] || { echo 'missing ENVIRONMENT' >&2; exit 1; }\necho \"" + id + " installed env=${SERVERCLI_CFG_ENVIRONMENT}\"\nexit 0\n"
	verify := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "operations", "install.sh"), []byte(install), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "operations", "verify.sh"), []byte(verify), 0o755); err != nil {
		t.Fatal(err)
	}
}

// makeE2EBundle builds a signed + age-encrypted bundle envelope on disk and
// returns the paths to the pubkey and age key.
func makeE2EBundle(t *testing.T, dir string) (pubkeyPath, ageKeyPath, bundleURL string) {
	t.Helper()
	// Release Ed25519 key.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	pubkeyPath = filepath.Join(dir, "release.pub.pem")
	if err := os.WriteFile(pubkeyPath, pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	// Age identity.
	ageID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ageKeyPath = filepath.Join(dir, "bootstrap.agekey")
	if err := os.WriteFile(ageKeyPath, []byte(ageID.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Payload.
	payload := map[string]any{
		"inventory": e2eInventoryYAML,
		"secrets":   map[string]string{"postgres.app_password": "pw-value-123"},
	}
	plain, _ := json.Marshal(payload)
	var encBuf bytes.Buffer
	w, err := age.Encrypt(&encBuf, ageID.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Manifest.
	m := bootstrap.BundleManifest{
		SchemaVersion:           "1.0",
		BundleID:                "bundle-e2e-1",
		BundleVersion:           "1.0.0",
		Environment:             "test",
		TargetNode:              "n1",
		TargetRole:              "primary",
		CreatedAt:               time.Now().UTC(),
		MinimumBootstrapVersion: "0.0.1",
		PayloadDigest:           sha256HexBytes(encBuf.Bytes()),
	}
	canon, err := bundle.CanonicalManifestBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = sigverify.SignEd25519(priv, canon)
	env := map[string]any{
		"manifest": m,
		"payload":  base64.StdEncoding.EncodeToString(encBuf.Bytes()),
	}
	raw, _ := json.Marshal(env)
	bundlePath := filepath.Join(dir, "bundle.json")
	if err := os.WriteFile(bundlePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return pubkeyPath, ageKeyPath, "file://" + bundlePath
}

func sha256HexBytes(b []byte) string {
	h := sha256.Sum256(b)
	return strings.ToLower(strings.TrimSpace(hexEncode(h[:])))
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexDigits[c>>4]
		out[i*2+1] = hexDigits[c&0xf]
	}
	return string(out)
}

// TestInitApplyEndToEnd exercises the full database-free init chain: signed
// bundle import -> preflight -> 7 modules install/verify -> commit -> ready.
func TestInitApplyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	modsDir := filepath.Join(dir, "modules")
	phases := map[string]string{
		"v2ray": "foundation-core", "docker": "foundation-core",
		"postgres": "foundation-core", "caddy": "foundation-core",
		"control-plane": "foundation-core", "agent": "foundation-core",
		"gitea": "foundation-services",
	}
	for id, ph := range phases {
		writeE2EModule(t, modsDir, id, ph)
	}
	pubkey, ageKey, bundleURL := makeE2EBundle(t, dir)

	statePath := filepath.Join(dir, "state.json")
	var out, errb bytes.Buffer
	code := Run([]string{
		"init", "apply", "--yes",
		"--environment=test", "--node-name=n1",
		"--bundle-url=" + bundleURL,
		"--age-key-file=" + ageKey,
		"--pubkey-file=" + pubkey,
		"--modules-dir=" + modsDir,
		"--state-path=" + statePath,
		"--secrets-path=" + filepath.Join(dir, "secrets.enc"),
		"--keys-dir=" + filepath.Join(dir, "keys"),
		"--run-dir=" + filepath.Join(dir, "run"),
		"--lock-dir=" + filepath.Join(dir, "locks"),
		"--backup-dir=" + filepath.Join(dir, "backups"),
		"--inventory-path=" + filepath.Join(dir, "cluster.yaml"),
		"--ownership-path=" + filepath.Join(dir, "ownership.json"),
		"--output=json",
	}, &out, &errb, VersionInfo{Version: "0.1.0-test", Build: "test", Commit: "abc"})

	if code != 0 {
		t.Fatalf("init apply exit=%d\nstdout=%s\nstderr=%s", code, out.String(), errb.String())
	}
	combined := out.String() + errb.String()
	if strings.Contains(combined, "pw-value-123") {
		t.Fatalf("secret leaked into output: %s", combined)
	}
	// State must be ready with all 7 steps succeeded.
	state, err := initstate.OpenReadOnly(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Overall != "ready" {
		t.Fatalf("overall = %q, want ready", state.Overall)
	}
	for _, id := range []string{"v2ray", "docker", "postgres", "caddy", "control-plane", "agent", "gitea"} {
		st := state.Step(id)
		if st == nil || st.Status != initstate.StepSucceeded {
			t.Fatalf("module %s not succeeded: %+v", id, st)
		}
	}
	// Ownership records must be persisted (owner=servercli) so ops/repair
	// gates are authoritative across processes.
	owRaw, oerr := os.ReadFile(filepath.Join(dir, "ownership.json"))
	if oerr != nil {
		t.Fatalf("ownership file missing: %v", oerr)
	}
	if !strings.Contains(string(owRaw), `"owner": "servercli"`) {
		t.Fatalf("ownership not persisted with servercli owner: %s", owRaw)
	}
}
