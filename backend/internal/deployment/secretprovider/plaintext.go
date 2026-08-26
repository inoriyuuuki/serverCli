package secretprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// PlaintextSecretCodec is the V1 codec for mode "none". Decode/Encode are
// identity operations: payloads are stored as plaintext and there is NO
// pseudo-encryption or reversible encoding. mode "none" explicitly means
// plaintext.
type PlaintextSecretCodec struct{}

// NewPlaintextSecretCodec returns a codec for mode "none".
func NewPlaintextSecretCodec() *PlaintextSecretCodec { return &PlaintextSecretCodec{} }

// Mode returns "none".
func (c *PlaintextSecretCodec) Mode() string { return ModeNone }

// Decode returns in unchanged: V1 plaintext needs no decoding.
func (c *PlaintextSecretCodec) Decode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return in, nil
}

// Encode returns in unchanged: V1 plaintext is stored as-is.
func (c *PlaintextSecretCodec) Encode(ctx context.Context, in []byte, meta SecretMetadata) ([]byte, error) {
	return in, nil
}

// Validate checks that the payload matches the declared metadata: encryption
// mode must be "none", the size must match and the SHA-256 content hash must
// match. Path confinement (no "..", no absolute-path or symlink escapes) is
// enforced by the provider layer, which resolves keys against the allowed
// secrets root before handing raw bytes to the codec.
func (c *PlaintextSecretCodec) Validate(ctx context.Context, raw []byte, meta SecretMetadata) error {
	if meta.EncryptionMode != ModeNone {
		return fmt.Errorf("%w: plaintext codec rejects mode %q", ErrUnsupportedMode, meta.EncryptionMode)
	}
	if meta.Size >= 0 && int64(len(raw)) != meta.Size {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrSizeMismatch, len(raw), meta.Size)
	}
	if meta.ContentHash != "" {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != meta.ContentHash {
			return ErrHashMismatch
		}
	}
	return nil
}

// ProviderOption configures a PlaintextSecretProvider.
type ProviderOption func(*PlaintextSecretProvider)

// WithRenderedRoot overrides the default rendered root (rootDir/rendered).
// Rendered output is a sensitive staging area (e.g. .servercli-local/rendered/)
// and is created/owned by the caller.
func WithRenderedRoot(dir string) ProviderOption {
	return func(p *PlaintextSecretProvider) { p.renderedRoot = dir }
}

// WithMaxSecretBytes overrides the default 1 MiB read limit.
func WithMaxSecretBytes(n int64) ProviderOption {
	return func(p *PlaintextSecretProvider) { p.maxSecretBytes = n }
}

// PlaintextSecretProvider is the V1 provider. It resolves secrets stored as
// plaintext files under a private repository secrets root and materializes
// copies into an allowed rendered root.
type PlaintextSecretProvider struct {
	rootDir        string
	renderedRoot   string
	codecs         map[string]RepositorySecretCodec
	maxSecretBytes int64
}

