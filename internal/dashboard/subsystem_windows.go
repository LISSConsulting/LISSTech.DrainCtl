//go:build windows

package dashboard

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

// Compile-time assertion: *Subsystem satisfies lifecycle.Subsystem.
var _ lifecycle.Subsystem = (*Subsystem)(nil)

// dashboardShutdownTimeout bounds the graceful HTTP drain on Stop. Matches
// the pre-LCI inline value so behaviour is unchanged.
const dashboardShutdownTimeout = 5 * time.Second

// Subsystem owns the dashboard HTTP server. Construct via NewSubsystem,
// register alongside other lifecycle.Subsystems, call Start with a
// service-scoped ctx, call Stop on shutdown. Mirrors the selfmetrics /
// debugpprof / pipe Subsystem pattern.
//
// Transitively-owned goroutines (SessionStore reaper, NegotiateMiddleware
// SSPI reaper) are started by their constructors with the derived ctx and
// exit on Stop's cancel. They are not joined to the subsystem's wg — the
// http.Server drain (which Stop blocks on) is the externally observable
// boundary, and the reapers are constructor-internal cleanup loops.
type Subsystem struct {
	cfg     dc.DashboardConfig
	dataDir string
	ms      metricsReader
	as      auditReader
	mnt     maintenanceReader
	srv     serverReader
	sps     eventSpikeReader

	ds    *DashboardServer
	state *ServerState

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// NewSubsystem constructs the dashboard subsystem. ms/as/mnt may be nil
// (degraded mode); srv and sps are required since feature 009.
func NewSubsystem(cfg dc.DashboardConfig, dataDir string, ms metricsReader, as auditReader, mnt maintenanceReader, srv serverReader, sps eventSpikeReader) *Subsystem {
	return &Subsystem{
		cfg:     cfg,
		dataDir: dataDir,
		ms:      ms,
		as:      as,
		mnt:     mnt,
		srv:     srv,
		sps:     sps,
	}
}

// State returns the ServerState created by Start so the service main loop
// can call Register / wire OnEvtSpike* callbacks. Nil before a successful
// Start.
func (s *Subsystem) State() *ServerState { return s.state }

// Server returns the underlying *DashboardServer for callers that need to
// reach the broker or session store. Nil before a successful Start.
func (s *Subsystem) Server() *DashboardServer { return s.ds }

// Start performs the legacy-servers.json migration, sets up the HTTP
// server (TLS, routes, listener) synchronously, then launches the Serve
// + shutdown-drain goroutines tied to a derived context. A synchronous
// failure (TLS load, bind) returns the error before any goroutine
// launches; the caller drops the subsystem and continues without
// dashboard.
func (s *Subsystem) Start(ctx context.Context) error {
	// One-shot import of any legacy servers.json produced by a pre-009 binary.
	// Renames the file so the import is idempotent across reboots.
	if err := MigrateLegacyServersJSON(ctx, s.dataDir, s.srv); err != nil {
		slog.Warn("dashboard: legacy servers.json migration failed", "error", err)
	}

	derived, cancel := context.WithCancel(ctx)

	state := NewServerState(s.srv)

	ds := &DashboardServer{
		state:                state,
		cfg:                  s.cfg,
		sessionStore:         NewSessionStore(derived),
		broker:               NewBroker(),
		ms:                   s.ms,
		as:                   s.as,
		mnt:                  s.mnt,
		spikes:               s.sps,
		remoteEvtSpikeStatus: make(map[string]evtspike.DetectorStatus),
	}
	ds.wireServerStateCallbacks()

	mux := http.NewServeMux()
	registerRoutes(derived, ds, mux)

	tlsCfg, fingerprint, err := setupTLS(s.cfg, s.dataDir)
	if err != nil {
		cancel()
		return err
	}
	ds.fingerprint = fingerprint

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		cancel()
		return fmt.Errorf("dashboard listen %s: %w", addr, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}

	// Apply security headers to all responses.
	// HSTS is additionally applied when TLS is active.
	handler := securityMiddleware(mux)
	if tlsCfg != nil {
		handler = hstsMiddleware(handler)
	}

	ds.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
		// Route http.Server internal errors (TLS handshake failures, etc.) through
		// slog at DEBUG so they land in the DBG ETW channel (off by default) rather
		// than flowing via log.Default() → slog.LevelInfo → Operational channel.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}

	s.ds = ds
	s.state = state
	s.cancel = cancel

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		slog.Info("dashboard=listening", "addr", addr, "scheme", scheme)
		if err := ds.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("dashboard server error", "error", err)
		}
	}()
	go func() {
		defer s.wg.Done()
		<-derived.Done()
		shutCtx, shCancel := context.WithTimeout(context.Background(), dashboardShutdownTimeout)
		defer shCancel()
		if err := ds.server.Shutdown(shutCtx); err != nil {
			slog.Warn("dashboard shutdown error", "error", err)
		}
		slog.Info("dashboard=stopped")
	}()

	return nil
}

// Stop cancels the derived ctx (which fires the shutdown-drain goroutine)
// and blocks until both internal goroutines have exited. Idempotent; safe
// to call when Start was never invoked or returned an error.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}
