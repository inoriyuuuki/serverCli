package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"servercli/internal/config"
)

// MasterKey loads or creates the control plane's 32-byte claim/CSRF master
// key (0600 file in the state dir). Only derived hashes are persisted in the
// database.
func MasterKey(cfg *config.Config) ([]byte, error) {
	path := filepath.Join(cfg.AgentStateDir, "claim_master_key")
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return data[:32], nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("keys: master key generation failed: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// claimTokenFor deterministically derives the one-time claim token for an
// enrollment from the master key.
func claimTokenFor(master []byte, enrollmentID string) string {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("servercli:v1:claim:" + enrollmentID))
	return "ctok_" + hex.EncodeToString(mac.Sum(nil))[:48]
}

// credentialFor derives the node credential from the claim token.
func credentialFor(claimToken string) string {
	mac := hmac.New(sha256.New, []byte(claimToken))
	mac.Write([]byte("servercli:v1:node-credential"))
	return "ncred_" + hex.EncodeToString(mac.Sum(nil))[:48]
}

// csrfForSession deterministically derives the CSRF token bound to a session.
func csrfForSession(master []byte, sessionID string) string {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("servercli:v1:csrf:" + sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}
