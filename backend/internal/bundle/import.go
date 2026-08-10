package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
	"servercli/internal/secretstore"
)

// ImportOptions configures one bundle import.
type ImportOptions struct {
	Environment      string // must match manifest and inventory environment
	NodeName         string // must match manifest target_node (if set) and inventory node name
	BundleURL        string // https:// or file:// URL of the bundle envelope
	AgeKeyFile       string // X25519 age identity (PEM or AGE-SECRET-KEY text)
	PublicKeyFile    string // release Ed25519 public key PEM
	AllowDevReplay   bool   // permit low-version replay, only in dev/test
	BootstrapVersion string // currently installed bootstrap version
	InventoryPath    string // default /etc/servercli/private/cluster.yaml
	SecretsPath      string // default /var/lib/servercli/bootstrap/secrets.enc
	MasterKeyPath    string // default /etc/servercli/keys/master.key
	RunDir           string // default /run/servercli/bootstrap (tmpfs; reserved for plaintext spooling)
}

// ImportResult is the structured outcome of a bundle import. It carries no
// secret material.
type ImportResult struct {
	BundleID      string `json:"bundle_id"`
	BundleVersion string `json:"bundle_version"`
	Environment   string `json:"environment"`
	NodeName      string `json:"node_name"`
	InputDigest   string `json:"input_digest"`
}

// bundleFile is the downloaded bundle envelope: a signed Bundle Manifest plus
// the age-encrypted payload (base64 in JSON). manifest.payload_digest is the
// lowercase hex sha256 of the raw encrypted payload bytes.
type bundleFile struct {
	Manifest bootstrap.BundleManifest `json:"manifest"`
	Payload  []byte                   `json:"payload"`
}

// bundlePayload is the age-decrypted plaintext of a bundle.
type bundlePayload struct {
	Inventory string            `json:"inventory"` // cluster.yaml YAML text
	Secrets   map[string]string `json:"secrets"`
}

