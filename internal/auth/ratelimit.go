package auth

import (
	"sync"
	"time"
)

// LoginLimiter is an in-memory failed-login throttle keyed by client IP. After a
// few failures it makes the caller wait, with the wait growing exponentially.
// Successful logins reset the counter for that key.
//
// Single-admin app, single process: in-memory is fine, no need for Redis.
//
// The map is bounded (maxEntries) and expired entries are swept opportunistically
// on writes so an attacker spraying random IPs can't grow it without bound.
type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

type loginEntry struct {
	failures int
	nextOK   time.Time
}

const (
	// maxEntries caps tracked client IPs. Reaching the cap drops the oldest
	// (lowest nextOK) entry to make room.
	maxEntries = 4096
	// retentionAfterReady is how long an entry sticks around once its backoff
	// expires. Long enough to keep the failure count meaningful for a returning
	// attacker, short enough to bound memory.
	retentionAfterReady = 1 * time.Hour
)

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{entries: make(map[string]*loginEntry)}
}

// Allow reports whether a login attempt from key is permitted right now, and
// returns the time until the next attempt would be allowed (zero if allowed).
func (l *LoginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return true, 0
	}
	if d := time.Until(e.nextOK); d > 0 {
		return false, d
	}
	return true, 0
}

// RegisterFailure records a failed attempt and schedules the next allowed time.
// Backoff: 0,0,0,2s,4s,8s,16s,30s,60s,120s capped at 300s.
func (l *LoginLimiter) RegisterFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		l.sweepLocked()
		if len(l.entries) >= maxEntries {
			l.evictOldestLocked()
		}
		e = &loginEntry{}
		l.entries[key] = e
	}
	e.failures++
	e.nextOK = time.Now().Add(backoffFor(e.failures))
}

// sweepLocked drops entries whose backoff expired more than retentionAfterReady ago.
// Cheap O(n) walk run only on insert, so amortized fine.
func (l *LoginLimiter) sweepLocked() {
	cutoff := time.Now().Add(-retentionAfterReady)
	for k, e := range l.entries {
		if e.nextOK.Before(cutoff) {
			delete(l.entries, k)
		}
	}
}

// evictOldestLocked removes the entry with the smallest nextOK. Called only when
// the map hits maxEntries after a sweep, i.e. a sustained spoofed-IP flood.
func (l *LoginLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for k, e := range l.entries {
		if first || e.nextOK.Before(oldest) {
			oldestKey = k
			oldest = e.nextOK
			first = false
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

// Reset clears the throttle for key (call on successful login).
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func backoffFor(failures int) time.Duration {
	switch {
	case failures <= 3:
		return 0
	case failures == 4:
		return 2 * time.Second
	case failures == 5:
		return 4 * time.Second
	case failures == 6:
		return 8 * time.Second
	case failures == 7:
		return 16 * time.Second
	case failures == 8:
		return 30 * time.Second
	case failures == 9:
		return 60 * time.Second
	case failures == 10:
		return 120 * time.Second
	default:
		return 300 * time.Second
	}
}
