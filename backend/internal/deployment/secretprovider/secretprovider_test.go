package secretprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testCtx = context.Background()

// --- helpers ---------------------------------------------------------------

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newTestProvider builds a provider rooted at a fresh 0700 secrets dir with a
// plaintext codec registered under mode "none".
func newTestProvider(t *testing.T) (*PlaintextSecretProvider, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	codecs := map[string]RepositorySecretCodec{ModeNone: NewPlaintextSecretCodec()}
	return NewPlaintextProvider(root, codecs), root
}

// writeSecret writes rel under root with the given perms and returns the path.
func writeSecret(t *testing.T, root, rel string, content []byte, fileMode, dirMode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), dirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}
	return path
}

func refFor(rel, hash string, size int64) SecretReference {
	return SecretReference{
		ObjectKey:      rel,
		Version:        "v1",
		ContentHash:    hash,
		EncryptionMode: ModeNone,
		Size:           size,
	}
}

// --- PlaintextSecretCodec --------------------------------------------------

func TestPlaintextCodecValidate(t *testing.T) {
	c := NewPlaintextSecretCodec()
	if c.Mode() != ModeNone {
		t.Fatalf("mode = %q, want %q", c.Mode(), ModeNone)
	}
	content := []byte("password: hunter2\n")
	meta := SecretMetadata{
		Version:        "v1",
		ContentHash:    hashOf(content),
		EncryptionMode: ModeNone,
		Size:           int64(len(content)),
	}
	if err := c.Validate(testCtx, content, meta); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}

	// size mismatch
	badSize := meta
	badSize.Size = meta.Size + 1
	if err := c.Validate(testCtx, content, badSize); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("size mismatch: want ErrSizeMismatch, got %v", err)
	}

	// hash mismatch
	badHash := meta
	badHash.ContentHash = strings.Repeat("0", 64)
	if err := c.Validate(testCtx, content, badHash); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash mismatch: want ErrHashMismatch, got %v", err)
	}

	// wrong encryption mode rejected
	badMode := meta
	badMode.EncryptionMode = ModeAESGCM
	if err := c.Validate(testCtx, content, badMode); err == nil {
		t.Fatal("aes-gcm mode accepted by plaintext codec")
	}

	// empty encryption mode rejected
	emptyMode := meta
	emptyMode.EncryptionMode = ""
	if err := c.Validate(testCtx, content, emptyMode); err == nil {
		t.Fatal("empty mode accepted by plaintext codec")
	}
}

func TestPlaintextCodecIdentity(t *testing.T) {
	c := NewPlaintextSecretCodec()
	in := []byte("ak=LTfake\nsk=secret\n")
	meta := SecretMetadata{Version: "v1", EncryptionMode: ModeNone, Size: int64(len(in)), ContentHash: hashOf(in)}
	dec, err := c.Decode(testCtx, in, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, in) {
		t.Fatal("Decode must be identity for plaintext")
	}
	enc, err := c.Encode(testCtx, in, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, in) {
		t.Fatal("Encode must be identity for plaintext")
	}
}

// --- Provider: ResolveReference --------------------------------------------

func TestProviderResolveReferenceSuccess(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	hash := hashOf(content)
	writeSecret(t, root, "shared/profile.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/profile.secrets.yaml", hash, int64(len(content)))

	m, err := p.ResolveReference(testCtx, ref)
	if err != nil {
		t.Fatalf("ResolveReference failed: %v", err)
	}
	if m == nil {
		t.Fatal("nil material")
	}
	if m.Path != filepath.Join(root, ref.ObjectKey) {
		t.Fatalf("path = %q, want %q", m.Path, filepath.Join(root, ref.ObjectKey))
	}
	if _, err := os.Stat(m.Path); err != nil {
		t.Fatalf("material path not readable: %v", err)
	}
	if m.Version != "v1" || m.ContentHash != hash || m.EncryptionMode != ModeNone {
		t.Fatalf("material metadata mismatch: %+v", m)
	}
}

func TestProviderResolveReferenceNotFound(t *testing.T) {
	p, _ := newTestProvider(t)
	ref := refFor("shared/missing.secrets.yaml", hashOf([]byte("x")), 1)
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestProviderResolveReferenceHashMismatch(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/profile.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/profile.secrets.yaml", strings.Repeat("0", 64), int64(len(content)))
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
}

func TestProviderResolveReferenceSizeMismatch(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/profile.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/profile.secrets.yaml", hashOf(content), int64(len(content))+5)
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("want ErrSizeMismatch, got %v", err)
	}
}

func TestProviderResolveReferenceBadPerms(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	hash := hashOf(content)

	// file 0644 -> reject
	writeSecret(t, root, "shared/worldread.secrets.yaml", content, 0644, 0700)
	ref := refFor("shared/worldread.secrets.yaml", hash, int64(len(content)))
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrInsecurePerm) {
		t.Fatalf("world-readable file: want ErrInsecurePerm, got %v", err)
	}

	// directory 0755 -> reject
	writeSecret(t, root, "shared/worldreaddir.secrets.yaml", content, 0600, 0755)
	ref2 := refFor("shared/worldreaddir.secrets.yaml", hash, int64(len(content)))
	if _, err := p.ResolveReference(testCtx, ref2); !errors.Is(err, ErrInsecurePerm) {
		t.Fatalf("world-readable dir: want ErrInsecurePerm, got %v", err)
	}
}

