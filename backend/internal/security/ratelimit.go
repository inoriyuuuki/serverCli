package security

import (
	"sync"
	"time"
)

// LoginLimiter enforces per (IP, username) lockout: after maxFailures failed
// attempts within the window, login is blocked for lockDuration.
type LoginLimiter struct {
	mu           sync.Mutex
	buckets      map[string]*loginBucket
	maxFailures  int
	window       time.Duration
	lockDuration time.Duration
	now          func() time.Time
}

type loginBucket struct {
	failures    []time.Time
	lockedUntil time.Time
}

// NewLoginLimiter returns a limiter with the default contract policy
// (5 failures locks for 5 minutes).
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		buckets:      make(map[string]*loginBucket),
		maxFailures:  5,
		window:       5 * time.Minute,
		lockDuration: 5 * time.Minute,
		now:          time.Now,
	}
}

func (l *LoginLimiter) key(ip, username string) string {
	return ip + "\x00" + username
}

// Locked returns the remaining lock time (0 if not locked).
func (l *LoginLimiter) Locked(ip, username string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[l.key(ip, username)]
	if b == nil {
		return 0
	}
	now := l.now()
	if b.lockedUntil.After(now) {
		return b.lockedUntil.Sub(now)
	}
	return 0
}

// RecordFailure registers a failed attempt. It returns true when the bucket
// just became locked.
func (l *LoginLimiter) RecordFailure(ip, username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := l.key(ip, username)
	b := l.buckets[key]
	if b == nil {
		b = &loginBucket{}
		l.buckets[key] = b
	}
	if b.lockedUntil.After(now) {
		return false
	}
	// Drop failures older than the window.
	cutoff := now.Add(-l.window)
	kept := b.failures[:0]
	for _, f := range b.failures {
		if f.After(cutoff) {
			kept = append(kept, f)
		}
	}
	b.failures = append(kept, now)
	if len(b.failures) >= l.maxFailures {
		b.lockedUntil = now.Add(l.lockDuration)
		b.failures = nil
		return true
	}
	return false
}

// Reset clears the bucket for a successful login.
func (l *LoginLimiter) Reset(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, l.key(ip, username))
}

// Cleanup removes buckets that have been idle beyond the lock duration.
func (l *LoginLimiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if !b.lockedUntil.After(now) && (len(b.failures) == 0 || b.failures[len(b.failures)-1].Before(now.Add(-l.window))) {
			delete(l.buckets, k)
		}
	}
}
