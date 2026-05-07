//go:build windows

// Package debugpprof exposes the runtime/pprof debug HTTP server as an
// LCI subsystem. Off by default — enable by setting DRAINCTL_PPROF_PORT
// to a TCP port (1-65535). Loopback-only. Used for memory-leak and
// goroutine-leak diagnosis on production boxes without rebuilding:
//
//	go tool pprof http://127.0.0.1:<port>/debug/pprof/heap
//	go tool pprof http://127.0.0.1:<port>/debug/pprof/goroutine
//
// Operators without `go` installed can curl the endpoints directly:
//
//	curl -o heap.pprof http://127.0.0.1:<port>/debug/pprof/heap
//
// Gating:
//   - Off by default. Operator must set EnvVar to enable.
//   - Listener binds 127.0.0.1 only — never reachable from another host
//     even by accident. The dashboard's external listener is unaffected.
//   - The pprof endpoints are unauthenticated. The loopback binding is the
//     sole gate. Anyone with code-execution on the box already has
//     equivalent access to drainctld's process state, so adding session
//     auth would be ceremony with no protection delta.
//
// See docs/architecture/lifecycle.md for the LCI rules this subsystem
// follows.
package debugpprof

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// EnvVar is the environment variable that gates the pprof debug server.
// Empty / unset = disabled. Set to a TCP port (1-65535) to enable.
const EnvVar = "DRAINCTL_PPROF_PORT"

// shutdownTimeout bounds the graceful drain on Stop so a wedged pprof
// connection cannot stall service shutdown past the SCM wait hint.
const shutdownTimeout = 2 * time.Second

// Subsystem owns the pprof HTTP server. Construct via New, register
// alongside other lifecycle.Subsystems, call Start with a service-scoped
// ctx, call Stop on shutdown. Mirrors the selfmetrics / evtspike /
// updater Subsystem pattern.
type Subsystem struct {
	port int // 0 means disabled; New keeps it 0 when EnvVar is unset/invalid

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
	srv      *http.Server
}

// New reads EnvVar and returns a Subsystem that will bind a loopback
// listener on that port when Start is called. An unset or invalid env
// var produces a disabled Subsystem whose Start is a no-op.
func New() *Subsystem {
	raw := os.Getenv(EnvVar)
	if raw == "" {
		return &Subsystem{}
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		// G706 false positive: slog passes attribute values through
		// the handler's structured-field encoder which escapes control
		// characters (including newlines) — the env-var value cannot
		// inject a fake log line.
		//nolint:gosec // G706: log injection — false positive, slog escapes structured field values
		slog.Warn("pprof: invalid port; debug server not started",
			"env", EnvVar, "value", raw, "error", err)
		return &Subsystem{}
	}
	return &Subsystem{port: port}
}

// Start binds the loopback listener and launches the HTTP server. When
// the Subsystem is disabled (port == 0) Start returns nil without
// launching any goroutines. A bind failure logs Warn and returns nil —
// service startup must not depend on pprof being available.
func (s *Subsystem) Start(ctx context.Context) error {
	if s.port == 0 {
		return nil
	}
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("pprof: bind failed; debug server not started",
			"addr", addr, "error", err)
		return nil
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

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Warn("pprof: debug server enabled — loopback only; intended for short-lived diagnosis",
		"addr", addr,
		"unset_to_disable", EnvVar)

	derived, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof: server failed", "error", err)
		}
	}()
	go func() {
		defer s.wg.Done()
		<-derived.Done()
		shutdownCtx, shCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shCancel()
		_ = s.srv.Shutdown(shutdownCtx)
	}()
	return nil
}

// Stop cancels the derived ctx (which fires the shutdown goroutine) and
// blocks until both internal goroutines have exited. Idempotent; safe to
// call when the Subsystem was disabled or Start was never invoked.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}
