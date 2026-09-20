package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBackoffForIsMonotonicAndFreeForThreeTries(t *testing.T) {
	for f := range 4 {
		if d := backoffFor(f); d != 0 {
			t.Fatalf("backoffFor(%d) = %v, want no delay for the first three failures", f, d)
		}
	}
	prev := time.Duration(0)
	for f := 4; f <= 11; f++ {
		d := backoffFor(f)
		if d <= prev {
			t.Fatalf("backoffFor(%d) = %v, which is not longer than %v", f, d, prev)
		}
		prev = d
	}
	if got := backoffFor(11); got != 300*time.Second {
		t.Fatalf("got %v past the table, want the 300s ceiling", got)
	}
	if got := backoffFor(1000); got != 300*time.Second {
		t.Fatalf("got %v, want the backoff to cap at 300s", got)
	}
}

func TestAllowIsOpenForAnUnknownKey(t *testing.T) {
	l := NewLoginLimiter()
	ok, retry := l.Allow("10.0.0.1")
	if !ok || retry != 0 {
		t.Fatalf("got (%v, %v), want an unknown client to be allowed", ok, retry)
	}
}

func TestThreeFailuresDoNotThrottle(t *testing.T) {
	l := NewLoginLimiter()
	for range 3 {
		l.RegisterFailure("10.0.0.1")
		if ok, _ := l.Allow("10.0.0.1"); !ok {
			t.Fatal("throttled inside the free allowance")
		}
	}
}

func TestFourthFailureThrottles(t *testing.T) {
	l := NewLoginLimiter()
	for range 4 {
		l.RegisterFailure("10.0.0.1")
	}
	ok, retry := l.Allow("10.0.0.1")
	if ok {
		t.Fatal("the fourth failure did not throttle")
	}
	if retry <= 0 || retry > 2*time.Second {
		t.Fatalf("got retry-after %v, want it just under 2s", retry)
	}
	if ok, _ := l.Allow("10.0.0.2"); !ok {
		t.Fatal("throttling one client also throttled another")
	}
}

func TestBackoffGrowsWithFailures(t *testing.T) {
	l := NewLoginLimiter()
	for range 4 {
		l.RegisterFailure("10.0.0.1")
	}
	_, first := l.Allow("10.0.0.1")
	l.RegisterFailure("10.0.0.1")
	_, second := l.Allow("10.0.0.1")
	if second <= first {
		t.Fatalf("got %v then %v, want the wait to grow", first, second)
	}
}

func TestResetClearsTheThrottle(t *testing.T) {
	l := NewLoginLimiter()
	for range 6 {
		l.RegisterFailure("10.0.0.1")
	}
	if ok, _ := l.Allow("10.0.0.1"); ok {
		t.Fatal("expected the client to be throttled before the reset")
	}
	l.Reset("10.0.0.1")
	if ok, retry := l.Allow("10.0.0.1"); !ok || retry != 0 {
		t.Fatalf("got (%v, %v) after a reset, want a clean slate", ok, retry)
	}
	l.RegisterFailure("10.0.0.1")
	if ok, _ := l.Allow("10.0.0.1"); !ok {
		t.Fatal("the failure counter was not reset, so one failure throttled again")
	}
}

func TestExpiredWaitReopensTheClient(t *testing.T) {
	l := NewLoginLimiter()
	for range 5 {
		l.RegisterFailure("10.0.0.1")
	}
	if ok, _ := l.Allow("10.0.0.1"); ok {
		t.Fatal("expected a throttle")
	}
	l.mu.Lock()
	l.entries["10.0.0.1"].nextOK = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if ok, retry := l.Allow("10.0.0.1"); !ok || retry != 0 {
		t.Fatalf("got (%v, %v), want the client allowed once the wait elapsed", ok, retry)
	}
}

func TestEntriesAreCappedAndOldOnesSweptAway(t *testing.T) {
	l := NewLoginLimiter()
	l.RegisterFailure("stale")
	l.mu.Lock()
	l.entries["stale"].nextOK = time.Now().Add(-2 * retentionAfterReady)
	l.mu.Unlock()

	l.RegisterFailure("fresh")

	l.mu.Lock()
	_, staleStillThere := l.entries["stale"]
	_, freshThere := l.entries["fresh"]
	l.mu.Unlock()
	if staleStillThere {
		t.Fatal("a long-idle entry was not swept")
	}
	if !freshThere {
		t.Fatal("the new entry was not recorded")
	}
}

func TestEntryCountStaysUnderTheCap(t *testing.T) {
	l := NewLoginLimiter()
	for i := range maxEntries + 200 {
		l.RegisterFailure(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n > maxEntries {
		t.Fatalf("got %d entries, want at most %d", n, maxEntries)
	}
}

func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	l := NewLoginLimiter()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("10.0.0.%d", i%4)
			for range 64 {
				l.RegisterFailure(key)
				l.Allow(key)
				l.Reset(key)
			}
		}(i)
	}
	wg.Wait()
}
