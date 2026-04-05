//go:build windows

package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestRateLimiter_AllowsWithinBurst verifies that up to cap requests succeed.
func TestRateLimiter_AllowsWithinBurst(t *testing.T) {
	rl := newIPRateLimiter(1, 5) // 1 token/sec, burst 5
	for i := range 5 {
		if ok, _ := rl.Allow("1.2.3.4"); !ok {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
}

// TestRateLimiter_RejectsAfterBurst verifies that requests beyond cap are denied.
func TestRateLimiter_RejectsAfterBurst(t *testing.T) {
	rl := newIPRateLimiter(1, 3) // burst of 3
	for range 3 {
		rl.Allow("10.0.0.1")
	}
	if ok, _ := rl.Allow("10.0.0.1"); ok {
		t.Fatal("request beyond burst should be denied")
	}
}

// TestRateLimiter_IndependentIPs verifies that different IPs have independent buckets.
func TestRateLimiter_IndependentIPs(t *testing.T) {
	rl := newIPRateLimiter(1, 2) // burst 2
	for range 2 {
		rl.Allow("192.168.1.1")
	}
	// 192.168.1.1 is exhausted; 192.168.1.2 should still have tokens.
	if ok, _ := rl.Allow("192.168.1.2"); !ok {
		t.Fatal("different IP should have independent token bucket")
	}
	if ok, _ := rl.Allow("192.168.1.1"); ok {
		t.Fatal("exhausted IP should be denied")
	}
}

// TestRateLimiter_TokensRefill verifies that tokens refill after time passes.
func TestRateLimiter_TokensRefill(t *testing.T) {
	rl := newIPRateLimiter(100, 1) // 100 tokens/sec, burst 1
	// Drain the bucket.
	rl.Allow("5.5.5.5")
	if ok, _ := rl.Allow("5.5.5.5"); ok {
		t.Fatal("burst exhausted — should be denied before refill")
	}
	// Backdating lastSeen simulates time passing (10 ms → +1 token at 100/s).
	rl.mu.Lock()
	rl.buckets["5.5.5.5"].lastSeen = time.Now().Add(-10 * time.Millisecond)
	rl.mu.Unlock()
	if ok, _ := rl.Allow("5.5.5.5"); !ok {
		t.Fatal("should be allowed after tokens refill")
	}
}

// TestRateLimiter_TokensRefillClampedToCap verifies that token refill is capped
// at the bucket capacity. When enough time has elapsed that the refill would
// push tokens above cap, the bucket is clamped to cap.
func TestRateLimiter_TokensRefillClampedToCap(t *testing.T) {
	rl := newIPRateLimiter(10, 5) // 10 tokens/sec, burst cap 5

	// Consume one token to create a non-full bucket.
	if ok, _ := rl.Allow("9.9.9.9"); !ok {
		t.Fatal("first request should be allowed")
	}

	// Backdate lastSeen by 10 seconds — at 10 tokens/sec that would add 100
	// tokens, far above the cap of 5. The Allow call must clamp to 5.
	rl.mu.Lock()
	rl.buckets["9.9.9.9"].lastSeen = time.Now().Add(-10 * time.Second)
	rl.mu.Unlock()

	// Drain exactly cap (5) tokens — if clamping works, all 5 succeed.
	for i := 0; i < 5; i++ {
		if ok, _ := rl.Allow("9.9.9.9"); !ok {
			t.Fatalf("request %d/%d should be allowed (bucket should be at cap)", i+1, 5)
		}
	}
	// The 6th request must be denied — bucket should be empty, not overflowed.
	if ok, _ := rl.Allow("9.9.9.9"); ok {
		t.Error("6th request should be denied — tokens should have been clamped to cap")
	}
}

// TestRateLimiter_Middleware_Allows verifies the middleware passes allowed requests.
func TestRateLimiter_Middleware_Allows(t *testing.T) {
	rl := newIPRateLimiter(10, 10)
	reached := false
	handler := rateLimitMiddleware(rl, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:55000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !reached {
		t.Fatal("handler should have been reached")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// TestRateLimiter_Middleware_Rejects verifies the middleware returns 429 when exhausted.
func TestRateLimiter_Middleware_Rejects(t *testing.T) {
	rl := newIPRateLimiter(1, 0) // cap=0 so every request fails immediately
	handler := rateLimitMiddleware(rl, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "203.0.113.1:44000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
}

// TestRateLimiter_Middleware_Rejects_SetsRetryAfterHeader verifies that 429 responses
// include a Retry-After header with a positive-integer delay (RFC 7231 SHOULD).
func TestRateLimiter_Middleware_Rejects_SetsRetryAfterHeader(t *testing.T) {
	rl := newIPRateLimiter(1, 0) // cap=0 so every request fails immediately
	handler := rateLimitMiddleware(rl, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "203.0.113.1:44000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	ra := rr.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("missing Retry-After header on 429 response")
	}
	n, err := strconv.Atoi(ra)
	if err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want positive integer seconds", ra)
	}
}

// TestRateLimiter_StaleBucketPruned verifies that a bucket not accessed for
// longer than pruneAfter is removed from the map on the next Allow call.
func TestRateLimiter_StaleBucketPruned(t *testing.T) {
	rl := newIPRateLimiter(10, 10)

	// Seed a bucket for IP "1.2.3.4".
	rl.Allow("1.2.3.4")

	// Age the bucket past the 5-minute prune window.
	rl.mu.Lock()
	rl.buckets["1.2.3.4"].lastSeen = time.Now().Add(-10 * time.Minute)
	rl.mu.Unlock()

	// Trigger pruning via an Allow call from a different IP.
	rl.Allow("5.6.7.8")

	rl.mu.Lock()
	_, exists := rl.buckets["1.2.3.4"]
	rl.mu.Unlock()

	if exists {
		t.Error("stale bucket for 1.2.3.4 was not pruned after 10 minutes of inactivity")
	}
}

// TestRateLimiter_Middleware_BadRemoteAddr verifies graceful handling of malformed RemoteAddr.
func TestRateLimiter_Middleware_BadRemoteAddr(t *testing.T) {
	rl := newIPRateLimiter(10, 10)
	handler := rateLimitMiddleware(rl, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "malformed" // no port
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Should not panic; falls back to full RemoteAddr as key.
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with fallback IP, got %d", rr.Code)
	}
}