// ImportBundle downloads a bundle, verifies its signed manifest, decrypts the
// age payload, validates the inventory, writes the inventory to
// opts.InventoryPath (0700 dir / 0600 file, atomic + fsync) and stores each
// secret (after secretstore.SanitizeName) in the Bootstrap Secret Store. The
// store is fully persisted before ImportBundle returns.
//
// If store is nil, the store is opened (or created) from opts.SecretsPath and
// opts.MasterKeyPath, which default to bootstrap.FileSecretsEnc and
// bootstrap.FileMasterKey.
func ImportBundle(ctx context.Context, opts ImportOptions, store *secretstore.Store, log *slog.Logger) (*ImportResult, error) {
	logger := discardLogger(log)
	if err := applyDefaults(&opts); err != nil {
		return nil, err
	}
	if err := validateImportOptions(opts); err != nil {
		return nil, err
	}

	// 1. Download bundle envelope.
	raw, err := fetchBundle(ctx, opts.BundleURL)
	if err != nil {
		return nil, fmt.Errorf("download bundle: %w", err)
	}
	var env bundleFile
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse bundle envelope: %w", err)
	}

	// The signed manifest must pin the payload before decryption.
	if env.Manifest.PayloadDigest == "" {
		return nil, fmt.Errorf("bundle manifest: missing payload_digest")
	}
	if got := sha256Hex(env.Payload); !equalDigest(got, env.Manifest.PayloadDigest) {
		return nil, fmt.Errorf("bundle payload digest mismatch (manifest %s, got %s)", env.Manifest.PayloadDigest, got)
	}

	pubPEM, err := os.ReadFile(opts.PublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read public key %s: %w", opts.PublicKeyFile, err)
	}
	ageKey, err := os.ReadFile(opts.AgeKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read age key %s: %w", opts.AgeKeyFile, err)
	}

	// 2. Verify the signed manifest.
	if err := VerifyBundleManifest(&env.Manifest, pubPEM, opts.BootstrapVersion, opts.Environment, opts.AllowDevReplay); err != nil {
		return nil, err
	}
	if env.Manifest.TargetNode != "" && env.Manifest.TargetNode != opts.NodeName {
		return nil, fmt.Errorf("bundle target_node %q does not match local node %q", env.Manifest.TargetNode, opts.NodeName)
	}

	// 3. Decrypt the payload.
	plain, err := DecryptBundle(env.Payload, ageKey)
	if err != nil {
		return nil, err
	}
	var payload bundlePayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, fmt.Errorf("parse decrypted bundle payload: %w", err)
	}
	if payload.Inventory == "" {
		return nil, fmt.Errorf("bundle payload: empty inventory")
	}

	// 4. Parse and validate the inventory.
	var inv bootstrap.Inventory
	if err := yaml.Unmarshal([]byte(payload.Inventory), &inv); err != nil {
		return nil, fmt.Errorf("parse inventory YAML: %w", err)
	}
	if inv.Environment != opts.Environment {
		return nil, fmt.Errorf("inventory environment mismatch (inventory %q, local %q)", inv.Environment, opts.Environment)
	}
	if inv.Node.Name != opts.NodeName {
		return nil, fmt.Errorf("inventory node mismatch (inventory %q, local %q)", inv.Node.Name, opts.NodeName)
	}

	// 5. Write the inventory atomically (0700 dir / 0600 file, fsync).
	if err := writeFileAtomic(opts.InventoryPath, []byte(payload.Inventory), 0o700, 0o600); err != nil {
		return nil, fmt.Errorf("write inventory: %w", err)
	}
	logger.Info("bundle inventory written", "path", opts.InventoryPath)

	// 6. Sanitize and persist secrets; the store is atomically written before
	//    we return success.
	keys := make([]string, 0, len(payload.Secrets))
	for k := range payload.Secrets {
		if err := secretstore.SanitizeName(k); err != nil {
			return nil, fmt.Errorf("bundle secret name: %w", err)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	st := store
	if st == nil {
		st, err = openBootstrapStore(opts)
		if err != nil {
			return nil, err
		}
	}
	for _, k := range keys {
		if err := st.Set(k, payload.Secrets[k]); err != nil {
			return nil, fmt.Errorf("write secret store %s: %w", k, err)
		}
	}
	logger.Info("bundle secrets stored", "count", len(keys))

	// 7. Input digest: canonical JSON of the inventory plus bundle secrets,
	//    computed with modman.ComputeInputDigest. No secret value is logged.
	canonInv, err := json.Marshal(&inv)
	if err != nil {
		return nil, fmt.Errorf("canonicalize inventory: %w", err)
	}
	digest := modman.ComputeInputDigest(map[string]string{"inventory": string(canonInv)}, payload.Secrets)

	return &ImportResult{
		BundleID:      env.Manifest.BundleID,
		BundleVersion: env.Manifest.BundleVersion,
		Environment:   opts.Environment,
		NodeName:      opts.NodeName,
		InputDigest:   digest,
	}, nil
}

func applyDefaults(opts *ImportOptions) error {
	if opts.InventoryPath == "" {
		opts.InventoryPath = bootstrap.FileClusterYAML
	}
	if opts.SecretsPath == "" {
		opts.SecretsPath = bootstrap.FileSecretsEnc
	}
	if opts.MasterKeyPath == "" {
		opts.MasterKeyPath = bootstrap.FileMasterKey
	}
	if opts.RunDir == "" {
		opts.RunDir = bootstrap.DirRunBootstrap
	}
	return nil
}

func validateImportOptions(opts ImportOptions) error {
	switch {
	case opts.Environment == "":
		return fmt.Errorf("bundle import: environment is required")
	case opts.NodeName == "":
		return fmt.Errorf("bundle import: node_name is required")
	case opts.BundleURL == "":
		return fmt.Errorf("bundle import: bundle_url is required")
	case opts.AgeKeyFile == "":
		return fmt.Errorf("bundle import: age_key_file is required")
	case opts.PublicKeyFile == "":
		return fmt.Errorf("bundle import: public_key_file is required")
	}
	return nil
}

func openBootstrapStore(opts ImportOptions) (*secretstore.Store, error) {
	if opts.MasterKeyPath == "" || opts.SecretsPath == "" {
		return nil, fmt.Errorf("bundle import: master_key_path and secrets_path are required when no store is provided")
	}
	mk, err := secretstore.LoadOrCreateMasterKey(opts.MasterKeyPath)
	if err != nil {
		return nil, fmt.Errorf("bundle import: %w", err)
	}
	st, err := secretstore.OpenBootstrapStore(opts.SecretsPath, mk)
	if err != nil {
		return nil, fmt.Errorf("bundle import: %w", err)
	}
	return st, nil
}

// fetchBundle reads a bundle envelope from an https/http URL or a file://
// path (used by tests and local imports).
func fetchBundle(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse bundle URL: %w", err)
	}
	switch u.Scheme {
	case "file":
		return readFileLimited(u.Path, maxArtifactBytes)
	case "http", "https":
		return httpGet(ctx, rawURL, 0, maxArtifactBytes)
	default:
		return nil, fmt.Errorf("unsupported bundle URL scheme %q (want https or file)", u.Scheme)
	}
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fsyncs it, chmods to fileMode, renames into place and fsyncs the directory.
// The parent directory is created (and tightened) to dirMode. Existing
// symlinks at path are refused.
func writeFileAtomic(path string, data []byte, dirMode, fileMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: refusing symlink", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".inventory-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
