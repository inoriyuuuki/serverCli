package bundle

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"servercli/internal/bootstrap"
	"servercli/internal/sigverify"
)

func signedBundleManifest(t *testing.T, m *bootstrap.BundleManifest, priv ed25519.PrivateKey, pubPEM []byte) *bootstrap.BundleManifest {
	t.Helper()
	canon, err := CanonicalManifestBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = sigverify.SignEd25519(priv, canon)
	if err := verifyManifestSignature(m, pubPEM, m.Signature); err != nil {
		t.Fatalf("self-sign should verify: %v", err)
	}
	return m
}

func TestVerifyBundleManifestSuccess(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)

	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); err != nil {
		t.Fatalf("VerifyBundleManifest: %v", err)
	}
}

func TestVerifyBundleManifestTamperedSignature(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)
	m.BundleVersion = "9.9.9" // mutate after signing
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); err == nil {
		t.Fatal("expected signature failure after tampering")
	}
}

func TestVerifyBundleManifestWrongKey(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	otherPriv, _ := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)
	canon, _ := CanonicalManifestBytes(m)
	m.Signature = sigverify.SignEd25519(otherPriv, canon)
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); err == nil {
		t.Fatal("expected signature failure for wrong key")
	}
}

func TestVerifyBundleManifestEnvironmentMismatch(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "staging", false); err == nil {
		t.Fatal("expected environment mismatch error")
	}
}

func TestVerifyBundleManifestExpired(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "1.0.0")
	past := time.Now().UTC().Add(-time.Hour)
	m.ExpiresAt = &past
	signedBundleManifest(t, m, priv, pubPEM)
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerifyBundleManifestMinimumBootstrapTooHigh(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.5.0", "2.0.0")
	signedBundleManifest(t, m, priv, pubPEM)
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); err == nil {
		t.Fatal("expected minimum_bootstrap_version error")
	}
}

func TestVerifyBundleManifestReplayProductionRejectedAlways(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	// Old bundle (1.0.0) being replayed against a 1.5.0 bootstrap.
	m := testBundleManifest("prod", "node-a", "bundle-1", "1.0.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)

	// allowDevReplay must NOT help in production.
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", true); !errors.Is(err, ErrReplayRejected) {
		t.Fatalf("expected ErrReplayRejected in production, got %v", err)
	}
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "prod", false); !errors.Is(err, ErrReplayRejected) {
		t.Fatalf("expected ErrReplayRejected in production, got %v", err)
	}
}

func TestVerifyBundleManifestReplayDev(t *testing.T) {
	priv, pubPEM := testEd25519Key(t)
	m := testBundleManifest("dev", "node-a", "bundle-1", "1.0.0", "1.0.0")
	signedBundleManifest(t, m, priv, pubPEM)

	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "dev", true); err != nil {
		t.Fatalf("dev replay with allowDevReplay should pass: %v", err)
	}
	if err := VerifyBundleManifest(m, pubPEM, "1.5.0", "dev", false); !errors.Is(err, ErrReplayRejected) {
		t.Fatalf("dev replay without allowDevReplay should be rejected, got %v", err)
	}
	// test environment behaves like dev (manifest environment must match).
	mTest := testBundleManifest("test", "node-a", "bundle-1", "1.0.0", "1.0.0")
	signedBundleManifest(t, mTest, priv, pubPEM)
	if err := VerifyBundleManifest(mTest, pubPEM, "1.5.0", "test", true); err != nil {
		t.Fatalf("test replay with allowDevReplay should pass: %v", err)
	}
}

func TestDecryptBundleSuccessAndFailure(t *testing.T) {
	id, rec := testAgeIdentity(t)
	plain := []byte(`{"inventory":"x","secrets":{}}`)
	cipher := ageEncrypt(t, rec, plain)

	got, err := DecryptBundle(cipher, []byte(id.String()))
	if err != nil {
		t.Fatalf("DecryptBundle: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted payload mismatch")
	}

	// Wrong key must fail.
	id2, _ := testAgeIdentity(t)
	if _, err := DecryptBundle(cipher, []byte(id2.String())); err == nil {
		t.Fatal("expected decrypt failure with wrong age key")
	}
	if _, err := DecryptBundle(nil, []byte(id.String())); err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestLoadBundleManifestRoundTrip(t *testing.T) {
	priv, _ := testEd25519Key(t)
	m := testBundleManifest("dev", "node-a", "bundle-1", "1.0.0", "1.0.0")
	signManifest(t, priv, m)
	raw, _ := json.Marshal(m)
	got, err := LoadBundleManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.BundleID != m.BundleID || got.Signature != m.Signature {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, m)
	}
	if _, err := LoadBundleManifest([]byte("{bad")); err == nil {
		t.Fatal("expected parse error")
	}
}
