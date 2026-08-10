package service

import (
	"testing"
	"time"
)

func TestLimiterPerTokenBucketsIndependent(t *testing.T) {
	l := NewNotificationLimiter(1, 1000)
	if _, ok := l.TryAcquire("A"); !ok {
		t.Fatal("first A should succeed")
	}
	if _, ok := l.TryAcquire("A"); ok {
		t.Fatal("second A should be limited")
	}
	if _, ok := l.TryAcquire("B"); !ok {
		t.Fatal("B should have its own bucket and succeed")
	}
	if _, ok := l.TryAcquire("B"); ok {
		t.Fatal("second B should be limited")
	}
}

func TestLimiterGlobalShared(t *testing.T) {
	l := NewNotificationLimiter(1000, 1)
	if _, ok := l.TryAcquire("A"); !ok {
		t.Fatal("first acquire should succeed")
	}
	if _, ok := l.TryAcquire("B"); ok {
		t.Fatal("global bucket exhausted: B should be limited")
	}
	if retry, ok := l.TryAcquireGlobal(); ok {
		t.Fatalf("global bucket exhausted: TryAcquireGlobal should fail, got ok=%v retry=%s", ok, retry)
	}
}

func TestLimiterAtomicNoDoubleDebit(t *testing.T) {
	l := NewNotificationLimiter(1, 1)
	if _, ok := l.TryAcquire("A"); !ok {
		t.Fatal("first acquire should succeed")
	}
	if retry, ok := l.TryAcquire("B"); ok {
		t.Fatalf("B should fail on global, got ok=%v", ok)
	} else if retry <= 0 {
		t.Fatalf("expected positive retryAfter, got %s", retry)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets["B"]; b == nil || b.tokens != 1 {
		t.Fatalf("B bucket must not be debited when global fails, tokens=%v", b)
	}
	if l.global.tokens >= 1 {
		t.Fatalf("global bucket should be empty after A's acquire, tokens=%v", l.global.tokens)
	}
}

func TestLimiterRetryAfterCeilMinOne(t *testing.T) {
	l := NewNotificationLimiter(1, 1000)
	if _, ok := l.TryAcquire("A"); !ok {
		t.Fatal("first acquire should succeed")
	}
	retry, ok := l.TryAcquire("A")
	if ok {
		t.Fatal("second acquire should fail")
	}
	// Refill rate is 1 token per minute; refilling a full token takes 60s.
	if retry < 59*time.Second || retry > 60*time.Second {
		t.Fatalf("retryAfter out of range: %s", retry)
	}
	if retry%time.Second != 0 {
		t.Fatalf("retryAfter should be whole seconds, got %s", retry)
	}
}

func TestLimiterCleanupRemovesIdleBuckets(t *testing.T) {
	l := NewNotificationLimiter(10, 100)
	if _, ok := l.TryAcquire("idle"); !ok {
		t.Fatal("idle acquire failed")
	}
	if _, ok := l.TryAcquire("active"); !ok {
		t.Fatal("active acquire failed")
	}
	l.mu.Lock()
	l.buckets["idle"].last = time.Now().Add(-11 * time.Minute)
	l.mu.Unlock()

	l.Cleanup()

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.buckets["idle"]; ok {
		t.Fatal("idle bucket should have been removed")
	}
	if _, ok := l.buckets["active"]; !ok {
		t.Fatal("recently active bucket should remain")
	}
}

func TestLimiterConstructorPanicsOnNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-positive per-token rate")
		}
	}()
	NewNotificationLimiter(0, 10)
}

func TestLimiterConstructorPanicsOnNonPositiveGlobal(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-positive global rate")
		}
	}()
	NewNotificationLimiter(10, 0)
}
