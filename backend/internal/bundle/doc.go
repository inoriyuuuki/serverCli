// Package bundle implements the ServerCLI bootstrap trust chain for signed
// release manifests and encrypted bundles:
//
//   - FetchAndVerifyRelease downloads and verifies the signed Release
//     Manifest from GitHub Releases (primary) with an OSS mirror fallback;
//   - DownloadArtifact downloads one release artifact and checks its sha256
//     against the digest already covered by the signed manifest;
//   - VerifyBundleManifest verifies a Bundle Manifest signature plus
//     environment, expiry, bootstrap-version and replay checks;
//   - DecryptBundle decrypts an age-encrypted bundle payload;
//   - ImportBundle downloads, verifies, decrypts and imports a bundle
//     (inventory + secrets) into the node-local inventory file and the
//     encrypted Bootstrap Secret Store;
//   - ResumeGuard ties resume to the exact bundle_id + input digest recorded
//     when init started.
//
// The bundle file (envelope) format is JSON:
//
//	{
//	  "manifest": { ...bootstrap.BundleManifest... },
//	  "payload":  "<base64 of the age-encrypted plaintext>"
//	}
//
// where manifest.payload_digest is the lowercase hex sha256 of the raw
// encrypted payload bytes (base64-decoded). The decrypted plaintext is JSON:
//
//	{
//	  "inventory": "<cluster.yaml YAML text>",
//	  "secrets":   { "postgres.password": "..." }
//	}
//
// Canonical signing bytes for both manifest kinds are produced by
// encoding/json struct-field-order marshaling with the Signature field set to
// the empty string (see CanonicalManifestBytes). All manifests must be
// verified against that canonical form; bare sha256 is never treated as
// proof of authenticity.
//
// Secrets never appear in logs or returned errors; plaintext bundle content
// is kept in memory only and dropped after import.
package bundle
