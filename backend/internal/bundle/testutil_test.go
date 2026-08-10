package bundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"filippo.io/age"

	"servercli/internal/bootstrap"
	"servercli/internal/sigverify"
)

// testEd25519Key generates a throwaway Ed25519 key pair and returns the
// private key plus the PKIX "PUBLIC KEY" PEM. No real keys are used.
func testEd25519Key(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return priv, pubPEM
}

// testAgeIdentity generates a throwaway X25519 age identity.
func testAgeIdentity(t *testing.T) (*age.X25519Identity, *age.X25519Recipient) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id, id.Recipient()
}

func ageEncrypt(t *testing.T, recipient *age.X25519Recipient, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signManifest signs the canonical JSON of m and stores the base64 signature
// into its Signature field.
func signManifest[T any](t *testing.T, priv ed25519.PrivateKey, m *T) {
	t.Helper()
	canon, err := CanonicalManifestBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	rv := reflect.ValueOf(m).Elem()
	rv.FieldByName("Signature").SetString(sigverify.SignEd25519(priv, canon))
}

func fileURL(path string) string { return "file://" + path }

// publicKeyPEMFor derives the PKIX "PUBLIC KEY" PEM from a private key.
func publicKeyPEMFor(priv ed25519.PrivateKey) ([]byte, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an ed25519 key")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func writeReleaseManifest(t *testing.T, dir string, priv ed25519.PrivateKey, m *bootstrap.ReleaseManifest) {
	t.Helper()
	signManifest(t, priv, m)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ReleaseManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testReleaseManifest(version string, artifacts ...bootstrap.Artifact) *bootstrap.ReleaseManifest {
	return &bootstrap.ReleaseManifest{
		SchemaVersion:  "1",
		ReleaseVersion: version,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		SigningKeyID:   "test-key",
		Artifacts:      artifacts,
		SchemaCompat: bootstrap.SchemaCompat{
			MinSchemaVersion: "1",
			MaxSchemaVersion: "1",
			Reversible:       true,
		},
	}
}

// writeBundleFile writes a bundle envelope JSON at path: age-encrypted
// payload (inventory YAML + secrets) wrapped with a signed Bundle Manifest.
func writeBundleFile(t *testing.T, path string, priv ed25519.PrivateKey, rec *age.X25519Recipient,
	manifest *bootstrap.BundleManifest, inventory string, secrets map[string]string) {
	t.Helper()
	plain, err := json.Marshal(bundlePayload{Inventory: inventory, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	cipher := ageEncrypt(t, rec, plain)
	manifest.PayloadDigest = sha256Hex(cipher)
	signManifest(t, priv, manifest)
	env := bundleFile{Manifest: *manifest, Payload: cipher}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testBundleManifest(env, node, bundleID, bundleVersion, minBootstrap string) *bootstrap.BundleManifest {
	exp := time.Now().UTC().Add(24 * time.Hour)
	return &bootstrap.BundleManifest{
		SchemaVersion:           "1",
		BundleID:                bundleID,
		BundleVersion:           bundleVersion,
		Environment:             env,
		TargetNode:              node,
		TargetRole:              "control-plane",
		CreatedAt:               time.Now().UTC().Truncate(time.Second),
		MinimumBootstrapVersion: minBootstrap,
		PayloadDigest:           "",
		ExpiresAt:               &exp,
		SigningKeyID:            "test-key",
	}
}

const testInventoryYAML = `schema_version: "1"
environment: prod
node:
  name: node-a
  role: control-plane
services:
  postgres:
    manager: servercli
    owner: servercli
    phase: foundation-core
network:
  domain: example.internal
  public_ip: 203.0.113.10
backup:
  enabled: false
update:
  auto_apply: false
restore:
  require_explicit_id: true
secrets:
  postgres.password:
    key: postgres.password
    store: bootstrap
    source: bundle
`

func testSecrets() map[string]string {
	return map[string]string{
		"postgres.password": "s3cr3t-" + randHex(8),
		"postgres.user":     "app-" + randHex(4),
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
