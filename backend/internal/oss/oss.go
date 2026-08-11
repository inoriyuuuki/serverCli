// Package oss implements the OSS Provider used for Bootstrap bundles, Release
// Cache, configuration snapshots and backups.
//
// V1 constraints:
//   - OSS Bucket must be private; AK/Secret are read from a 0600 file or the
//     root-only secret store, never argv/logs/audit/API.
//   - No client-side encryption in V1 (documented security boundary).
//   - Every upload is followed by a download read-back + SHA256 verification
//     before it is reported verified.
//   - Internal/public endpoint auto-selection, timeouts and retries.
package oss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

// Provider is the OSS object-store contract used by bootstrap, release cache,
// config sync and backups. Implementations must be safe for concurrent use.
type Provider interface {
	// Put uploads data to key and returns the ETag when available.
	Put(ctx context.Context, key string, data []byte, contentType string) (etag string, err error)
	// Get downloads key. Returns ErrNotFound when the object is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// Head returns object metadata without downloading the body.
	Head(ctx context.Context, key string) (*ObjectMeta, error)
	// Exists reports whether key exists.
	Exists(ctx context.Context, key string) (bool, error)
	// Delete removes key (idempotent).
	Delete(ctx context.Context, key string) error
	// PutVerified uploads, read-backs and verifies SHA256. Returns the digest.
	PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error)
	// GetVerified downloads and verifies expectedSHA256 before returning data.
	GetVerified(ctx context.Context, key string, expectedSHA256 string) ([]byte, error)
}

// ObjectMeta is object metadata returned by Head.
type ObjectMeta struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ETag        string    `json:"etag,omitempty"`
	LastModified time.Time `json:"last_modified,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
}

// Config configures the OSS provider. AccessKeySecret must come from a 0600
// file / secret store; it is never serialized into manifests or logs.
type Config struct {
	Endpoint         string `json:"endpoint"`           // public endpoint, e.g. https://oss-cn-hangzhou.aliyuncs.com
	InternalEndpoint string `json:"internal_endpoint,omitempty"` // optional internal endpoint
	Bucket           string `json:"bucket"`
	Region           string `json:"region,omitempty"`
	AccessKeyID      string `json:"access_key_id,omitempty"`     // injected at runtime
	AccessKeySecret  string `json:"-"`                          // never serialized
	PreferInternal   bool   `json:"prefer_internal,omitempty"`  // prefer internal endpoint
	Timeout          time.Duration `json:"-"`
	Retries          int           `json:"-"`
	// UserAgent is an optional custom user-agent.
	UserAgent string `json:"-"`
}

// Normalize fills zero values with safe defaults.
func (c *Config) Normalize() {
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Retries <= 0 {
		c.Retries = 3
	}
}

// effectiveEndpoint returns the configured endpoint honoring PreferInternal.
func (c *Config) effectiveEndpoint() string {
	if c.PreferInternal && c.InternalEndpoint != "" {
		return c.InternalEndpoint
	}
	return c.Endpoint
}

// SHA256Hex returns the hex sha256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifySHA256 compares expected hex digest with the actual digest of data.
func VerifySHA256(data []byte, expectedHex string) error {
	if expectedHex == "" {
		return errors.New("oss: expected sha256 is empty")
	}
	actual := SHA256Hex(data)
	if !equalFoldHex(actual, expectedHex) {
		return errors.New("oss: sha256 mismatch")
	}
	return nil
}

func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("oss: object not found")

// ReadAll is a small helper mirroring io.ReadAll for tests.
func ReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
