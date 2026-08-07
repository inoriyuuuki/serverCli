package security

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify should succeed: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong")
	if err != nil || ok {
		t.Fatalf("verify should fail: ok=%v err=%v", ok, err)
	}
	if NeedsRehash(hash) {
		t.Fatal("hash with default params should not need rehash")
	}
}

func TestHashTokenStable(t *testing.T) {
	tok := "abc123"
	h1 := HashToken(tok)
	h2 := HashToken(tok)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not stable: %s %s", h1, h2)
	}
	if Prefix(tok, 3) != "abc" {
		t.Fatal("prefix wrong")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	ip, user := "1.2.3.4", "admin"
	for i := 0; i < 5; i++ {
		if locked := l.RecordFailure(ip, user); locked && i < 4 {
			t.Fatalf("locked too early at %d", i+1)
		}
	}
	if l.Locked(ip, user) <= 0 {
		t.Fatal("expected lock after 5 failures")
	}
	// advance time beyond lock duration
	now = now.Add(6 * time.Minute)
	if l.Locked(ip, user) != 0 {
		t.Fatal("lock should expire")
	}
	l.Reset(ip, user)
	now = now.Add(-6 * time.Minute)
	if l.Locked(ip, user) != 0 {
		t.Fatal("reset should clear bucket")
	}
}
