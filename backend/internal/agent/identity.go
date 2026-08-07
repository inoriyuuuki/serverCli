// Package agent implements the ServerCLI node agent.
package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Identity is the agent's persisted identity (0600).
type Identity struct {
	NodeID            string `json:"node_id"`
	NodeCredential    string `json:"node_credential"`
	InstanceRequestID string `json:"instance_request_id"`
	InstanceName      string `json:"instance_name"`
	InstanceRole      string `json:"instance_role"`
	PrivateKey        string `json:"private_key"` // base64 ed25519 seed
	CreatedAt         string `json:"created_at"`
}

// PublicKeyB64 returns the base64 ed25519 public key.
func (id *Identity) PublicKeyB64() (string, error) {
	seed, err := base64.StdEncoding.DecodeString(id.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("invalid identity private key")
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub), nil
}

// SignProof signs "ts|enrollmentID" with the identity key.
func (id *Identity) SignProof(ts, enrollmentID string) (string, error) {
	seed, err := base64.StdEncoding.DecodeString(id.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("invalid identity private key")
	}
	key := ed25519.NewKeyFromSeed(seed)
	sig := ed25519.Sign(key, []byte(ts+"|"+enrollmentID))
	return base64.StdEncoding.EncodeToString(sig), nil
}

// LoadIdentity reads identity.json from the state dir. It returns whatever is
// stored (possibly a pending pre-claim identity); callers check NodeID.
func LoadIdentity(stateDir string) (*Identity, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "identity.json"))
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

// SaveIdentity writes identity.json with 0600 permissions.
func (id *Identity) SaveIdentity(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(stateDir, ".identity.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(stateDir, "identity.json"))
}

// NewPendingIdentity creates an identity-less registration record with a fresh
// ed25519 keypair.
func NewPendingIdentity(stateDir string) (*Identity, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(seed)
	return &Identity{
		InstanceRequestID: hex.EncodeToString(hash[:16]),
		PrivateKey:        base64.StdEncoding.EncodeToString(seed),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// Fingerprint returns a stable instance fingerprint.
func (id *Identity) Fingerprint() string {
	sum := sha256.Sum256([]byte(id.InstanceRequestID + id.PrivateKey))
	return hex.EncodeToString(sum[:8])
}
