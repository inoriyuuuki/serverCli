package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"

	"servercli/internal/bootstrap"
	"servercli/internal/sigverify"
)

// CanonicalManifestBytes returns the canonical JSON a signed manifest's
// signature covers: the manifest serialized to JSON, then re-serialized as a
// map with the "signature" key removed and keys sorted (compact, no HTML
// escaping). This is byte-identical to `jq -cS 'del(.signature)'` / Python
// `json.dumps(sort_keys=True)` so Go, the CI signer and the installer all
// verify the same message. Signing and verification both use the raw canonical
// bytes with standard Ed25519 (no separate SHA-256 pre-hash).
func CanonicalManifestBytes(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical manifest: marshal: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("canonical manifest: unmarshal: %w", err)
	}
	delete(m, "signature")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("canonical manifest: encode: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// verifyManifestSignature verifies sigB64 over the canonical JSON of m using
// the release Ed25519 public key. When pubPEM is empty (publication signing
// disabled), verification is skipped.
func verifyManifestSignature(m any, pubPEM []byte, sigB64 string) error {
	if len(pubPEM) == 0 {
		return nil
	}
	canon, err := CanonicalManifestBytes(m)
	if err != nil {
		return fmt.Errorf("canonical manifest: %w", err)
	}
	if err := sigverify.VerifyEd25519(pubPEM, canon, sigB64); err != nil {
		return fmt.Errorf("manifest signature: %w", err)
	}
	return nil
}

// LoadReleaseManifest parses a Release Manifest from JSON.
func LoadReleaseManifest(data []byte) (*bootstrap.ReleaseManifest, error) {
	var m bootstrap.ReleaseManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if err := validateReleaseManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// LoadBundleManifest parses a Bundle Manifest from JSON.
func LoadBundleManifest(data []byte) (*bootstrap.BundleManifest, error) {
	var m bootstrap.BundleManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse bundle manifest: %w", err)
	}
	if err := validateBundleManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateReleaseManifest(m *bootstrap.ReleaseManifest) error {
	if m.SchemaVersion == "" {
		return fmt.Errorf("release manifest: missing schema_version")
	}
	if m.ReleaseVersion == "" {
		return fmt.Errorf("release manifest: missing release_version")
	}
	// V1 disables publication signing: an empty signature is acceptable and
	// the trust anchor is the artifact sha256 list (verified when a release
	// public key is configured). A non-empty signature is verified when a
	// pubkey is present.
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if a.Path == "" {
			return fmt.Errorf("release manifest: artifact %d missing path", i)
		}
		if err := checkSHA256Hex(a.SHA256); err != nil {
			return fmt.Errorf("release manifest: artifact %q: %w", a.Path, err)
		}
	}
	return nil
}

func validateBundleManifest(m *bootstrap.BundleManifest) error {
	if m.SchemaVersion == "" {
		return fmt.Errorf("bundle manifest: missing schema_version")
	}
	if m.BundleID == "" {
		return fmt.Errorf("bundle manifest: missing bundle_id")
	}
	if m.BundleVersion == "" {
		return fmt.Errorf("bundle manifest: missing bundle_version")
	}
	if m.Environment == "" {
		return fmt.Errorf("bundle manifest: missing environment")
	}
	if m.Signature == "" {
		return fmt.Errorf("bundle manifest: missing signature")
	}
	return nil
}

// VerifyReleaseManifest verifies the Ed25519 signature of a release manifest
// against the release public key. When pubPEM is empty (publication signing
// disabled), verification is skipped.
func VerifyReleaseManifest(m *bootstrap.ReleaseManifest, pubPEM []byte) error {
	if m == nil {
		return fmt.Errorf("release manifest: nil")
	}
	return verifyManifestSignature(m, pubPEM, m.Signature)
}
