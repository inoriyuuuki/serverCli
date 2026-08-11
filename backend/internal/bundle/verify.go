package bundle

import (
	"errors"
	"fmt"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/sigverify"
)

// Non-production environments where low-version bundle replay may be
// explicitly permitted via allowDevReplay.
var replayAllowedEnvs = map[string]bool{
	"dev":  true,
	"test": true,
}

// ErrReplayRejected is returned when a bundle is older than the currently
// bootstrapped version and replay is not permitted.
var ErrReplayRejected = errors.New("bundle: low-version replay rejected (use config import plan/apply)")

// VerifyBundleManifest verifies a Bundle Manifest and enforces the
// import-time gates:
//
//   - Ed25519 signature over the canonical JSON (Signature field blanked),
//     ONLY when a release public key (pubPEM) is provided. Publication
//     signing is disabled by default in this deployment (pubPEM empty), so
//     the signature check is skipped; the remaining gates still hold:
//   - manifest environment must equal environment;
//   - manifest must not be expired (ExpiresAt in the past);
//   - minimum_bootstrap_version must be <= currentBootstrapVersion;
//   - replay protection: a bundle whose bundle_version is lower than
//     currentBootstrapVersion is a low-version replay. Production
//     environments (anything other than "dev"/"test") always reject it;
//     allowDevReplay only relaxes the check for dev/test environments.
func VerifyBundleManifest(m *bootstrap.BundleManifest, pubPEM []byte, currentBootstrapVersion, environment string, allowDevReplay bool) error {
	if m == nil {
		return fmt.Errorf("verify bundle manifest: nil manifest")
	}
	if err := validateBundleManifest(m); err != nil {
		return err
	}
	if len(pubPEM) > 0 {
		if err := verifyManifestSignature(m, pubPEM, m.Signature); err != nil {
			return fmt.Errorf("verify bundle manifest: %w", err)
		}
	}
	if m.Environment != environment {
		return fmt.Errorf("verify bundle manifest: environment mismatch (manifest %q, local %q)", m.Environment, environment)
	}
	if m.ExpiresAt != nil && !m.ExpiresAt.IsZero() && time.Now().After(*m.ExpiresAt) {
		return fmt.Errorf("verify bundle manifest: expired at %s", m.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if m.MinimumBootstrapVersion != "" && compareVersions(m.MinimumBootstrapVersion, currentBootstrapVersion) > 0 {
		return fmt.Errorf("verify bundle manifest: minimum_bootstrap_version %s > current bootstrap %s", m.MinimumBootstrapVersion, currentBootstrapVersion)
	}

	// Low-version replay: bundle older than the bootstrapped version.
	if currentBootstrapVersion != "" && compareVersions(m.BundleVersion, currentBootstrapVersion) < 0 {
		prod := !replayAllowedEnvs[environment]
		if prod || !allowDevReplay {
			return fmt.Errorf("%w: bundle_version %s < current bootstrap %s (environment %q)", ErrReplayRejected, m.BundleVersion, currentBootstrapVersion, environment)
		}
	}
	return nil
}

// DecryptBundle decrypts an age-encrypted bundle payload with an X25519 age
// identity key (PEM or AGE-SECRET-KEY text, see sigverify.DecryptAge).
func DecryptBundle(payload, ageKeyPEM []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("decrypt bundle: empty payload")
	}
	if len(ageKeyPEM) == 0 {
		return nil, fmt.Errorf("decrypt bundle: empty age key")
	}
	plain, err := sigverify.DecryptAge(ageKeyPEM, payload)
	if err != nil {
		return nil, fmt.Errorf("decrypt bundle: %w", err)
	}
	return plain, nil
}
