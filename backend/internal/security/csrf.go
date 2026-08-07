package security

import (
	"crypto/sha256"
	"encoding/hex"
)

// NewCSRFToken generates a random CSRF token.
func NewCSRFToken() (string, error) {
	return NewToken(24)
}

// HashCSRF hashes a CSRF token for server-side storage.
func HashCSRF(token string) string {
	sum := sha256.Sum256([]byte("csrf:" + token))
	return hex.EncodeToString(sum[:])
}
