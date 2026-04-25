//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"time"
)

// pprofEnvVar is the environment variable that enables the runtime/pprof
// debug HTTP server. Empty / unset = disabled. Set to a TCP port number
// (1-65535) to enable; the listener binds 127.0.0.1 only.
const pprofEnvVar = "DRAINCTL_PPROF_PORT"

// startPprof launches the runtime/pprof debug HTTP server on a loopback-only
// listener if pprofEnvVar is set to a positive integer. Used for memory-leak
// and goroutine-leak diagnosis:
//
//	go tool pprof http://127.0.0.1:<port>/debug/pprof/heap
//	go tool pprof http://127.0.0.1:<port>/debug/pprof/goroutine
//
// Operators without `go` installed can curl the endpoints directly:
//
//	curl -o heap.pprof http://127.0.0.1:<port>/debug/pprof/heap
//
// Gating:
//   - Off by default. Operator must set DRAINCTL_PPROF_PORT to enable.
//   - Listener binds 127.0.0.1 only — never reachable from another host
//     even by accident. The dashboard's external listener is unaffected.
//   - The pprof endpoints are unauthenticated. The loopback binding is the
//     sole gate. Anyone with code-execution on the box already has
//     equivalent access to drainctld's process state, so adding session
//     auth would be ceremony with no protection delta.
//
// On any setup failure (bad env var, listener bind, etc.) startPprof logs
// a Warn and returns nil — service startup must not depend on pprof being
// available. The shutdown path is wired off ctx.Done() with a 2-second
// drain timeout.
func startPprof(ctx context.Context) {
	raw := os.Getenv(pprofEnvVar)
	if raw == "" {
		return
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		// G706 false positive: slog passes attribute values through
		// the handler's structured-field encoder which escapes control
		// characters (including newlines) — the env-var value cannot
		// inject a fake log line. The interpolation into a log line
		// only happens at the handler layer where escaping has already
		// been applied.
		//nolint:gosec // G706: log injection — false positive, slog escapes structured field values
		slog.Warn("pprof: invalid port; debug server not started",
			"env", pprofEnvVar, "value", raw, "error", err)
		return
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("pprof: bind failed; debug server not started",
			"addr", addr, "error", err)
		return
	}

	mux := http.NewServeMux()
	// pprof.Index serves the directory page AND dispatches to the named
	// profile handlers (heap, goroutine, allocs, etc.) based on the path
	// suffix, so registering it under "/debug/pprof/" covers most of what
	// operators need. The standalone handlers below cover the few pprof
	// routes Index doesn't dispatch to (cmdline, profile, symbol, trace).
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Warn("pprof: debug server enabled — loopback only; intended for short-lived diagnosis",
		"addr", addr,
		"unset_to_disable", pprofEnvVar)

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof: server failed", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}
