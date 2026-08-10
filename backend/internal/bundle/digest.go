package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// sha256Hex returns the lowercase hex sha256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// checkSHA256Hex validates that s is a 64-char lowercase/uppercase hex digest.
func checkSHA256Hex(s string) error {
	if len(s) != sha256.Size*2 {
		return fmt.Errorf("sha256 digest must be %d hex chars, got %d", sha256.Size*2, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("invalid sha256 digest: %w", err)
	}
	return nil
}

// equalDigest compares two hex digests case-insensitively.
func equalDigest(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
