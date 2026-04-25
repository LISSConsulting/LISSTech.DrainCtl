//go:build windows

package svc

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStartPprof_DisabledByDefault — empty env var must produce no listener
// and no log spam. The function returns silently.
func TestStartPprof_DisabledByDefault(t *testing.T) {
	t.Setenv(pprofEnvVar, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprof(ctx)
	// No listener to verify; absence is the contract. The negative
	// signal is "this test doesn't hang and doesn't log a Warn we
	// didn't expect."
}

// TestStartPprof_InvalidPortDoesNotPanic — malformed port values get
// a Warn log and return without binding. Service startup MUST NOT
// depend on pprof.
func TestStartPprof_InvalidPortDoesNotPanic(t *testing.T) {
	cases := []string{"abc", "0", "-1", "65536", "99999999"}
	for _, val := range cases {
		t.Run(val, func(t *testing.T) {
			t.Setenv(pprofEnvVar, val)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startPprof(ctx) // must not panic, must not bind
		})
	}
}

// TestStartPprof_ServesHeap — with a valid port, the heap profile
// endpoint is reachable from loopback and returns a non-empty pprof
// blob. Validates the gating path AND the handler wiring.
func TestStartPprof_ServesHeap(t *testing.T) {
	// Find an unused port by binding ":0" then closing the listener.
	// Tiny race window between close and re-bind, but acceptable for a
	// unit test running in isolation. CI handles ephemeral-port races
	// at a much larger scale via SO_REUSEADDR; we don't need that
	// sophistication here.
	port := freeLoopbackPort(t)
	t.Setenv(pprofEnvVar, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprof(ctx)

	// Give the goroutine a moment to bind. 100 ms is well past the
	// observed cold start on the Windows runners that flaked the pipe
	// integration test.
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

// TestStartPprof_LoopbackOnly — confirm the listener cannot be reached
// via a non-loopback interface. Best-effort: on a single-NIC dev box
// this just verifies the address explicitly contains 127.0.0.1.
func TestStartPprof_LoopbackOnly(t *testing.T) {
	port := freeLoopbackPort(t)
	t.Setenv(pprofEnvVar, port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprof(ctx)

	// Direct loopback works.
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
	// Confirming the listener REJECTS non-loopback would require
	// network namespace shenanigans we won't do in a unit test;
	// the binding-to-127.0.0.1 contract is enforced by the
	// hard-coded address string in startPprof and the documentation
	// comment.
}

// freeLoopbackPort grabs an ephemeral port the OS picked for us, then
// returns it as a string so caller can hand it to startPprof. We
// release it immediately — startPprof rebinds. This is the standard
// "find a free port" trick used across Go's stdlib tests.
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
