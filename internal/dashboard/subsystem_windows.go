//go:build windows

package dashboard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/investigation"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondrop"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// FeatureRuntime owns additive investigation and session-drop work. The
// subsystem calls its methods in durable restart order before opening the
// dashboard listener. Stop is called after the subsystem context is cancelled.
// Wake requests the same durable inbox drain after a committed accepted report.
type FeatureRuntime interface {
	Recover(context.Context) error
	DrainInbox(context.Context) error
	PruneExpiredQueued(context.Context) error
	StartWorker(context.Context) error
	Stop()
	WakeSessionDropInbox()
}

func startFeatureRuntime(ctx context.Context, runtime FeatureRuntime) error {
	if runtime == nil {
		return nil
	}
	if err := runtime.Recover(ctx); err != nil {
		return fmt.Errorf("recovery: %w", err)
	}
	if err := runtime.DrainInbox(ctx); err != nil {
		return fmt.Errorf("startup inbox drain: %w", err)
	}
	if err := runtime.PruneExpiredQueued(ctx); err != nil {
		return fmt.Errorf("queued attempt prune: %w", err)
	}
	if err := runtime.StartWorker(ctx); err != nil {
		return fmt.Errorf("worker start: %w", err)
	}
	return nil
}

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
// exit on Stop's cancel. The HTTP drain, RD Session Collection resolver, and
// stale-host transition worker are joined to the subsystem's wg; the reapers
// remain constructor-internal cleanup loops.
type Subsystem struct {
	cfg                       dc.DashboardConfig
	dataDir                   string
	ms                        metricsReader
	as                        auditReader
	mnt                       maintenanceReader
	srv                       serverReader
	sps                       eventSpikeReader
	removals                  removalWriter
	outbox                    *telemetry.ForceUpdateOutboxStore
	sessionSnapshots          sessionSnapshotWriter
	sessionQueries            sessionQueryReader
	sessionActions            sessionActionStore
	freshness                 *telemetry.FreshnessStore
	localForceUpdateSupported bool
	featureRuntime            FeatureRuntime
	rdCollections             *rdCollectionResolver

	ds    *DashboardServer
	state *ServerState

	wg       sync.WaitGroup
	cancel   context.CancelFunc
	stopOnce sync.Once
}

// SubsystemDependencies is the complete dashboard dependency set. Concrete
// telemetry stores make every ownership edge explicit at construction time.
type SubsystemDependencies struct {
	Metrics                   *telemetry.MetricsStore
	Audit                     *telemetry.AuditStore
	Maintenance               *telemetry.MaintenanceStore
	Servers                   *telemetry.ServerStore
	EventSpikes               *telemetry.EventSpikeStore
	Removals                  *telemetry.RemovalStore
	ForceUpdateOutbox         *telemetry.ForceUpdateOutboxStore
	SessionSnapshots          *telemetry.SessionSnapshotStore
	SessionQueries            *telemetry.SessionQueryStore
	SessionActions            *telemetry.SessionActionStore
	Freshness                 *telemetry.FreshnessStore
	LocalForceUpdateSupported bool
	FeatureRuntime            FeatureRuntime
}