func TestProviderResolveReferencePathTraversal(t *testing.T) {
	p, _ := newTestProvider(t)
	outside := filepath.Join(t.TempDir(), "outside.secrets.yaml")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := hashOf([]byte("x"))
	cases := []SecretReference{
		refFor("../outside.secrets.yaml", hash, 1),
		refFor("shared/../../outside.secrets.yaml", hash, 1),
		refFor("..", hash, 1),
		refFor(".", hash, 1),
		refFor("", hash, 1),
		{ObjectKey: outside, ContentHash: hash, EncryptionMode: ModeNone, Size: 1}, // absolute
	}
	for _, ref := range cases {
		if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("object key %q: want ErrPathEscape, got %v", ref.ObjectKey, err)
		}
	}
}

func TestProviderResolveReferenceSymlinkEscape(t *testing.T) {
	p, root := newTestProvider(t)
	outside := filepath.Join(t.TempDir(), "outside.secrets.yaml")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.secrets.yaml")); err != nil {
		t.Fatal(err)
	}
	ref := refFor("link.secrets.yaml", hashOf([]byte("x")), 1)
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("symlink escape: want ErrPathEscape, got %v", err)
	}
}

func TestProviderResolveReferenceUnsupportedModes(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/profile.secrets.yaml", content, 0600, 0700)
	hash := hashOf(content)

	refAES := refFor("shared/profile.secrets.yaml", hash, int64(len(content)))
	refAES.EncryptionMode = ModeAESGCM
	if _, err := p.ResolveReference(testCtx, refAES); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("aes-gcm: want ErrNotImplemented, got %v", err)
	}

	refFoo := refFor("shared/profile.secrets.yaml", hash, int64(len(content)))
	refFoo.EncryptionMode = "foo"
	if _, err := p.ResolveReference(testCtx, refFoo); !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("foo: want ErrUnsupportedMode, got %v", err)
	}
}

func TestProviderResolveReferenceTooLarge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	codecs := map[string]RepositorySecretCodec{ModeNone: NewPlaintextSecretCodec()}
	p := NewPlaintextProvider(root, codecs, WithMaxSecretBytes(5))
	content := []byte("0123456789")
	writeSecret(t, root, "shared/big.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/big.secrets.yaml", hashOf(content), int64(len(content)))
	if _, err := p.ResolveReference(testCtx, ref); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

// --- Provider: ValidateMetadata --------------------------------------------

func TestProviderValidateMetadata(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("k: v\n")
	writeSecret(t, root, "shared/p.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/p.secrets.yaml", hashOf(content), int64(len(content)))
	if err := p.ValidateMetadata(testCtx, ref); err != nil {
		t.Fatalf("ValidateMetadata failed: %v", err)
	}

	bad := ref
	bad.ContentHash = strings.Repeat("0", 64)
	if err := p.ValidateMetadata(testCtx, bad); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}

	missing := refFor("shared/missing.secrets.yaml", hashOf(content), int64(len(content)))
	if err := p.ValidateMetadata(testCtx, missing); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestProviderValidateMetadataSkipsPermissions(t *testing.T) {
	// ValidateMetadata is metadata-only: it must NOT reject on insecure perms.
	p, root := newTestProvider(t)
	content := []byte("k: v\n")
	writeSecret(t, root, "shared/p.secrets.yaml", content, 0644, 0755)
	ref := refFor("shared/p.secrets.yaml", hashOf(content), int64(len(content)))
	if err := p.ValidateMetadata(testCtx, ref); err != nil {
		t.Fatalf("metadata-only validation must not check perms: %v", err)
	}
}

// --- Provider: Materialize + Cleanup ---------------------------------------

func TestProviderMaterializeAndCleanup(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	hash := hashOf(content)
	writeSecret(t, root, "nodes/n1/feature.secrets.yaml", content, 0600, 0700)
	ref := refFor("nodes/n1/feature.secrets.yaml", hash, int64(len(content)))

	dst := filepath.Join(root, "rendered", ref.ObjectKey)
	if err := p.Materialize(testCtx, ref, dst); err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("materialized mode = %04o, want 0600", fi.Mode().Perm())
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("materialized content mismatch")
	}

	if err := p.Cleanup(testCtx, ref); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("Cleanup did not remove the materialized file")
	}
	// Cleanup is idempotent.
	if err := p.Cleanup(testCtx, ref); err != nil {
		t.Fatalf("second Cleanup failed: %v", err)
	}
}

