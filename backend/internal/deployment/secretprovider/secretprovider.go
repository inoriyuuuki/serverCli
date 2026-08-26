// Package secretprovider defines the pluggable repository secret provider and
// codec abstraction. V1 secrets are stored as plaintext files under the
// private deployment repository (repository/secrets/shared/... and
// repository/secrets/nodes/<node_id>/...). Every secret read MUST go through
// RepositorySecretProvider so that storage can later migrate to AES-GCM or
// KMS envelope encryption without changing callers.
//
// Security invariants (mandatory, see doc/14_DEPLOYMENT_SECURITY.md):
//   - Secret payloads are NEVER stored in references, materials or errors and
//     are NEVER logged. Errors carry no secret content.
//   - Paths are confined to their allowed roots: ".." traversal, absolute path
//     escapes and symlink escapes are rejected.
//   - Reads are capped at maxSecretBytes (default 1 MiB).
package secretprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Encryption modes understood by the registry.
const (
	// ModeNone is the V1 plaintext mode: payloads are stored as-is. It
	// explicitly means plaintext; no pseudo-encryption or reversible encoding.
	ModeNone = "none"
	// ModeAESGCM is reserved for a future AES-GCM codec.
	ModeAESGCM = "aes-gcm"
	// ModeKMSEnvelope is reserved for a future KMS envelope codec.
	ModeKMSEnvelope = "kms-envelope"
)

// DefaultMaxSecretBytes caps how large a secret payload may be read
// (1 MiB). Oversized secrets are rejected before being loaded into memory.
const DefaultMaxSecretBytes int64 = 1 << 20

// Sentinel errors shared across the package.
var (
	ErrSecretNotFound = errors.New("secretprovider: secret not found")
	ErrPathEscape     = errors.New("secretprovider: path escapes allowed root")
	ErrSizeMismatch   = errors.New("secretprovider: secret size mismatch")
	ErrHashMismatch   = errors.New("secretprovider: secret content hash mismatch")
	ErrInsecurePerm   = errors.New("secretprovider: insecure file permissions")
	ErrTooLarge       = errors.New("secretprovider: secret exceeds maximum size")
)

// SecretReference identifies a stored secret by reference and metadata ONLY.
// It NEVER carries the secret body.
type SecretReference struct {
	ID             string
	Name           string
	FeatureID      string
	ScopeType      string
	ScopeID        string
	ObjectKey      string
	Version        string
	ContentHash    string
	EncryptionMode string
	Size           int64
	RepositoryRoot string
}

// SecretMetadata is the subset of reference metadata handed to codecs.
type SecretMetadata struct {
	Version        string
	ContentHash    string
	EncryptionMode string
	Size           int64
}

// SecretMaterial describes a resolved/materialized secret on disk. It carries
// only the path and verification metadata, NEVER the payload; callers read the
// secret from the returned path.
type SecretMaterial struct {
	Path           string
	Version        string
	ContentHash    string
	EncryptionMode string
}

// RepositorySecretProvider is the abstraction every secret consumer MUST use.
type RepositorySecretProvider interface {
	ResolveReference(ctx context.Context, ref SecretReference) (*SecretMaterial, error)
	ValidateMetadata(ctx context.Context, ref SecretReference) error
	Materialize(ctx context.Context, ref SecretReference, dstPath string) error
	Cleanup(ctx context.Context, ref SecretReference) error
}

// RepositorySecretCodec encodes/decodes/validates payloads for one encryption
// mode.
type RepositorySecretCodec interface {
	Mode() string
	Decode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error)
	Encode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error)
	Validate(ctx context.Context, raw []byte, meta SecretMetadata) error
}

// isUnsafeRelativePath reports whether p is unsafe as a repository-relative
// key: empty, absolute, "." or containing ".." traversal elements.
func isUnsafeRelativePath(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return true
	}
	if strings.Contains(p, "/../") || strings.HasPrefix(p, "../") || strings.HasSuffix(p, "/..") {
		return true
	}
	clean := filepath.Clean(p)
	if clean == "." || clean == ".." {
		return true
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}

// pathWithin reports whether p is root itself or a strict descendant of root.
func pathWithin(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveWithinRoot validates that rel is a safe relative key inside root and
// returns the absolute path. The target must already exist so symlink escapes
// can be detected via filepath.EvalSymlinks.
func resolveWithinRoot(root, rel string) (string, error) {
	if isUnsafeRelativePath(rel) {
		return "", ErrPathEscape
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	joined := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolve secret path: %w", err)
	}
	if !pathWithin(resolved, rootResolved) {
		return "", ErrPathEscape
	}
	return joined, nil
}

// resolveDstWithinRoot validates a destination path against root. dstPath may
// be absolute (must be inside root) or relative (resolved against root). The
// nearest existing ancestor is symlink-resolved and must remain inside root;
// this rejects symlink escapes even when the destination does not exist yet.
func resolveDstWithinRoot(root, dstPath string) (string, error) {
	if dstPath == "" {
		return "", ErrPathEscape
	}
	var target string
	if filepath.IsAbs(dstPath) {
		target = filepath.Clean(dstPath)
	} else if isUnsafeRelativePath(dstPath) {
		return "", ErrPathEscape
	} else {
		target = filepath.Join(root, dstPath)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve rendered root: %w", err)
	}
	if filepath.Clean(target) == filepath.Clean(root) {
		return "", ErrPathEscape
	}
	cur := target
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if !pathWithin(resolved, rootResolved) {
				return "", ErrPathEscape
			}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("cannot resolve destination path")
		}
		cur = parent
	}
	return target, nil
}

// readLimited reads at most maxBytes bytes from path. Larger files are
// rejected with ErrTooLarge. Content is only used for hashing/validation and
// is never logged.
func readLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat secret: %w", err)
	}
	if fi.Size() > maxBytes {
		return nil, ErrTooLarge
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	if int64(len(buf)) > maxBytes {
		return nil, ErrTooLarge
	}
	return buf, nil
}

// copyFileLimited copies src to dst while enforcing maxBytes. The destination
// is created with 0600.
func copyFileLimited(src, dst string, maxBytes int64) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open secret: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create materialized secret: %w", err)
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(in, maxBytes+1))
	if err != nil {
		return fmt.Errorf("copy secret: %w", err)
	}
	if n > maxBytes {
		return ErrTooLarge
	}
	return nil
}
