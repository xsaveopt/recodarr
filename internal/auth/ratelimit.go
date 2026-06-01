package auth

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

type loginEntry struct {
	failures int
	nextOK   time.Time
}

const (
	maxEntries = 4096

	retentionAfterReady = 1 * time.Hour
)

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{entries: make(map[string]*loginEntry)}
}

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

func (l *LoginLimiter) sweepLocked() {
	cutoff := time.Now().Add(-retentionAfterReady)
	for k, e := range l.entries {
		if e.nextOK.Before(cutoff) {
			delete(l.entries, k)
		}
	}
}

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