func TestProviderMaterializeHashMismatch(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/feature.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/feature.secrets.yaml", strings.Repeat("0", 64), int64(len(content)))
	dst := filepath.Join(root, "rendered", ref.ObjectKey)
	if err := p.Materialize(testCtx, ref, dst); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("failed Materialize must not leave a destination file")
	}
}

func TestProviderMaterializeDstEscape(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/feature.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/feature.secrets.yaml", hashOf(content), int64(len(content)))

	// absolute dst outside the rendered root
	outside := filepath.Join(t.TempDir(), "out.secrets.yaml")
	if err := p.Materialize(testCtx, ref, outside); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("absolute escape: want ErrPathEscape, got %v", err)
	}

	// traversal dst
	rendered := filepath.Join(root, "rendered")
	esc := filepath.Join(rendered, "..", "..", "escape.secrets.yaml")
	if err := p.Materialize(testCtx, ref, esc); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("traversal escape: want ErrPathEscape, got %v", err)
	}
}

func TestProviderMaterializeDstSymlinkEscape(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("token: abc123\n")
	writeSecret(t, root, "shared/feature.secrets.yaml", content, 0600, 0700)
	ref := refFor("shared/feature.secrets.yaml", hashOf(content), int64(len(content)))

	rendered := filepath.Join(root, "rendered")
	if err := os.MkdirAll(rendered, 0700); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(rendered, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(rendered, "link", "out.secrets.yaml")
	if err := p.Materialize(testCtx, ref, dst); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("symlink escape: want ErrPathEscape, got %v", err)
	}
}

func TestProviderCleanupRejectsUnsafeKeys(t *testing.T) {
	p, root := newTestProvider(t)
	if err := os.MkdirAll(filepath.Join(root, "rendered"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", ".", "..", "../evil.secrets.yaml", "/abs/evil.secrets.yaml"} {
		ref := refFor(key, hashOf([]byte("x")), 1)
		if err := p.Cleanup(testCtx, ref); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("key %q: want ErrPathEscape, got %v", key, err)
		}
	}
}

// --- Errors must never leak secret content ---------------------------------

func TestErrorsNeverContainSecretContent(t *testing.T) {
	p, root := newTestProvider(t)
	content := []byte("topsecret-payload-987654321")
	writeSecret(t, root, "shared/s.secrets.yaml", content, 0600, 0700)

	// hash mismatch
	bad := refFor("shared/s.secrets.yaml", strings.Repeat("0", 64), int64(len(content)))
	if _, err := p.ResolveReference(testCtx, bad); err == nil {
		t.Fatal("expected error")
	} else if strings.Contains(err.Error(), string(content)) {
		t.Fatal("hash-mismatch error leaked secret content")
	}

	// size mismatch
	badSize := refFor("shared/s.secrets.yaml", hashOf(content), int64(len(content))+1)
	if _, err := p.ResolveReference(testCtx, badSize); err == nil {
		t.Fatal("expected error")
	} else if strings.Contains(err.Error(), string(content)) {
		t.Fatal("size-mismatch error leaked secret content")
	}
}

// --- Registry + future stubs -----------------------------------------------

func TestRegistry(t *testing.T) {
	r := NewRegistry(NewPlaintextSecretCodec())
	if got := r.Modes(); len(got) != 1 || got[0] != ModeNone {
		t.Fatalf("registered modes = %v, want [none]", got)
	}
	c, err := r.Get(ModeNone)
	if err != nil {
		t.Fatalf("Get(none) failed: %v", err)
	}
	if c.Mode() != ModeNone {
		t.Fatalf("Get(none).Mode() = %q", c.Mode())
	}

	if _, err := r.Get(ModeAESGCM); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Get(aes-gcm): want ErrNotImplemented, got %v", err)
	}
	if _, err := r.Get(ModeKMSEnvelope); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Get(kms-envelope): want ErrNotImplemented, got %v", err)
	}
	if _, err := r.Get("foo"); !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("Get(foo): want ErrUnsupportedMode, got %v", err)
	}
}

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Get(ModeNone); !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("empty registry: want ErrUnsupportedMode, got %v", err)
	}
	if err := r.Register(NewPlaintextSecretCodec()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ModeNone); err != nil {
		t.Fatalf("Get(none) after Register failed: %v", err)
	}
}

func TestFutureCodecStubs(t *testing.T) {
	stubs := map[string]RepositorySecretCodec{
		ModeAESGCM:      &AESGCMSecretCodec{},
		ModeKMSEnvelope: &KMSEnvelopeSecretCodec{},
	}
	for mode, c := range stubs {
		if c.Mode() != mode {
			t.Fatalf("%s stub Mode() = %q", mode, c.Mode())
		}
		if _, err := c.Decode(testCtx, nil, SecretMetadata{}); !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s Decode: want ErrNotImplemented, got %v", mode, err)
		}
		if _, err := c.Encode(testCtx, nil, SecretMetadata{}); !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s Encode: want ErrNotImplemented, got %v", mode, err)
		}
		if err := c.Validate(testCtx, nil, SecretMetadata{}); !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s Validate: want ErrNotImplemented, got %v", mode, err)
		}
	}
}
