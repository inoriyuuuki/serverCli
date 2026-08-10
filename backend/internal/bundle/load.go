package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
)

// LoadedBundle is a bundle verified and decrypted in memory. Nothing is
// written to disk; used by `init plan` / `config import plan` (diff mode).
type LoadedBundle struct {
	Manifest    *bootstrap.BundleManifest
	Inventory   *bootstrap.Inventory
	Secrets     map[string]string
	InputDigest string
}

// LoadBundle downloads a bundle envelope, verifies its signed manifest and
// payload digest, decrypts the age payload and parses the inventory without
// writing inventory, secrets or state. It performs the same signature and
// replay checks as ImportBundle so `plan` never previews unverified input.
func LoadBundle(ctx context.Context, opts ImportOptions) (*LoadedBundle, error) {
	if err := applyDefaults(&opts); err != nil {
		return nil, err
	}
	if err := validateImportOptions(opts); err != nil {
		return nil, err
	}
	raw, err := fetchBundle(ctx, opts.BundleURL)
	if err != nil {
		return nil, fmt.Errorf("download bundle: %w", err)
	}
	var env bundleFile
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse bundle envelope: %w", err)
	}
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
	if err := VerifyBundleManifest(&env.Manifest, pubPEM, opts.BootstrapVersion, opts.Environment, opts.AllowDevReplay); err != nil {
		return nil, err
	}
	if env.Manifest.TargetNode != "" && env.Manifest.TargetNode != opts.NodeName {
		return nil, fmt.Errorf("bundle target_node %q does not match local node %q", env.Manifest.TargetNode, opts.NodeName)
	}
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
	canonInv, err := json.Marshal(&inv)
	if err != nil {
		return nil, fmt.Errorf("canonicalize inventory: %w", err)
	}
	digest := modman.ComputeInputDigest(map[string]string{"inventory": string(canonInv)}, payload.Secrets)
	return &LoadedBundle{
		Manifest:    &env.Manifest,
		Inventory:   &inv,
		Secrets:     payload.Secrets,
		InputDigest: digest,
	}, nil
}
