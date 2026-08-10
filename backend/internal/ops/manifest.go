package ops

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"servercli/internal/sigverify"
)

// ManifestSchemaVersion is the schema version of BackupManifest.
const ManifestSchemaVersion = "1"

// BackupFile is one file captured in a backup with its digest and size.
type BackupFile struct {
	Path   string `json:"path"`   // path relative to the backup root
	SHA256 string `json:"sha256"` // hex digest
	Size   int64  `json:"size"`
}

// BackupManifest is the signed manifest for one backup of one service. The
// signature covers the canonical JSON of every other field, so digest values
// and metadata cannot be tampered with independently of the signature.
type BackupManifest struct {
	SchemaVersion   string       `json:"schema_version"`
	BackupID        string       `json:"backup_id"`
	RecoverySetID   string       `json:"recovery_set_id"`
	Service         string       `json:"service"`
	Node            string       `json:"node"`
	Environment     string       `json:"environment"`
	AppVersion      string       `json:"app_version"`
	DBSchemaVersion string       `json:"db_schema_version,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	Files           []BackupFile `json:"files"`
	Dependencies    []string     `json:"dependencies,omitempty"`
	Signature       string       `json:"signature,omitempty"`
	SigningKeyID    string       `json:"signing_key_id,omitempty"`
}

// Canonical returns the signature input: the manifest with Signature cleared.
func (m *BackupManifest) Canonical() ([]byte, error) {
	cp := *m
	cp.Signature = ""
	return json.Marshal(&cp)
}

// Sign signs the canonical manifest with an injected Ed25519 private key.
// Tests must inject randomly generated keys; real private keys never appear in
// tests, logs or fixtures.
func (m *BackupManifest) Sign(priv ed25519.PrivateKey) error {
	canon, err := m.Canonical()
	if err != nil {
		return err
	}
	m.Signature = sigverify.SignEd25519(priv, canon)
	return nil
}

// Verify verifies the manifest signature with the given PEM/raw public key.
func (m *BackupManifest) Verify(pubPEM []byte) error {
	canon, err := m.Canonical()
	if err != nil {
		return err
	}
	return sigverify.VerifyEd25519(pubPEM, canon, m.Signature)
}

// sha256File hashes a file and returns hex digest + size.
func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// collectFiles walks dir and returns every regular file (symlinks refused)
// sorted by relative path. It is used both at backup time and to verify a
// stored backup.
func collectFiles(dir string) ([]BackupFile, error) {
	var files []BackupFile
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("ops: refusing symlink in backup: %s", p)
		}
		digest, size, err := sha256File(p)
		if err != nil {
			return err
		}
		files = append(files, BackupFile{Path: filepath.ToSlash(rel), SHA256: digest, Size: size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// verifyFiles re-verifies the digest of every file in a backup root and
// performs a read-back check: the file is re-read in full and hashed again.
// This implements the local "digest verification + readback verification"
// completion condition; remote verification is delegated to Uploader.
func verifyFiles(files []BackupFile, root string) error {
	for _, bf := range files {
		if strings.Contains(bf.Path, "..") || filepath.IsAbs(bf.Path) {
			return fmt.Errorf("ops: unsafe path in manifest: %q", bf.Path)
		}
		p := filepath.Join(root, filepath.FromSlash(bf.Path))
		digest, size, err := sha256File(p)
		if err != nil {
			return fmt.Errorf("ops: digest verify %s: %w", bf.Path, err)
		}
		if digest != bf.SHA256 {
			return fmt.Errorf("ops: digest mismatch for %s: got %s want %s", bf.Path, digest, bf.SHA256)
		}
		if size != bf.Size {
			return fmt.Errorf("ops: size mismatch for %s: got %d want %d", bf.Path, size, bf.Size)
		}
		// Read-back verification: re-read full content and hash once more.
		readback, _, err := sha256File(p)
		if err != nil {
			return fmt.Errorf("ops: readback %s: %w", bf.Path, err)
		}
		if readback != bf.SHA256 {
			return fmt.Errorf("ops: readback digest mismatch for %s", bf.Path)
		}
	}
	return nil
}
