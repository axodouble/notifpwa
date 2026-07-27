package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is an in-memory per-key token bucket. Buckets are created on
// demand and lazily evicted once idle, so memory stays bounded without a
// background goroutine. The bucket count is additionally hard-capped at
// maxBuckets: a caller that rotates its key (e.g. spoofing X-Forwarded-For)
// cannot grow the map without bound, and reclaiming space only happens on
// the (rarer) path of inserting a new key, keeping the common per-request
// path O(1) instead of an O(n) sweep of the whole map.
type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	burst      float64
	refill     float64 // tokens per second
	idleEvic   time.Duration
	maxBuckets int
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(burst int, refillPerSec float64) *rateLimiter {
	return &rateLimiter{
		buckets:    make(map[string]*bucket),
		burst:      float64(burst),
		refill:     refillPerSec,
		idleEvic:   10 * time.Minute,
		maxBuckets: 50000,
	}
}

// allow reports whether a request for key is permitted at time now, consuming
// one token if so.
func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.buckets[key]
	if !ok {
		r.makeRoom(now)
		b = &bucket{tokens: r.burst, last: now}
		r.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		b.tokens = min(r.burst, b.tokens+elapsed*r.refill)
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// makeRoom reclaims space for a new bucket when the map is at or over
// maxBuckets. It first evicts idle-expired entries; if that isn't enough it
// evicts the single oldest (by last-seen) entry. Callers must hold r.mu.
func (r *rateLimiter) makeRoom(now time.Time) {
	if r.maxBuckets <= 0 || len(r.buckets) < r.maxBuckets {
		return
	}

	for k, b := range r.buckets {
		if now.Sub(b.last) > r.idleEvic {
			delete(r.buckets, k)
		}
	}

	if len(r.buckets) < r.maxBuckets {
		return
	}

	var oldestKey string
	var oldestLast time.Time
	first := true
	for k, b := range r.buckets {
		if first || b.last.Before(oldestLast) {
			oldestKey = k
			oldestLast = b.last
			first = false
		}
	}
	if !first {
		delete(r.buckets, oldestKey)
	}
}

// clientIP returns the caller's IP: the first hop of X-Forwarded-For if
// present, otherwise the host portion of RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimit wraps next, rejecting requests over the per-IP rate with 429.
func (s *Server) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(clientIP(r), time.Now()) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}
