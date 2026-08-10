// Package secretstore implements the database-free local Bootstrap Secret
// Store: an age/master-key encrypted file at
// /var/lib/servercli/bootstrap/secrets.enc with a root-only master key at
// /etc/servercli/keys/master.key.
//
// Security model (first version): root-only file permissions protect against
// accidental commit, ordinary-user reads and plaintext on disk. It does not
// claim to resist root / full-disk compromise.
package secretstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/chacha20poly1305"
)

// MasterKey is a 32-byte root-owned key used to seal the bootstrap store.
type MasterKey struct {
	raw     []byte
	keyFile string
}

// LoadOrCreateMasterKey loads the master key from path, creating it (0600 in a
// 0700 dir) if missing. It rejects symlinks, wrong owner (when euid==0) and
// loose permissions.
func LoadOrCreateMasterKey(path string) (*MasterKey, error) {
	if err := secureFile(path, 0o600, true); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(raw) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("master key: invalid length %d", len(raw))
	}
	return &MasterKey{raw: raw, keyFile: path}, nil
}

// secureFile validates an existing path (or creates it with perm) enforcing
// 0700 dir, 0600 file, no symlink, owner root when euid==0.
func secureFile(path string, perm os.FileMode, create bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: refusing symlink", path)
		}
		if fi.Mode().Perm() != perm {
			// Tighten, never loosen.
			if err := os.Chmod(path, perm); err != nil {
				return err
			}
		}
		if os.Geteuid() == 0 {
			st, ok := fi.Sys().(*syscall.Stat_t)
			if ok && int(st.Uid) != 0 {
				return fmt.Errorf("%s: owner is not root", path)
			}
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !create {
		return os.ErrNotExist
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	// Master key = 32 random bytes.
	raw := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Bytes returns the raw key (callers must not log it).
func (k *MasterKey) Bytes() []byte { return k.raw }

// Store is a sealed key/value secret map backed by an encrypted file.
type Store struct {
	path string
	key  *MasterKey
}

// OpenBootstrapStore loads (or creates empty) the encrypted store.
func OpenBootstrapStore(path string, key *MasterKey) (*Store, error) {
	s := &Store{path: path, key: key}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

type sealedData struct {
	Nonce  []byte `json:"nonce"`
	Cipher []byte `json:"cipher"`
}

// Get returns a secret value.
func (s *Store) Get(k string) (string, bool) {
	m, err := s.load()
	if err != nil {
		return "", false
	}
	v, ok := m[k]
	return v, ok
}

// Set stores a secret and immediately persists atomically with fsync.
func (s *Store) Set(k, v string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	m[k] = v
	return s.save(m)
}

// Delete removes a secret and persists.
func (s *Store) Delete(k string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, k)
	return s.save(m)
}

// List returns secret keys only (never values).
func (s *Store) List() ([]string, error) {
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *Store) load() (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read secret store: %w", err)
	}
	var sealed sealedData
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return nil, fmt.Errorf("secret store corrupt: %w", err)
	}
	aead, err := chacha20poly1305.NewX(s.key.raw)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, sealed.Nonce, sealed.Cipher, nil)
	if err != nil {
		return nil, errors.New("secret store: decryption failed (wrong master key?)")
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("secret store payload corrupt: %w", err)
	}
	return m, nil
}

func (s *Store) save(m map[string]string) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(s.key.raw)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := sealedData{Nonce: nonce, Cipher: aead.Seal(nil, nonce, plain, nil)}
	raw, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Fingerprint returns a stable non-secret identifier of the store contents
// (hash of key set + ciphertext) for drift/audit comparisons.
func (s *Store) Fingerprint() (string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "empty", nil
		}
		return "", err
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// SanitizeName validates a secret key name: [A-Za-z_][A-Za-z0-9_]*, upper-case
// letters, dots and dashes allowed for grouping, no shell metacharacters.
func SanitizeName(k string) error {
	if k == "" {
		return errors.New("secret key empty")
	}
	if len(k) > 256 {
		return errors.New("secret key too long")
	}
	for i, r := range k {
		ok := r == '_' || r == '.' || r == '-'
		if i == 0 {
			ok = (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_'
		} else if !ok {
			ok = (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-'
		}
		if !ok {
			return fmt.Errorf("invalid secret key %q", k)
		}
	}
	if strings.ContainsAny(k, "$`\\;|&<>(){}[]*?~!#%'\"") {
		return fmt.Errorf("invalid secret key %q", k)
	}
	return nil
}
