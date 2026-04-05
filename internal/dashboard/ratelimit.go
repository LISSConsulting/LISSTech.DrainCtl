//go:build windows

package dashboard

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ipRateLimiter enforces a token-bucket rate limit per client IP address.
// Each IP gets a burst capacity of cap tokens; tokens refill at rate per second.
// Stale entries (no request in 5 minutes) are pruned lazily on every Allow call.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rlBucket
	rate    float64 // tokens added per second
	cap     float64 // maximum token capacity (burst)
}

type rlBucket struct {
	tokens   float64
	lastSeen time.Time
}

// newIPRateLimiter creates a rate limiter allowing rate tokens/second per IP
// with a burst capacity of cap requests.
func newIPRateLimiter(rate, cap float64) *ipRateLimiter {
	return &ipRateLimiter{
		buckets: make(map[string]*rlBucket),
		rate:    rate,
		cap:     cap,
	}
}

// Allow returns true if the given IP may proceed, consuming one token.
// It refills tokens proportional to elapsed time since the last call.
func (rl *ipRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &rlBucket{tokens: rl.cap, lastSeen: now}
		rl.buckets[ip] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.cap {
		b.tokens = rl.cap
	}
	b.lastSeen = now

	// Prune IPs that haven't made a request in 5 minutes.
	const pruneAfter = 5 * time.Minute
	for addr, bkt := range rl.buckets {
		if addr != ip && now.Sub(bkt.lastSeen) > pruneAfter {
			delete(rl.buckets, addr)
		}
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// rateLimitMiddleware wraps next and returns 429 Too Many Requests when the
// client IP exceeds the configured rate. The IP is extracted from RemoteAddr.
func rateLimitMiddleware(rl *ipRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !rl.Allow(ip) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
