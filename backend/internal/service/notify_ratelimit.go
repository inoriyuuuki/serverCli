package service

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// tokenBucket is a classic token bucket with continuous refill. Capacity is
// the per-minute allowance; refill happens lazily on every acquisition based
// on elapsed wall time.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// refill adds tokens proportionally to the time elapsed since last and caps
// the balance at the bucket capacity (which equals the per-minute rate).
func (b *tokenBucket) refill(ratePerSec, capacity float64, now time.Time) {
	if b.last.IsZero() {
		b.tokens = capacity
		b.last = now
		return
	}
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() * ratePerSec
		if b.tokens > capacity {
			b.tokens = capacity
		}
	}
	b.last = now
}

// retryAfterSeconds returns the time needed to refill one full token (ceil to
// whole seconds, minimum 1s). Caller must hold the limiter lock.
func retryAfterSeconds(tokens, ratePerSec float64) time.Duration {
	need := 1 - tokens
	secs := need / ratePerSec
	secs = math.Ceil(secs)
	if secs < 1 {
		secs = 1
	}
	return time.Duration(secs) * time.Second
}

// tokenBucketIdle is how long a per-token bucket may stay unused before
// Cleanup removes it.
const tokenBucketIdle = 10 * time.Minute

// NotificationLimiter is an in-process, concurrency-safe token bucket limiter
// for notifications. Every access token gets its own bucket (keyed by token
// ID); a shared global bucket caps total throughput. External API requests
// atomically acquire both the per-token and the global quota (TryAcquire);
// internal callers only consume the global quota (TryAcquireGlobal).
type NotificationLimiter struct {
	mu                sync.Mutex
	perTokenPerMinute float64
	globalPerMinute   float64
	buckets           map[string]*tokenBucket
	global            *tokenBucket
}

// NewNotificationLimiter builds a limiter with the given per-token and global
// per-minute allowances. Non-positive rates panic: the config loader already
// validates them, so this only guards against programming errors.
func NewNotificationLimiter(perTokenPerMinute, globalPerMinute int) *NotificationLimiter {
	if perTokenPerMinute <= 0 {
		panic(fmt.Sprintf("notify: per-token rate must be positive, got %d", perTokenPerMinute))
	}
	if globalPerMinute <= 0 {
		panic(fmt.Sprintf("notify: global rate must be positive, got %d", globalPerMinute))
	}
	now := time.Now()
	return &NotificationLimiter{
		perTokenPerMinute: float64(perTokenPerMinute),
		globalPerMinute:   float64(globalPerMinute),
		buckets:           make(map[string]*tokenBucket),
		global:            &tokenBucket{tokens: float64(globalPerMinute), last: now},
	}
}

// TryAcquire atomically acquires one token from both the per-token bucket for
// tokenID and the global bucket. If either bucket is empty, neither is
// debited and ok=false; retryAfter is the time until the failing bucket can
// serve a request again. ok=true means both were debited.
func (l *NotificationLimiter) TryAcquire(tokenID string) (retryAfter time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	tokenRate := l.perTokenPerMinute / 60.0
	globalRate := l.globalPerMinute / 60.0

	b := l.buckets[tokenID]
	if b == nil {
		b = &tokenBucket{}
		l.buckets[tokenID] = b
	}
	b.refill(tokenRate, l.perTokenPerMinute, now)
	if b.tokens < 1 {
		return retryAfterSeconds(b.tokens, tokenRate), false
	}

	l.global.refill(globalRate, l.globalPerMinute, now)
	if l.global.tokens < 1 {
		// Global is exhausted: leave the per-token bucket untouched so the
		// caller only ever observes an all-or-nothing acquisition.
		return retryAfterSeconds(l.global.tokens, globalRate), false
	}

	b.tokens--
	l.global.tokens--
	return 0, true
}

// TryAcquireGlobal acquires one token from the global bucket only. It is used
// by internal notification senders (e.g. the task scheduler) that have no
// per-token identity.
func (l *NotificationLimiter) TryAcquireGlobal() (retryAfter time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	globalRate := l.globalPerMinute / 60.0
	l.global.refill(globalRate, l.globalPerMinute, now)
	if l.global.tokens < 1 {
		return retryAfterSeconds(l.global.tokens, globalRate), false
	}
	l.global.tokens--
	return 0, true
}

// Cleanup removes per-token buckets that have been idle for more than
// tokenBucketIdle. It is safe to call concurrently with TryAcquire.
func (l *NotificationLimiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-tokenBucketIdle)
	for id, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, id)
		}
	}
}
