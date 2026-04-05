//go:build windows

package dashboard

import (
	"fmt"
	"math"
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

// Allow returns (true, 0) if the given IP may proceed, consuming one token.
// It returns (false, retryAfter) when the bucket is exhausted; retryAfter is
// the minimum wait (whole seconds, ≥ 1 s) before the next token arrives.
// Tokens refill proportional to elapsed time since the last call.
func (rl *ipRateLimiter) Allow(ip string) (bool, time.Duration) {
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
	// The current IP's lastSeen was just set to now, so it is never stale here.
	const pruneAfter = 5 * time.Minute
	for addr, bkt := range rl.buckets {
		if now.Sub(bkt.lastSeen) > pruneAfter {
			delete(rl.buckets, addr)
		}
	}

	if b.tokens < 1 {
		// Ceiling of seconds until 1 token is available.
		secs := math.Ceil((1 - b.tokens) / rl.rate)
		return false, time.Duration(int64(secs)) * time.Second
	}
	b.tokens--
	return true, 0
}

// rateLimitMiddleware wraps next and returns 429 Too Many Requests when the
// client IP exceeds the configured rate. The IP is extracted from RemoteAddr.
// A Retry-After header (delay in whole seconds, RFC 7231) is included on 429.
func rateLimitMiddleware(rl *ipRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if ok, wait := rl.Allow(ip); !ok {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
