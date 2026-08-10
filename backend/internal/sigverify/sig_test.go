package sigverify

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"filippo.io/age"
)

func pemPublicKey(pub ed25519.PublicKey) []byte {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func TestEd25519RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello manifest")
	sig := SignEd25519(priv, msg)
	if err := VerifyEd25519(pemPublicKey(pub), msg, sig); err != nil {
		t.Fatal(err)
	}
	// Tampered message must fail.
	if err := VerifyEd25519(pemPublicKey(pub), []byte("hello manifesT"), sig); err == nil {
		t.Fatal("tampered message verified")
	}
	// Wrong key must fail.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyEd25519(pemPublicKey(pub2), msg, sig); err == nil {
		t.Fatal("wrong key verified")
	}
}

func TestRawBase64Key(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := SignEd25519(priv, []byte("x"))
	// base64 raw key
	raw := base64Encode(pub)
	if err := VerifyEd25519([]byte(raw), []byte("x"), sig); err != nil {
		t.Fatal(err)
	}
}

func TestAgeRoundTrip(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	rec := id.Recipient()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("bundle-payload")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptAge([]byte(id.String()), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "bundle-payload" {
		t.Fatalf("plain = %q", plain)
	}
	// Wrong identity must fail.
	id2, _ := age.GenerateX25519Identity()
	if _, err := DecryptAge([]byte(id2.String()), buf.Bytes()); err == nil {
		t.Fatal("wrong identity decrypted")
	}
}

func base64Encode(b []byte) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		out = append(out, tbl[(n>>18)&0x3f])
		out = append(out, tbl[(n>>12)&0x3f])
		if rem > 1 {
			out = append(out, tbl[(n>>6)&0x3f])
		} else {
			out = append(out, '=')
		}
		if rem > 2 {
			out = append(out, tbl[n&0x3f])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}
