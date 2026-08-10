package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
	"servercli/internal/secretstore"
)

// buildTestBundle writes a bundle envelope into dir and returns its file URL.
func buildTestBundle(t *testing.T, dir string, manifest *bootstrap.BundleManifest, secrets map[string]string) string {
	t.Helper()
	priv, _ := testEd25519Key(t)
	id, rec := testAgeIdentity(t)
	bundlePath := filepath.Join(dir, "bundle.json")
	writeBundleFile(t, bundlePath, priv, rec, manifest, testInventoryYAML, secrets)

	// Persist the age key + pub key alongside so ImportOptions can point at them.
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.agekey"), []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	pubPEM, err := publicKeyPEMFor(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release.pub"), pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return fileURL(bundlePath)
}

func defaultImportOptions(dir, bundleURL string) ImportOptions {
	return ImportOptions{
		Environment:      "prod",
		NodeName:         "node-a",
		BundleURL:        bundleURL,
		AgeKeyFile:       filepath.Join(dir, "bootstrap.agekey"),
		PublicKeyFile:    filepath.Join(dir, "release.pub"),
		BootstrapVersion: "1.5.0",
		InventoryPath:    filepath.Join(dir, "etc/servercli/private/cluster.yaml"),
		SecretsPath:      filepath.Join(dir, "var/lib/servercli/bootstrap/secrets.enc"),
		MasterKeyPath:    filepath.Join(dir, "etc/servercli/keys/master.key"),
		RunDir:           filepath.Join(dir, "run/servercli/bootstrap"),
	}
}