// NewSubsystem constructs the dashboard subsystem. Nil stores explicitly
// select degraded endpoint behavior, which is safe during rolling upgrades.
func NewSubsystem(cfg dc.DashboardConfig, dataDir string, dependencies SubsystemDependencies) *Subsystem {
	subsystem := &Subsystem{
		cfg:                       cfg,
		dataDir:                   dataDir,
		outbox:                    dependencies.ForceUpdateOutbox,
		freshness:                 dependencies.Freshness,
		localForceUpdateSupported: dependencies.LocalForceUpdateSupported,
		featureRuntime:            dependencies.FeatureRuntime,
	}
	// Assign concrete stores to interfaces only when non-nil. Assigning a
	// typed nil pointer directly would produce a non-nil interface and panic
	// when degraded-mode workers call it.
	if dependencies.Metrics != nil {
		subsystem.ms = dependencies.Metrics
	}
	if dependencies.Audit != nil {
		subsystem.as = dependencies.Audit
	}
	if dependencies.Maintenance != nil {
		subsystem.mnt = dependencies.Maintenance
	}
	if dependencies.Servers != nil {
		subsystem.srv = dependencies.Servers
	}
	if dependencies.EventSpikes != nil {
		subsystem.sps = dependencies.EventSpikes
	}
	if dependencies.Removals != nil {
		subsystem.removals = dependencies.Removals
	}
	if dependencies.SessionSnapshots != nil {
		subsystem.sessionSnapshots = dependencies.SessionSnapshots
	}
	if dependencies.SessionQueries != nil {
		subsystem.sessionQueries = dependencies.SessionQueries
	}
	if dependencies.SessionActions != nil {
		subsystem.sessionActions = dependencies.SessionActions
	}
	return subsystem
}

// State returns the ServerState created by Start so the service main loop
// can call Register / wire OnEvtSpike* callbacks. Nil before a successful
// Start.
func (s *Subsystem) State() *ServerState { return s.state }

// Server returns the underlying *DashboardServer for callers that need to
// reach the broker or session store. Nil before a successful Start.
func (s *Subsystem) Server() *DashboardServer { return s.ds }

// SetLocalForceUpdateSupported changes eligibility for the local dashboard
// host when dashboard_only is reloaded.
func (s *Subsystem) SetLocalForceUpdateSupported(supported bool) {
	s.localForceUpdateSupported = supported
	if s.ds != nil {
		s.ds.setLocalForceUpdateSupported(supported)
	}
}

// WakeFeatureRuntime prompts an immediate configuration recheck after a
// reload; Worker validates configuration again before any provider egress.
func (s *Subsystem) WakeFeatureRuntime() {
	if s.featureRuntime != nil {
		s.featureRuntime.WakeSessionDropInbox()
	}
}

// UpdateHeartbeatInterval applies a poll-interval change without restarting
// the dashboard listener.
func (s *Subsystem) UpdateHeartbeatInterval(interval time.Duration) {
	if s.ds != nil {
		s.ds.setHeartbeatInterval(interval)
	}
}

// UpdateRDConnectionBroker switches authoritative collection discovery to the
// configured broker and triggers an immediate refresh.
func (s *Subsystem) UpdateRDConnectionBroker(broker string) {
	if s.ds != nil && s.ds.rdCollections != nil {
		s.ds.rdCollections.UpdateBroker(broker)
	}
}

// runSessionRetentionOnce removes expired current snapshot attempts using the
// dashboard lifecycle context and telemetry database.
func (ds *DashboardServer) runSessionRetentionOnce(ctx context.Context) error {
	ds.sessionPolicyMu.Lock()
	defer ds.sessionPolicyMu.Unlock()
	return ds.runSessionRetentionOnceLocked(ctx)
}

