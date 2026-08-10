// Package sigverify provides Ed25519 signature verification for release and
// bundle manifests plus age v1 (X25519) decryption for SOPS/age bundles.
//
// The same release public key verifies artifacts from both GitHub Releases and
// the OSS mirror; digests inside a signed manifest are the trust anchor and
// bare SHA256 is never treated as proof of authenticity.
package sigverify

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// ParsePublicKeyPEM parses an Ed25519 public key in PKIX ("PUBLIC KEY") PEM
// form, or a raw base64 32-byte key.
func ParsePublicKeyPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKIX public key: %w", err)
		}
		ed, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key is %T, not Ed25519", pub)
		}
		return ed, nil
	}
	// Raw base64 32-byte Ed25519 public key.
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(pemBytes)))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("invalid raw Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

// VerifyEd25519 verifies sig (base64) over msg with the given PEM/raw key.
func VerifyEd25519(pubPEM, msg []byte, sigB64 string) error {
	pub, err := ParsePublicKeyPEM(pubPEM)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("Ed25519 signature verification failed")
	}
	return nil
}

// SignEd25519 signs msg with a raw Ed25519 private key and returns base64 sig.
func SignEd25519(priv ed25519.PrivateKey, msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
}

// LoadPublicKeyFile reads a PEM public key from disk.
func LoadPublicKeyFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// DecryptAge decrypts an age-encrypted payload using an X25519 identity
// parsed from PEM (filippo.io/age format).
func DecryptAge(identityPEM []byte, ciphertext []byte) ([]byte, error) {
	identities, err := parseIdentities(identityPEM)
	if err != nil {
		return nil, err
	}
	rr := bytes.NewReader(ciphertext)
	out, err := age.Decrypt(rr, identities...)
	if err != nil {
		return nil, fmt.Errorf("age decrypt: %w", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(out); err != nil {
		return nil, fmt.Errorf("age read: %w", err)
	}
	return buf.Bytes(), nil
}

func parseIdentities(pemBytes []byte) ([]age.Identity, error) {
	// Try native age identity first ("AGE-SECRET-KEY-...").
	if id, err := age.ParseX25519Identity(string(pemBytes)); err == nil {
		return []age.Identity{id}, nil
	}
	// Try SSH ed25519 identity.
	if id, err := agessh.ParseIdentity(pemBytes); err == nil {
		return []age.Identity{id}, nil
	}
	return nil, errors.New("no usable age identity found in key material")
}
