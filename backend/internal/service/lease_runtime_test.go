package service

import (
	"bytes"
	"testing"
	"time"
)

func TestLeaseRuntimeTokenRoundTrip(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	exp := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	tok, err := SignLeaseRuntimeToken(master, "lease-1", "node-9", exp)
	if err != nil {
		t.Fatal(err)
	}
	leaseID, nodeID, gotExp, ok := VerifyLeaseRuntimeToken(master, tok)
	if !ok {
		t.Fatal("verify failed")
	}
	if leaseID != "lease-1" || nodeID != "node-9" || !gotExp.Equal(exp) {
		t.Fatalf("binding mismatch: %s %s %v", leaseID, nodeID, gotExp)
	}

	// Wrong key, tampered token and bad prefix all fail.
	if _, _, _, ok := VerifyLeaseRuntimeToken(bytes.Repeat([]byte("x"), 32), tok); ok {
		t.Fatal("wrong key should fail")
	}
	if _, _, _, ok := VerifyLeaseRuntimeToken(master, tok+"x"); ok {
		t.Fatal("tampered token should fail")
	}
	if _, _, _, ok := VerifyLeaseRuntimeToken(master, "nope_"+tok); ok {
		t.Fatal("bad prefix should fail")
	}
}
