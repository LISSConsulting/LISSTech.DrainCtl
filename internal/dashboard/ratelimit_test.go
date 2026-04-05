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
