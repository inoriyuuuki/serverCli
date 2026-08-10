package bundle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"servercli/internal/bootstrap"
)

// ReleaseManifestName is the file name of the signed release manifest within
// a GitHub Release asset set and the OSS mirror. Both sources must serve the
// exact same manifest bytes (same signature).
const ReleaseManifestName = "servercli-release.json"

// Source identifiers returned by FetchAndVerifyRelease.
const (
	SourceGitHub = "github"
	SourceOSS    = "oss"
)

const (
	maxManifestBytes = 4 << 20 // 4 MiB
	maxArtifactBytes = 1 << 30 // 1 GiB
	manifestTimeout  = 30 * time.Second
)

// FetchAndVerifyRelease downloads the signed Release Manifest from the GitHub
// source (primary), falling back to the OSS mirror when the primary source is
// unreachable or fails verification. Both sources verify against the same
// release Ed25519 public key; every artifact sha256 is inside the signed
// manifest, so a valid signature proves the digest list.
//
// It returns the verified manifest and the source that supplied it
// ("github" or "oss").
func FetchAndVerifyRelease(ctx context.Context, githubBaseURL, ossBaseURL string, pubPEM []byte, log *slog.Logger) (*bootstrap.ReleaseManifest, string, error) {
	logger := discardLogger(log)
	if githubBaseURL == "" || ossBaseURL == "" {
		return nil, "", fmt.Errorf("fetch release: github and oss base URLs are required")
	}
	if len(pubPEM) == 0 {
		return nil, "", fmt.Errorf("fetch release: empty public key")
	}

	m, err := fetchAndVerifyReleaseFrom(ctx, githubBaseURL, pubPEM)
	if err == nil {
		logger.Info("release manifest verified from primary source", "source", SourceGitHub)
		return m, SourceGitHub, nil
	}
	logger.Warn("primary source failed, falling back to OSS mirror", "source", SourceGitHub, "error", err)

	m, ossErr := fetchAndVerifyReleaseFrom(ctx, ossBaseURL, pubPEM)
	if ossErr != nil {
		return nil, "", fmt.Errorf("fetch release manifest: github: %v; oss: %v", err, ossErr)
	}
	logger.Info("release manifest verified from fallback source", "source", SourceOSS)
	return m, SourceOSS, nil
}

func fetchAndVerifyReleaseFrom(ctx context.Context, baseURL string, pubPEM []byte) (*bootstrap.ReleaseManifest, error) {
	raw, err := fetchFrom(ctx, baseURL, ReleaseManifestName, manifestTimeout, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	m, err := LoadReleaseManifest(raw)
	if err != nil {
		return nil, err
	}
	if err := verifyManifestSignature(m, pubPEM, m.Signature); err != nil {
		return nil, err
	}
	return m, nil
}

// DownloadArtifact downloads one release artifact from baseURL and verifies
// its bytes against the sha256 digest carried by the signed Release Manifest.
// A mismatch is an authentication failure: bare download bytes are never
// trusted without the manifest digest.
func DownloadArtifact(ctx context.Context, baseURL string, art bootstrap.Artifact, timeout time.Duration) ([]byte, error) {
	if art.Path == "" {
		return nil, fmt.Errorf("download artifact: empty path")
	}
	if err := checkSHA256Hex(art.SHA256); err != nil {
		return nil, fmt.Errorf("download artifact %q: %w", art.Path, err)
	}
	data, err := fetchFrom(ctx, baseURL, art.Path, timeout, maxArtifactBytes)
	if err != nil {
		return nil, err
	}
	if got := sha256Hex(data); !equalDigest(got, art.SHA256) {
		return nil, fmt.Errorf("download artifact %q: sha256 mismatch (manifest %s, got %s)", art.Path, art.SHA256, got)
	}
	return data, nil
}

// discardLogger returns a no-op logger when log is nil.
func discardLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return log
}