func (ds *DashboardServer) runSessionRetentionOnceLocked(ctx context.Context) error {
	if ds.sessionSnapshots == nil && ds.sessionActions == nil {
		return nil
	}

	cfg, err := ds.sessionsConfig()
	if err != nil {
		return fmt.Errorf("session retention config: %w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, sessionStoreTimeout)
	defer cancel()

	if ds.sessionSnapshots != nil {
		if _, err := ds.sessionSnapshots.PurgeSnapshots(opCtx, cfg.RetentionHours, ds.clock()); err != nil {
			return err
		}
	}
	if ds.sessionActions == nil {
		return nil
	}

	now := ds.clock()
	statuses, err := ds.sessionActions.Expire(opCtx, now)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		ds.broadcastSessionAction(status)
	}
	_, _, err = ds.sessionActions.Retain(opCtx, now.Add(-time.Duration(cfg.RetentionHours)*time.Hour), now.Add(-time.Duration(cfg.RetentionHours)*time.Hour), 1000)
	return err
}

// runSessionRetention performs an immediate retention pass, then repeats it
// hourly until the dashboard lifecycle context is cancelled.
func (ds *DashboardServer) runSessionRetention(ctx context.Context) {
	if ds.sessionSnapshots == nil && ds.sessionActions == nil {
		return
	}
	run := func() {
		if err := ds.runSessionRetentionOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("dashboard: session retention purge failed", "error", err)
		}
	}

	run()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

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
	if err := startFeatureRuntime(derived, s.featureRuntime); err != nil {
		cancel()
		s.featureRuntime.Stop()
		return fmt.Errorf("dashboard feature runtime: %w", err)
	}

	state := NewServerState(s.srv)
	state.SetFreshnessStore(s.freshness)
	if s.removals != nil {
		state.SetExclusionReader(s.removals)
		state.SetRemovalWriter(s.removals)
	}
	forceUpdates, err := newPersistentForceUpdateState(derived, s.outbox)
	if err != nil {
		cancel()
		if s.featureRuntime != nil {
			s.featureRuntime.Stop()
		}
		return fmt.Errorf("dashboard force-update outbox: %w", err)
	}

	rdCollections := s.rdCollections
	if rdCollections == nil {
		rdCollections = newRDCollectionResolver(nil)
		s.rdCollections = rdCollections
	}

	ds := &DashboardServer{
		state:                    state,
		cfg:                      s.cfg,
		sessionStore:             NewSessionStore(derived),
		broker:                   NewBroker(),
		rdCollections:            rdCollections,
		ms:                       s.ms,
		as:                       s.as,
		mnt:                      s.mnt,
		spikes:                   s.sps,
		sessionSnapshots:         s.sessionSnapshots,
		sessionQueries:           s.sessionQueries,
		sessionActions:           s.sessionActions,
		heartbeatIntervalChanged: make(chan struct{}, 1),
		remoteEvtSpikeStatus:     make(map[string]evtspike.DetectorStatus),
		forceUpdates:             forceUpdates,
		featureRuntime:           s.featureRuntime,
	}
	ds.setFeatureRuntime(s.featureRuntime)

	if runtime, ok := s.featureRuntime.(interface {
		bindFeaturePublishers(func(investigation.Update), func(sessiondrop.SSEEvent))
	}); ok {
		runtime.bindFeaturePublishers(
			func(update investigation.Update) { _ = ds.PublishInvestigationUpdate(update) },
			func(event sessiondrop.SSEEvent) { _ = ds.PublishSessionDrop(event) },
		)
	}
	ds.setLocalForceUpdateSupported(s.localForceUpdateSupported)
	ds.setHeartbeatInterval(s.cfg.HeartbeatInterval)
	ds.wireServerStateCallbacks()

	mux := http.NewServeMux()
	registerRoutes(derived, ds, mux)

	tlsCfg, fingerprint, err := setupTLS(s.cfg, s.dataDir)
	if err != nil {
		cancel()
		if s.featureRuntime != nil {
			s.featureRuntime.Stop()
		}
		return err
	}
	ds.fingerprint = fingerprint

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		cancel()
		if s.featureRuntime != nil {
			s.featureRuntime.Stop()
		}
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

	s.wg.Add(5)
	go func() {
		defer s.wg.Done()
		slog.Info("dashboard=listening", "addr", addr, "scheme", scheme)
		if err := ds.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("dashboard server error", "error", err)
		}
	}()
	go func() {
		defer s.wg.Done()
		rdCollections.Run(derived, s.cfg.RDConnectionBroker)
	}()
	go func() {
		defer s.wg.Done()
		ds.runStaleHostTransitions(derived)
	}()
	go func() {
		defer s.wg.Done()
		ds.runSessionRetention(derived)
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

// Stop cancels the derived ctx (which fires the shutdown-drain goroutine) and
// blocks until all owned goroutines have exited. Idempotent; safe to call when
// Start was never invoked or returned an error.
func (s *Subsystem) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.featureRuntime != nil {
			s.featureRuntime.Stop()
		}
		s.wg.Wait()
	})
}
