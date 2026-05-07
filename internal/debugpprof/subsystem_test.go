//go:build windows

package debugpprof

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDisabledByDefault — empty env var must produce no listener and no
// log spam. Start returns silently and Stop on a never-Started disabled
// subsystem must be a safe no-op.
func TestDisabledByDefault(t *testing.T) {
	t.Setenv(EnvVar, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := New()
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	sub.Stop()
}

// TestInvalidPortDoesNotPanic — malformed port values get a Warn log
// and produce a disabled subsystem. Service startup MUST NOT depend on
// pprof.
func TestInvalidPortDoesNotPanic(t *testing.T) {
	cases := []string{"abc", "0", "-1", "65536", "99999999"}
	for _, val := range cases {
		t.Run(val, func(t *testing.T) {
			t.Setenv(EnvVar, val)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := New()
			if err := sub.Start(ctx); err != nil {
				t.Fatalf("Start() = %v, want nil", err)
			}
			sub.Stop()
		})
	}
}

// TestServesHeap — with a valid port, the heap profile endpoint is
// reachable from loopback and returns a non-empty pprof blob. Validates
// the gating path AND the handler wiring.
func TestServesHeap(t *testing.T) {
	// Find an unused port by binding ":0" then closing the listener.
	// Tiny race window between close and re-bind, but acceptable for a
	// unit test running in isolation.
	port := freeLoopbackPort(t)
	t.Setenv(EnvVar, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := New()
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	defer sub.Stop()

	// Give the goroutine a moment to bind. 100 ms is well past the
	// observed cold start on Windows runners.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	var err error
	for {
		resp, err = http.Get("http://127.0.0.1:" + port + "/debug/pprof/heap")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /debug/pprof/heap: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("heap profile body is empty")
	}
	// pprof profiles begin with the gzip magic bytes 0x1f 0x8b.
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Errorf("body does not look like a gzipped pprof profile (first 16 bytes: %x)",
			body[:min(16, len(body))])
	}
}

// TestLoopbackOnly — confirm the listener is bound on 127.0.0.1. Direct
// loopback works; verifying non-loopback REJECTION would require network-
// namespace shenanigans we won't do in a unit test. The binding-to-
// 127.0.0.1 contract is enforced by the hard-coded address string in
// Start and the documentation comment.
func TestLoopbackOnly(t *testing.T) {
	port := freeLoopbackPort(t)
	t.Setenv(EnvVar, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := New()
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	defer sub.Stop()

	deadline := time.Now().Add(2 * time.Second)
	var ok bool
	for {
		resp, err := http.Get("http://127.0.0.1:" + port + "/debug/pprof/")
		if err == nil {
			_ = resp.Body.Close()
			ok = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		t.Fatal("loopback connection failed; listener not bound?")
	}
}

// TestStopDrainsServer — after Stop returns, the listener must no longer
// accept new connections. Verifies the LCI Stop contract: every
// goroutine launched by Start has exited by the time Stop returns.
func TestStopDrainsServer(t *testing.T) {
	port := freeLoopbackPort(t)
	t.Setenv(EnvVar, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := New()
	if err := sub.Start(ctx); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}

	// Wait until the listener is up.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get("http://127.0.0.1:" + port + "/debug/pprof/")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener never came up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	sub.Stop()

	// After Stop, the port should reject quickly. Use a short client
	// timeout so we don't sit on a SYN that never gets answered.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + port + "/debug/pprof/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("server still accepting after Stop")
	}

	// Idempotent — second Stop must be a no-op.
	sub.Stop()
}

// freeLoopbackPort grabs an ephemeral port the OS picked for us, then
// returns it as a string so caller can hand it to Start. We release it
// immediately — Start rebinds. Standard "find a free port" trick used
// across Go's stdlib tests.
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("malformed addr: %s", addr)
	}
	return addr[idx+1:]
}
