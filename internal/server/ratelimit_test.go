package server

import (
	"testing"
	"time"
)

func TestRateLimiterBurstThenBlock(t *testing.T) {
	rl := newRateLimiter(2, 1) // burst 2, refill 1/sec
	base := time.Unix(1000, 0)

	if !rl.allow("ip", base) {
		t.Fatal("1st request should be allowed")
	}
	if !rl.allow("ip", base) {
		t.Fatal("2nd request should be allowed (burst)")
	}
	if rl.allow("ip", base) {
		t.Fatal("3rd request should be blocked")
	}
	// After ~1s, one token refills.
	if !rl.allow("ip", base.Add(1100*time.Millisecond)) {
		t.Fatal("request after refill should be allowed")
	}
}

func TestRateLimiterIsolatesKeys(t *testing.T) {
	rl := newRateLimiter(1, 1)
	base := time.Unix(1000, 0)
	if !rl.allow("a", base) {
		t.Fatal("key a first request allowed")
	}
	if !rl.allow("b", base) {
		t.Fatal("key b is independent and should be allowed")
	}
}

func TestRateLimiterBoundsBucketCount(t *testing.T) {
	rl := newRateLimiter(2, 1)
	rl.maxBuckets = 3
	base := time.Unix(1000, 0)

	for i := 0; i < 20; i++ {
		key := string(rune('a' + i))
		now := base.Add(time.Duration(i) * time.Millisecond)
		rl.allow(key, now)
	}

	rl.mu.Lock()
	n := len(rl.buckets)
	rl.mu.Unlock()

	if n > rl.maxBuckets {
		t.Fatalf("len(rl.buckets) = %d, want <= %d", n, rl.maxBuckets)
	}
}