// NewPlaintextProvider returns a V1 plaintext provider rooted at rootDir
// (e.g. .../repository/secrets). codecs maps encryption modes to codecs and
// must contain at least ModeNone for V1 secrets. In this provider source paths
// are always resolved against the configured rootDir;
// SecretReference.RepositoryRoot is carried as reference metadata for future
// providers.
func NewPlaintextProvider(rootDir string, codecs map[string]RepositorySecretCodec, opts ...ProviderOption) *PlaintextSecretProvider {
	p := &PlaintextSecretProvider{
		rootDir:        rootDir,
		renderedRoot:   filepath.Join(rootDir, "rendered"),
		codecs:         codecs,
		maxSecretBytes: DefaultMaxSecretBytes,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// codecFor returns the codec for mode, mapping the reserved future modes to
// ErrNotImplemented so they read as "reserved, not yet available".
func (p *PlaintextSecretProvider) codecFor(mode string) (RepositorySecretCodec, error) {
	if c, ok := p.codecs[mode]; ok && c != nil {
		return c, nil
	}
	switch mode {
	case ModeAESGCM, ModeKMSEnvelope:
		return nil, ErrNotImplemented
	default:
		return nil, ErrUnsupportedMode
	}
}

// verifySecretFile validates a reference against the repository root: safe
// path, existence, permissions (when checkPerms is true), size, hash and codec
// mode. It returns the absolute path to the secret file. The payload is read
// only to verify its hash and is never logged or included in errors.
func (p *PlaintextSecretProvider) verifySecretFile(ctx context.Context, ref SecretReference, checkPerms bool) (string, error) {
	if isUnsafeRelativePath(ref.ObjectKey) {
		return "", ErrPathEscape
	}
	codec, err := p.codecFor(ref.EncryptionMode)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(p.rootDir, ref.ObjectKey)
	if _, err := os.Lstat(joined); err != nil {
		if os.IsNotExist(err) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("stat secret: %w", err)
	}
	path, err := resolveWithinRoot(p.rootDir, ref.ObjectKey)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat secret: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("secret is not a regular file")
	}
	if checkPerms {
		if err := p.verifyPermissions(path); err != nil {
			return "", err
		}
	}
	if ref.Size >= 0 && fi.Size() != ref.Size {
		return "", ErrSizeMismatch
	}
	if fi.Size() > p.maxSecretBytes {
		return "", ErrTooLarge
	}
	raw, err := readLimited(path, p.maxSecretBytes)
	if err != nil {
		return "", err
	}
	meta := SecretMetadata{
		Version:        ref.Version,
		ContentHash:    ref.ContentHash,
		EncryptionMode: ref.EncryptionMode,
		Size:           ref.Size,
	}
	if err := codec.Validate(ctx, raw, meta); err != nil {
		return "", err
	}
	return path, nil
}

// verifyPermissions enforces the V1 on-disk rules: the secret file must be
// 0600 and every directory from the repository root down to the file's parent
// must be 0700.
func (p *PlaintextSecretProvider) verifyPermissions(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat secret: %w", err)
	}
	if fi.Mode().Perm() != 0600 {
		return fmt.Errorf("%w: file mode %04o, want 0600", ErrInsecurePerm, fi.Mode().Perm())
	}
	dir := filepath.Dir(path)
	for {
		di, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("stat secret directory: %w", err)
		}
		if !di.IsDir() {
			return fmt.Errorf("%w: %q is not a directory", ErrInsecurePerm, dir)
		}
		if di.Mode().Perm() != 0700 {
			return fmt.Errorf("%w: directory mode %04o, want 0700", ErrInsecurePerm, di.Mode().Perm())
		}
		if filepath.Clean(dir) == filepath.Clean(p.rootDir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

// ResolveReference validates ref and returns the resolved on-disk material.
// The secret payload is never returned or logged.
func (p *PlaintextSecretProvider) ResolveReference(ctx context.Context, ref SecretReference) (*SecretMaterial, error) {
	path, err := p.verifySecretFile(ctx, ref, true)
	if err != nil {
		return nil, err
	}
	return &SecretMaterial{
		Path:           path,
		Version:        ref.Version,
		ContentHash:    ref.ContentHash,
		EncryptionMode: ref.EncryptionMode,
	}, nil
}

// ValidateMetadata verifies only the reference metadata: path safety,
// existence, size, content hash and encryption mode. It never reads secret
// content into logs.
func (p *PlaintextSecretProvider) ValidateMetadata(ctx context.Context, ref SecretReference) error {
	_, err := p.verifySecretFile(ctx, ref, false)
	return err
}

// Materialize copies the secret at repositoryRoot/objectKey to dstPath inside
// the allowed rendered root, verifies size + hash of the copy and sets 0600.
// Callers are expected to materialize under renderedRoot/<objectKey> so that
// Cleanup can remove the temporary copy.
func (p *PlaintextSecretProvider) Materialize(ctx context.Context, ref SecretReference, dstPath string) error {
	src, err := p.verifySecretFile(ctx, ref, true)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.renderedRoot, 0700); err != nil {
		return fmt.Errorf("ensure rendered root: %w", err)
	}
	target, err := resolveDstWithinRoot(p.renderedRoot, dstPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	// Re-resolve after directory creation to catch any symlink introduced
	// between validation and write.
	if _, err := resolveDstWithinRoot(p.renderedRoot, dstPath); err != nil {
		return err
	}
	if err := copyFileLimited(src, target, p.maxSecretBytes); err != nil {
		return err
	}
	if err := os.Chmod(target, 0600); err != nil {
		return fmt.Errorf("chmod materialized secret: %w", err)
	}
	// Post-copy verification: size and hash must still match.
	fi, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat materialized secret: %w", err)
	}
	if ref.Size >= 0 && fi.Size() != ref.Size {
		return ErrSizeMismatch
	}
	raw, err := readLimited(target, p.maxSecretBytes)
	if err != nil {
		return err
	}
	meta := SecretMetadata{
		Version:        ref.Version,
		ContentHash:    ref.ContentHash,
		EncryptionMode: ref.EncryptionMode,
		Size:           ref.Size,
	}
	codec, err := p.codecFor(ref.EncryptionMode)
	if err != nil {
		return err
	}
	if err := codec.Validate(ctx, raw, meta); err != nil {
		return err
	}
	return nil
}

// Cleanup removes the temporary materialized copy at renderedRoot/<objectKey>.
// It never deletes outside the rendered root and is idempotent when no copy
// exists.
func (p *PlaintextSecretProvider) Cleanup(ctx context.Context, ref SecretReference) error {
	if _, err := os.Stat(p.renderedRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat rendered root: %w", err)
	}
	target, err := resolveDstWithinRoot(p.renderedRoot, ref.ObjectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cleanup materialized secret: %w", err)
	}
	return nil
}

var (
	_ RepositorySecretProvider = (*PlaintextSecretProvider)(nil)
	_ RepositorySecretCodec    = (*PlaintextSecretCodec)(nil)
)