func TestImportBundleFileURL(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	secrets := testSecrets()
	bundleURL := buildTestBundle(t, dir, manifest, secrets)
	opts := defaultImportOptions(dir, bundleURL)

	res, err := ImportBundle(context.Background(), opts, nil, nil)
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if res.BundleID != "bundle-abc" || res.BundleVersion != "1.5.0" ||
		res.Environment != "prod" || res.NodeName != "node-a" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Inventory file written with the original YAML, 0600 file in 0700 dir.
	invRaw, err := os.ReadFile(opts.InventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(invRaw) != testInventoryYAML {
		t.Fatalf("inventory content mismatch:\n%s", invRaw)
	}
	fi, err := os.Stat(opts.InventoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("inventory mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(opts.InventoryPath))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("inventory dir mode = %o, want 700", di.Mode().Perm())
	}

	// Secrets persisted in the encrypted store; values never appear in result.
	mk, err := secretstore.LoadOrCreateMasterKey(opts.MasterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := secretstore.OpenBootstrapStore(opts.SecretsPath, mk)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("store keys = %v, want 2", keys)
	}
	if v, ok := st.Get("postgres.password"); !ok || v == "" {
		t.Fatal("postgres.password not stored")
	}

	// Input digest is the canonical JSON of the inventory + secrets.
	wantDigest := modman.ComputeInputDigest(map[string]string{"inventory": inventoryCanonical(t, testInventoryYAML)}, secrets)
	if res.InputDigest != wantDigest {
		t.Fatalf("input digest = %s, want %s", res.InputDigest, wantDigest)
	}
}

func TestImportBundlePreOpenedStore(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	bundleURL := buildTestBundle(t, dir, manifest, testSecrets())
	opts := defaultImportOptions(dir, bundleURL)

	mk, err := secretstore.LoadOrCreateMasterKey(opts.MasterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := secretstore.OpenBootstrapStore(opts.SecretsPath, mk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(context.Background(), opts, st, nil); err != nil {
		t.Fatalf("ImportBundle with pre-opened store: %v", err)
	}
	if v, ok := st.Get("postgres.user"); !ok || v == "" {
		t.Fatal("postgres.user not stored via pre-opened store")
	}
}

func TestImportBundlePayloadDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	bundleURL := buildTestBundle(t, dir, manifest, testSecrets())
	opts := defaultImportOptions(dir, bundleURL)

	// Corrupt the envelope payload bytes (still valid JSON).
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env bundleFile
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.Payload = append(env.Payload, 0x00)
	corrupt, _ := json.Marshal(env)
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ImportBundle(context.Background(), opts, nil, nil); err == nil {
		t.Fatal("expected payload digest mismatch error")
	}
}

func TestImportBundleWrongAgeKey(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	bundleURL := buildTestBundle(t, dir, manifest, testSecrets())
	opts := defaultImportOptions(dir, bundleURL)

	id2, _ := testAgeIdentity(t)
	if err := os.WriteFile(opts.AgeKeyFile, []byte(id2.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBundle(context.Background(), opts, nil, nil); err == nil {
		t.Fatal("expected decrypt failure with wrong age key")
	}
}

func TestImportBundleInventoryMismatch(t *testing.T) {
	dir := t.TempDir()
	priv, _ := testEd25519Key(t)
	id, rec := testAgeIdentity(t)
	bundlePath := filepath.Join(dir, "bundle.json")

	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	// Inventory says environment=dev / node=node-b while manifest/opts say prod/node-a.
	badInv := "schema_version: \"1\"\nenvironment: dev\nnode:\n  name: node-b\n  role: control-plane\n"
	writeBundleFile(t, bundlePath, priv, rec, manifest, badInv, testSecrets())
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.agekey"), []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	pubPEM, _ := publicKeyPEMFor(priv)
	os.WriteFile(filepath.Join(dir, "release.pub"), pubPEM, 0o600)

	opts := defaultImportOptions(dir, fileURL(bundlePath))
	if _, err := ImportBundle(context.Background(), opts, nil, nil); err == nil {
		t.Fatal("expected inventory environment/node mismatch error")
	}
}

func TestImportBundleInvalidSecretName(t *testing.T) {
	dir := t.TempDir()
	priv, _ := testEd25519Key(t)
	id, rec := testAgeIdentity(t)
	bundlePath := filepath.Join(dir, "bundle.json")

	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	secrets := map[string]string{"bad;name": "x"}
	writeBundleFile(t, bundlePath, priv, rec, manifest, testInventoryYAML, secrets)
	os.WriteFile(filepath.Join(dir, "bootstrap.agekey"), []byte(id.String()), 0o600)
	pubPEM, _ := publicKeyPEMFor(priv)
	os.WriteFile(filepath.Join(dir, "release.pub"), pubPEM, 0o600)

	opts := defaultImportOptions(dir, fileURL(bundlePath))
	if _, err := ImportBundle(context.Background(), opts, nil, nil); err == nil {
		t.Fatal("expected invalid secret name error")
	}
	// Store must not contain the invalid key.
	mk, err := secretstore.LoadOrCreateMasterKey(opts.MasterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := secretstore.OpenBootstrapStore(opts.SecretsPath, mk)
	if _, ok := st.Get("bad;name"); ok {
		t.Fatal("invalid secret should not be stored")
	}
}

func TestImportBundleReplayRejectedInProduction(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.0.0", "1.0.0") // old bundle
	bundleURL := buildTestBundle(t, dir, manifest, testSecrets())
	opts := defaultImportOptions(dir, bundleURL)
	opts.AllowDevReplay = true // must not help in production
	if _, err := ImportBundle(context.Background(), opts, nil, nil); !errors.Is(err, ErrReplayRejected) {
		t.Fatalf("expected ErrReplayRejected in production import, got %v", err)
	}
}

func TestImportBundleRefusesSymlinkedInventory(t *testing.T) {
	dir := t.TempDir()
	manifest := testBundleManifest("prod", "node-a", "bundle-abc", "1.5.0", "1.0.0")
	bundleURL := buildTestBundle(t, dir, manifest, testSecrets())
	opts := defaultImportOptions(dir, bundleURL)

	target := filepath.Join(dir, "etc/servercli/private/cluster.yaml")
	os.MkdirAll(filepath.Dir(target), 0o700)
	os.WriteFile(filepath.Join(dir, "outside"), []byte("x"), 0o600)
	if err := os.Symlink(filepath.Join(dir, "outside"), target); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ImportBundle(context.Background(), opts, nil, nil); err == nil {
		t.Fatal("expected symlink refusal error")
	}
}

// inventoryCanonical returns the canonical JSON of the parsed inventory.
func inventoryCanonical(t *testing.T, yamlText string) string {
	t.Helper()
	var inv bootstrap.Inventory
	if err := yaml.Unmarshal([]byte(yamlText), &inv); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&inv)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
