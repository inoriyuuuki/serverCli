package repo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ManifestVersion is the only supported manifest version.
const ManifestVersion = 1

// ManifestFileName is the canonical manifest file name under manifests/.
const ManifestFileName = "repository-manifest.json"

// ManifestCanonicalFileName is the auxiliary file containing the exact
// canonical payload the manifest signature covers. Nodes and the bootstrap
// script HMAC this file (not the full manifest JSON, whose bytes differ from
// the canonical serialisation) so every verifier agrees on the same bytes.
const ManifestCanonicalFileName = "repository-manifest.canonical"

// ManifestObject describes one synced object in the repository.
type ManifestObject struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// RepositoryManifest is the JSON manifest describing the OSS-authoritative
// repository content.
//
// Signature is an HMAC-SHA256 (or RSA digest) over the canonical
// serialisation of objects; in V1 it is a placeholder produced with the
// deployment signing key, which is injected by the service layer. This
// package only verifies object existence, size and content hashes through
// VerifyObjects/VerifyManifest; it never validates the signature itself.
type RepositoryManifest struct {
	Version   int              `json:"version"`
	Objects   []ManifestObject `json:"objects"`
	Signature string           `json:"signature,omitempty"`
	SignedBy  string           `json:"signed_by,omitempty"`
	CreatedAt string           `json:"created_at,omitempty"`
}

// LoadManifest reads and parses rootDir/manifests/repository-manifest.json.
func LoadManifest(rootDir string) (*RepositoryManifest, error) {
	p := filepath.Join(rootDir, DirManifests, ManifestFileName)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", p, err)
	}
	var m RepositoryManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", p, err)
	}
	return &m, nil
}

// ValidateRelPath validates a repository-relative object path: it must be a
// non-empty relative path with no ".." components, no absolute path, no
// backslash and no control characters.
func ValidateRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path %q is absolute", p)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("path %q contains backslash", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("path %q contains a control character", p)
		}
	}
	for _, comp := range strings.Split(filepath.ToSlash(p), "/") {
		if comp == ".." {
			return fmt.Errorf("path %q contains a .. component", p)
		}
	}
	return nil
}

// VerifyObjects checks that every object in manifest exists under rootDir with
// the recorded size and SHA-256 hash. Paths are validated and symlinks are
// rejected so verification never follows a link out of rootDir. Any missing
// or mismatched object fails closed.
func VerifyObjects(manifest *RepositoryManifest, rootDir string) error {
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}
	if manifest.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}
	seen := make(map[string]struct{}, len(manifest.Objects))
	for i := range manifest.Objects {
		obj := &manifest.Objects[i]
		if err := ValidateRelPath(obj.Path); err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
		if _, dup := seen[obj.Path]; dup {
			return fmt.Errorf("object %d: duplicate path %q", i, obj.Path)
		}
		seen[obj.Path] = struct{}{}

		full := filepath.Join(rootDir, obj.Path)
		fi, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("object %d missing: %s", i, obj.Path)
			}
			return fmt.Errorf("object %d stat %s: %w", i, obj.Path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("object %d is a symlink: %s", i, obj.Path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("object %d is not a regular file: %s", i, obj.Path)
		}
		if fi.Size() != obj.Size {
			return fmt.Errorf("object %d size mismatch: want %d got %d (%s)", i, obj.Size, fi.Size(), obj.Path)
		}

		f, err := os.Open(full)
		if err != nil {
			return fmt.Errorf("object %d open %s: %w", i, obj.Path, err)
		}
		h := sha256.New()
		_, cerr := io.Copy(h, f)
		f.Close()
		if cerr != nil {
			return fmt.Errorf("object %d read %s: %w", i, obj.Path, cerr)
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(sum, obj.SHA256) {
			return fmt.Errorf("object %d sha256 mismatch (%s)", i, obj.Path)
		}
	}
	return nil
}

// VerifyManifest loads the manifest under rootDir and verifies every object
// (path safety, existence, size, SHA-256). It fails closed on any missing or
// mismatched object.
func VerifyManifest(ctx context.Context, rootDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := LoadManifest(rootDir)
	if err != nil {
		return err
	}
	if err := VerifyObjects(m, rootDir); err != nil {
		return err
	}
	return ctx.Err()
}

// CanonicalManifestPayload serialises the manifest object list in canonical
// form: objects sorted by path, encoded as a JSON array of
// {"path","size","sha256"} entries. The manifest signature is computed over
// exactly these bytes, so SignManifest/VerifyManifestSignature stay
// independent of field ordering or pretty-printing.
func CanonicalManifestPayload(m *RepositoryManifest) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("manifest is nil")
	}
	objs := make([]ManifestObject, len(m.Objects))
	copy(objs, m.Objects)
	sort.Slice(objs, func(i, j int) bool { return objs[i].Path < objs[j].Path })
	return json.Marshal(objs)
}

// SignManifest computes an HMAC-SHA256 signature over the canonical object
// payload with key and records the hex digest in Signature plus the signer
// identity in SignedBy. The key is the control-plane deployment signing key
// and is never embedded in the manifest.
func SignManifest(m *RepositoryManifest, key []byte) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if len(key) == 0 {
		return fmt.Errorf("manifest signing key must not be empty")
	}
	payload, err := CanonicalManifestPayload(m)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	m.Signature = hex.EncodeToString(mac.Sum(nil))
	m.SignedBy = "servercli-control-plane"
	return nil
}

// VerifyManifestSignature recomputes the HMAC-SHA256 signature over the
// canonical object payload and fails closed when the recorded signature is
// empty or does not match.
func VerifyManifestSignature(m *RepositoryManifest, key []byte) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.Signature == "" {
		return fmt.Errorf("manifest signature is empty")
	}
	if len(key) == 0 {
		return fmt.Errorf("manifest signing key must not be empty")
	}
	payload, err := CanonicalManifestPayload(m)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(m.Signature)), []byte(want)) {
		return fmt.Errorf("manifest signature mismatch")
	}
	return nil
}
