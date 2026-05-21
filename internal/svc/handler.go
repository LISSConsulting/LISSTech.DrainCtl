//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/debugpprof"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/selfmetrics"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/spikereport"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/updater"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"

	"golang.org/x/sys/windows/svc"
)

// configFetchBase is the baseline interval between dashboard notification-config
// refreshes. The actual interval grows exponentially after consecutive failures
// up to configFetchMax.
const (
	configFetchBase = 5 * time.Minute   // baseline re-fetch interval (overridden by dashboard.fetch_interval)
	configFetchMax  = 160 * time.Minute // ceiling (~maxConfigFetchInterval * 30s)
)

// backoffDuration returns the time interval to wait before the next dashboard
// config fetch attempt. base is the configured fetch interval; failures is the
// count of consecutive failures. The interval doubles per failure up to
// configFetchMax.
func backoffDuration(base time.Duration, failures int) time.Duration {
	if base <= 0 {
		base = configFetchBase
	}
	if failures <= 0 {
		return base
	}
	shift := failures
	if shift > 5 {
		shift = 5 // cap doubling at 2^5 = 32×
	}
	d := base << shift
	if d >= configFetchMax {
		return configFetchMax
	}
	return d
}

// Event IDs matching assets/drainctl.man ETW manifest.
// Pass as slog.Int("event_id", EvtXxx) to route to the specific manifest event.
const (
	EvtServiceStarted = 1000
	EvtServiceStopped = 1001
	EvtCheckHealthy   = 1002
	EvtConfigReloaded = 1003
	EvtTransition     = 1004
	EvtGenericInfo    = 1099
	EvtCheckGrace     = 2000
	EvtGenericWarning = 2099
	EvtCheckAlert     = 3000
	EvtRegistryFailed = 3001
	EvtServiceError   = 3002
	EvtGenericError   = 3099
	// Audit event IDs (5xxx) are in the root dc package.
)

// drainService implements svc.Handler.
type drainService struct {
	fileLevel *slog.LevelVar // min level for the file sink
	etwLevel  *slog.LevelVar // min level for the ETW sink
}

// observation is the latest per-tick sample of the local host's drain mode.
// The persistent record of transitions lives in the SQLite audit table; this
// struct is an in-memory cache used by the pipe handler to answer live status
// queries and by svcRunCheck to detect transitions between ticks.
//
// ChangedBy is carried across non-transition ticks so the dashboard's
// `Changed By` column reflects "who set the current state", not "who did
// anything on this tick" (which is only populated when a transition is
// detected; every subsequent tick would otherwise clear the display).
type observation struct {
	Timestamp  time.Time
	DrainMode  dc.DrainMode
	StateSince time.Time
	ChangedBy  string
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
// cfg is accessed from two goroutines (Execute loop + pipe server) and
// must be read/written via atomic.Pointer to avoid a data race.
type serviceHandler struct {
	audit        *telemetry.AuditStore
	metrics      *telemetry.MetricsStore // nil is tolerated in tests; HandleHistory skips perf enrichment
	observed     atomic.Pointer[observation]
	cfg          atomic.Pointer[dc.ServiceConfig]
	dashState    *dashboard.ServerState // nil if dashboard not enabled
	lastPerf     atomic.Pointer[dc.PerfSnapshot]
	lastSessions atomic.Pointer[dc.SessionSummary]
	evtSpikeSub  atomic.Pointer[evtspike.Subsystem] // nil while evtspike subsystem has not been constructed
	registration *registrationSubsystem
}

func (h *serviceHandler) HandleStatus() *dc.CheckResult {
	state, err := dc.ReadDrainMode()
	if err != nil {
		return &dc.CheckResult{
			Version:   dc.Version,
			Timestamp: time.Now(),
			Status:    "Error",
			Message:   err.Error(),
			ExitCode:  2,
		}
	}

	gp := h.cfg.Load().GracePeriod

	res := &dc.CheckResult{
		Version:            dc.Version,
		Timestamp:          time.Now(),
		Host:               state.Host,
		DrainModeLabel:     state.Mode.String(),
		DrainModeValue:     uint32(state.Mode),
		GracePeriodSeconds: int(gp.Seconds()),
	}

	drainActive := state.Mode != dc.AllowAll
	connAllowed := !drainActive
	res.ConnectionsAllowed = &connAllowed

	// State duration from the observed cache — only trust it when the cached
	// mode still matches what we just read from the registry. A mismatch means
	// the state changed between the last tick and this pipe request; the next
	// tick will refresh the cache.
	obs := h.observed.Load()
	if obs != nil && obs.DrainMode == state.Mode {
		s := obs.StateSince.Local()
		res.StateSince = &s
		dur := time.Since(obs.StateSince).Seconds()
		res.StateDurationSeconds = &dur
	}

	// Determine status.
	stateDur := time.Duration(0)
	if res.StateDurationSeconds != nil {
		stateDur = time.Duration(*res.StateDurationSeconds) * time.Second
	}

	res.Status, res.Message, res.ExitCode = dc.ClassifyState(drainActive, stateDur, gp)

	// Use cached session and performance data from the last poll cycle.
	// GetSessionSummary() may fail in the pipe handler goroutine context
	// (different thread security token), so prefer the cached snapshot.
	if sess := h.lastSessions.Load(); sess != nil {
		res.Sessions = sess
	}
	res.Performance = h.lastPerf.Load()

	// Check for transition.
	if obs != nil && obs.DrainMode != state.Mode {
		res.Transition = true
		res.TransitionFrom = obs.DrainMode.String()
	}

	return res
}

// historyPerfCounters names the metrics_raw counters that populate the perf
// and session fields on a `drainctl history` audit row. Kept in sync with
// the writer side in internal/dashboard/server.go `checkResultSamples`.
var historyPerfCounters = []string{
	"cpu_pct",
	"input_delay_max_ms",
	"mem_avail_mb",
	"mem_total_mb",
	"disk_queue",
	"tcp_retrans_sec",
	"sessions_active",
	"sessions_disconnected",
	"sessions_total",
	"sessions_max",
}

// historyPerfToleranceMs is the ±window used when joining a history row
// against metrics_raw. Two minutes covers a sampler interval up to ~60 s
// with one missed tick; counters outside this window render as `-`.
const historyPerfToleranceMs int64 = 2 * 60 * 1000

// HandleHistory returns the most recent audit rows from the SQLite audit
// table. Only drain-mode transitions (and startup reconciliation rows) are
// stored — per-tick observations are no longer persisted (T059). The
// changesOnly filter drops reconciliation-style no-op rows.
//
// Perf and session counters are NOT stored on audit rows; they are joined
// from metrics_raw at read time, picking the closest sample to each
// transition's timestamp. This preserves the pre-007 `drainctl history`
// column shape (CPU%, INPUT DLY, SESSIONS) while keeping the audit schema
// narrow and append-only.
func (h *serviceHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	if h.audit == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	recs, _, err := h.audit.QueryRange(ctx, telemetry.QueryFilter{
		Limit:       limit,
		ChangesOnly: changesOnly,
	})
	if err != nil {
		slog.Warn("pipe history query failed", "error", err)
		return nil
	}
	out := make([]dc.AuditRecord, len(recs))
	for i, r := range recs {
		out[i] = auditFromTelemetry(r)
		if h.metrics != nil {
			if values, mErr := h.metrics.NearestCounters(ctx, r.Host, r.Ts, historyPerfToleranceMs, historyPerfCounters); mErr == nil {
				applyPerfCounters(&out[i], values)
			} else {
				slog.Debug("pipe history perf enrich failed",
					"host", r.Host, "ts", r.Ts, "error", mErr)
			}
		}
	}
	return out
}

// applyPerfCounters copies values from a metrics_raw lookup into the
// per-row perf/session fields on an AuditRecord. Missing counters are
// left at the zero value so the CLI formatter renders them as `-`.
func applyPerfCounters(rec *dc.AuditRecord, values map[string]float64) {
	if v, ok := values["cpu_pct"]; ok {
		rec.CPUPct = v
	}
	if v, ok := values["input_delay_max_ms"]; ok {
		rec.InputDelayMax = v
	}
	if v, ok := values["mem_avail_mb"]; ok {
		rec.MemAvailMB = v
	}
	if v, ok := values["mem_total_mb"]; ok {
		rec.MemTotalMB = v
	}
	if v, ok := values["disk_queue"]; ok {
		rec.DiskQueue = v
	}
	if v, ok := values["tcp_retrans_sec"]; ok {
		rec.TCPRetransSec = v
	}
	if v, ok := values["sessions_active"]; ok {
		rec.ActiveSessions = int(v)
	}
	if v, ok := values["sessions_disconnected"]; ok {
		rec.DisconnectedSessions = int(v)
	}
	if v, ok := values["sessions_total"]; ok {
		rec.TotalSessions = int(v)
	}
	if v, ok := values["sessions_max"]; ok {
		rec.MaxSessions = int(v)
	}
}

// auditFromTelemetry maps a telemetry audit row to the public dc.AuditRecord
// shape for the pipe handler. Internal-package mirror of the identically-named
// helper in root-package history.go; duplicated because internal/svc cannot
// reach unexported root helpers.
func auditFromTelemetry(r telemetry.AuditRecord) dc.AuditRecord {
	rec := dc.AuditRecord{
		Timestamp:      r.Ts,
		Host:           r.Host,
		DrainMode:      dc.DrainMode(r.NewState),
		ChangedBy:      r.ChangedBy,
		Changed:        r.PrevState != r.NewState,
		Reconciliation: r.Reconciliation,
		Reason:         r.Reason,
	}
	rec.DrainLabel = rec.DrainMode.String()
	if r.KeyModifiedTs != nil {
		rec.KeyModified = *r.KeyModifiedTs
	}
	return rec
}

func (h *serviceHandler) HandleServers() json.RawMessage {
	if h.dashState == nil {
		return nil
	}
	all := h.dashState.All()
	raw, _ := json.Marshal(all)
	return raw
}

func (h *serviceHandler) HandleRemoveServer(hostname string) error {
	if h.dashState == nil {
		return fmt.Errorf("dashboard not enabled")
	}
	if !h.dashState.Remove(hostname) {
		return fmt.Errorf("host not found: %s", hostname)
	}
	return nil
}

// HandleRegister runs dashboard.Register under the service's machine-account
// identity so the CLI can route around the dashboard's requireMachineAccount
// gate. See BUGS.md #1.
func (h *serviceHandler) HandleRegister(dashboardURL string) (json.RawMessage, error) {
	if dashboardURL == "" {
		return nil, fmt.Errorf("dashboard url required")
	}
	// HandleRegister is invoked from the named-pipe server goroutine, which has
	// no service-scoped ctx threaded through. Bound the call locally so a stuck
	// SSPI/HTTP exchange cannot wedge the pipe handler indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if h.registration == nil {
		return nil, fmt.Errorf("registration subsystem not running")
	}
	result, err := h.registration.Register(ctx, dashboardURL)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal register result: %w", err)
	}
	return raw, nil
}

func (h *serviceHandler) HandleBaselineReset() error {
	sub := h.evtSpikeSub.Load()
	if sub == nil {
		return fmt.Errorf("evtspike subsystem not running")
	}
	return sub.ResetBaseline()
}

// Execute is the Windows service main loop.
func (s *drainService) Execute(args []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
	statusCh <- svc.Status{State: svc.StartPending, WaitHint: 10000}

	// Capture panics so the stack trace is written to both the file log and
	// the Windows Event Log before the process terminates.  Without this the
	// SCM records Event 7034 ("terminated unexpectedly") but we get no
	// indication of what actually went wrong.
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			slog.Error("PANIC", "panic", fmt.Sprintf("%v", r), "stack", stack, slog.Int("event_id", EvtServiceError))
			panic(r) // re-panic so the SCM sees the crash
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load config from config.json (migrates from registry if needed).
	// Validate and write back so new fields appear with defaults.
	fullCfg, err := dc.LoadConfig()
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	fullCfg.Validate()
	if err := dc.SaveConfig(fullCfg); err != nil {
		slog.Warn("config normalization failed", "error", err)
	}

	cfg := fullCfg.ToServiceConfig()
	dashCfg := fullCfg.ToDashboardConfig()
	prevEvtSpikeCfg := fullCfg.EvtSpike

	// Apply Go runtime soft memory limit. Makes the GC more aggressive about
	// returning pages to the OS, which matters on memory-constrained RDS hosts.
	memLimitMB := fullCfg.MemoryLimitMB
	debug.SetMemoryLimit(int64(memLimitMB) << 20)

	notifyTargets := fullCfg.Notifications
	notifyState := &dc.NotifyState{
		LastAlertNotify:       make(map[string]time.Time),
		LastSessionWarnNotify: make(map[string]time.Time),
	}

	// evtspike spike dispatch is funnelled through spikeCh so notifyState stays
	// single-threaded (only the Execute goroutine calls SendNotification). A
	// nil channel never selects, so the branch is inert when the subsystem is
	// disabled.
	var spikeCh chan dc.SpikePayload

	slog.Info("service=starting", "version", dc.Version, "grace", cfg.GracePeriod, "poll", cfg.PollInterval, "retention_days", cfg.RetentionDays, "fetch_interval", dashCfg.FetchInterval, "memory_limit_mb", memLimitMB)

	// Optional runtime/pprof debug server. Off unless DRAINCTL_PPROF_PORT
	// is set. Loopback-only. Used for memory-leak and goroutine-leak
	// diagnosis on production boxes without rebuilding. LCI Subsystem so
	// the listener drains cleanly on SCM-stop instead of relying on
	// ctx-cancel alone.
	pprofSub := debugpprof.New()
	if err := pprofSub.Start(ctx); err != nil {
		slog.Warn("pprof failed to start", "error", err)
	}

	// Periodic self-metrics emitted at slog.Debug — runtime heap state,
	// goroutine count, RSS, GC stats, SSPI counters. Passive (no
	// listener); operators see the trend by setting log_file_level=debug
	// and grepping "selfmetrics=" out of the daily log. LCI Subsystem so
	// the goroutine drains cleanly on SCM-stop instead of relying on
	// ctx-cancel alone.
	selfMetricsSub := selfmetrics.New(selfmetrics.DefaultInterval)
	if err := selfMetricsSub.Start(ctx); err != nil {
		slog.Warn("selfmetrics failed to start", "error", err)
	}

	// Open SQLite telemetry store before the named pipe and HTTP servers.
	telDB, err := telemetry.Open(dc.DefaultDataDir())
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	defer func() { _ = telDB.Close() }()

	// Import the legacy audit.jsonl into the SQLite audit table before any
	// audit writer (NewAuditStore + svcRunCheck) or drift reconciliation runs,
	// so LatestByHost baselines include pre-upgrade history (FR-020). Failure
	// is fatal: continuing would let live audit writes land before the legacy
	// import completes, contaminating the baseline that MigrateJSONL seeds
	// from latestNewStatePerHost on the next resume. The JSONL stays on disk
	// and the migration is idempotent, so the operator can restart and retry.
	migRes, migErr := telemetry.MigrateJSONL(ctx, telDB, dc.DefaultDataDir())
	if migErr != nil {
		slog.Error("service=failed", "error", fmt.Errorf("audit jsonl migration: %w", migErr))
		return false, 1
	}
	if migRes.JSONLFound {
		slog.Info("audit JSONL migration",
			"lines", migRes.LineCount,
			"imported", migRes.Imported,
			"observations", migRes.Observations,
			"skipped", migRes.Skipped,
			"backup", migRes.BackupPath,
			"duration", migRes.Duration)
	}

	metricsStore, err := telemetry.NewMetricsStore(ctx, telDB)
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	// Defer ordering note: this Close runs BEFORE telDB.Close() because Go
	// defers fire LIFO — declaring this later puts it on top of the stack.
	// Required ordering: prepared stmt must be finalised before its writer
	// pool tears down.
	defer func() { _ = metricsStore.Close() }()
	maintenanceStore := telemetry.NewMaintenanceStore(telDB)
	serverStore := telemetry.NewServerStore(telDB)
	eventSpikeStore := telemetry.NewEventSpikeStore(telDB)

	auditStore, err := telemetry.NewAuditStore(ctx, telDB)
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	defer func() { _ = auditStore.Close() }()

	// Drift reconciliation (T025, FR-001a): detect drain-state divergence
	// that happened while the service was down and emit exactly one
	// reconciliation audit row per host when needed. MUST run AFTER
	// MigrateJSONL AND NewAuditStore (so LatestByHost sees imported
	// history) and BEFORE live ingest starts (aggregator/retention/
	// svcRunCheck all below). A registry-read failure or reconcile error
	// is not fatal — the service proceeds in degraded mode and the next
	// live tick will catch up.
	if regState, regErr := dc.ReadDrainMode(); regErr != nil {
		slog.Warn("drift_reconciliation=skipped reason=registry_read_failed", "error", regErr)
	} else {
		probe := telemetry.DrainProbe{
			Host:        regState.Host,
			ModeValue:   int(regState.Mode),
			KeyModified: regState.KeyModified,
		}
		reconcileStart := time.Now()
		if rerr := telemetry.Reconcile(ctx, telDB, auditStore, probe, reconcileStart); rerr != nil {
			slog.Warn("drift_reconciliation=failed", "error", rerr)
		} else {
			slog.Info("drift_reconciliation=complete",
				"host", regState.Host,
				"observed_mode", regState.Mode,
				"duration", time.Since(reconcileStart))
		}
	}

	// Aggregator + retention workers run inside an LCI subsystem owned
	// by internal/telemetry. Shutdown is bounded at 10s via
	// waitWithTimeout so a stuck worker cannot stall svc.Stop past the
	// SCM's wait hint.
	telSub := telemetry.New(
		telemetry.Config{
			AggregatorIntervalSeconds: fullCfg.Telemetry.AggregatorIntervalSeconds,
			RetentionIntervalMinutes:  fullCfg.Telemetry.RetentionIntervalMinutes,
		},
		telemetry.Deps{
			Aggregator: telemetry.NewAggregator(telDB, fullCfg.Telemetry.AggregatorIntervalSeconds),
			Retention:  telemetry.NewRetention(telDB, fullCfg.Telemetry.RetentionIntervalMinutes, newRetentionProvider(fullCfg)),
		},
	)
	if err := telSub.Start(ctx); err != nil {
		slog.Error("service=failed", "error", fmt.Errorf("telemetry subsystem start: %w", err))
		return false, 1
	}

	// Spike-report fan-out lives in its own LCI subsystem. Forward()
	// launches a tracked goroutine; Stop() drains them. The shared
	// telemetryWG that used to back this (B2 / commit 1bacf6a) is gone.
	spikeSub := spikereport.New(dashboard.ReportSpike)
	if err := spikeSub.Start(ctx); err != nil {
		slog.Warn("spike report subsystem failed to start", "error", err)
	}

	// Auto-update subsystem (010). Opt-in via config; Start is a no-op when
	// cfg.Update.Enabled=false. The shutdown callback is the same `cancel`
	// the SCM-stop branch invokes — when the updater decides to install,
	// it triggers our own clean shutdown so msiexec can replace files
	// before SCM forces a stop. Stop is called from the SCM-stop branch
	// alongside evtSpikeSub.Stop / waitTelemetryWorkers; the LCI Stop is
	// idempotent so a defer-based fallback isn't needed.
	updaterSub := updater.New(fullCfg.Update, cancel)
	if err := updaterSub.Start(ctx); err != nil {
		slog.Warn("updater failed to start", "error", err)
	}

	// Registry watcher subsystem. A start failure (e.g. NOTIFY rights
	// denied) drops to poll-only operation: regCh is rebound to a
	// never-firing channel so the select arm goes inert without a nil
	// guard. The subsystem's own events channel is left untouched and
	// Stop is still called in the SCM-stop branch (idempotent).
	regSub := watcher.NewRegistrySubsystem(dc.RegPath)
	regCh := regSub.Events()
	if err := regSub.Start(ctx); err != nil {
		slog.Warn("registry watcher failed, polling only", "error", err)
		regCh = make(chan struct{}) // never fires
	}

	// Config-file watcher subsystem. Start cannot fail (the underlying
	// FindFirstChangeNotification path silently drops to a 5 s mtime poll
	// fallback), but the err return is preserved for symmetry with the
	// other LCI subs and so future Start-side init can surface upward.
	configSub := watcher.NewConfigFileSubsystem(dc.DefaultConfigPath())
	configCh := configSub.Events()
	if err := configSub.Start(ctx); err != nil {
		slog.Warn("config watcher failed", "error", err)
		configCh = make(chan struct{}) // never fires
	}

	// Start event log subscriber for change attribution.
	var evtSub *watcher.EventSubscriber
	if sub, err := watcher.NewEventSubscriber(ctx); err != nil {
		slog.Warn("event subscriber failed, attribution via wevtutil fallback", "error", err)
	} else {
		evtSub = sub
	}

	// Start pipe server.
	handler := &serviceHandler{audit: auditStore, metrics: metricsStore}
	handler.cfg.Store(&cfg)

	// Performance monitoring subsystem owns PDH open/prime/close and hot reload.
	perfSub := newPerformanceSubsystem(cfg.Performance, &handler.lastPerf)
	if err := perfSub.Start(ctx); err != nil {
		slog.Warn("performance monitoring failed to start", "error", err)
	}

	// Seed the in-memory observation from the latest audit row for this host
	// so HandleStatus can report a state duration before the first poll tick
	// lands. If the mode on disk diverged from the registry while the service
	// was down, the first tick will see a mismatch and record a transition.
	if hostname, _ := os.Hostname(); hostname != "" {
		if latest, err := auditStore.LatestByHost(ctx); err != nil {
			slog.Warn("observation seed: audit query failed", "error", err)
		} else if row, ok := latest[hostname]; ok {
			handler.observed.Store(&observation{
				Timestamp:  row.Ts,
				DrainMode:  dc.DrainMode(row.NewState),
				StateSince: row.Ts,
				ChangedBy:  row.ChangedBy,
			})
		}
	}

	registrationSub := newRegistrationSubsystem(dashboard.Register)
	if err := registrationSub.Start(ctx); err != nil {
		slog.Warn("dashboard registration subsystem failed to start", "error", err)
	} else {
		handler.registration = registrationSub
	}

	pipeSub := pipe.New(handler)
	if err := pipeSub.Start(ctx); err != nil {
		slog.Warn("pipe server failed to start", "error", err)
	}

	// Start dashboard if enabled. The Subsystem owns the HTTP server,
	// session/SSPI reapers, and broker; Stop drains them on SCM-stop.
	var dashState *dashboard.ServerState
	var dashSub *dashboard.Subsystem
	if dashCfg.Enabled {
		sub := dashboard.NewSubsystem(dashCfg, dc.DefaultDataDir(), metricsStore, auditStore, maintenanceStore, serverStore, eventSpikeStore)
		if err := sub.Start(ctx); err != nil {
			slog.Warn("dashboard failed to start", "error", err)
		} else {
			dashSub = sub
			dashState = sub.State()
			handler.dashState = dashState
			slog.Info("dashboard=started", "port", dashCfg.Port)
		}
	}

	// Auto-discover dashboard via SRV if URL is not explicitly configured.
	if dashCfg.URL == "" {
		if discovered := dashboard.DiscoverDashboardURL(); discovered != "" {
			dashCfg.URL = discovered
		}
	}

	// Prepare dashboard client and state; actual registration and config
	// fetch are deferred to the first poll tick so the service reports
	// Running to the SCM without waiting on network I/O.
	var useRemoteConfig bool
	var lastConfigFetch time.Time
	// Cached most-recent successful pull-config response. When the local
	// config.json watcher fires, we re-apply this cached remote on top of the
	// freshly-loaded local values so the dashboard's authoritative settings
	// aren't briefly overwritten by local defaults during the
	// local-reload → next-remote-fetch window (up to FetchInterval seconds).
	var lastRemote *dashboard.RemoteSettings
	dashConfigFailures := 0
	dashRegistered := false

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)

		// Local self-registration is instant (no network) — safe to do here.
		// Skip when dashboard_only is set (management server, not an RDSH).
		if dashState != nil && isLocalDashboard(dashCfg.URL) && !cfg.DashboardOnly {
			hostname, _ := os.Hostname()
			if hostname != "" {
				dashState.Register(hostname)
				dashRegistered = true
				slog.Info("dashboard=self-registered", "host", hostname)
			}
		}
		if cfg.DashboardOnly {
			slog.Info("dashboard_only=true, skipping local drain monitoring")
		}
		// Remote registration + config fetch happen on the first poll tick
		// (lastConfigFetch is zero, dashRegistered is false).
	}

	// Construct the evtspike subsystem unconditionally so a later
	// enabled=false→true config reload can bring it up without restart.
	// Subsystem.Start is idempotent w.r.t. cfg.Enabled: when disabled it
	// captures startCtx and returns without launching loops, so a later
	// Reload(Enabled=true) can hydrate. spikeCh is also allocated
	// unconditionally because OnSpike closes over it; a nil channel would
	// drop every future spike to the default: arm of the select.
	// OnSpike forwards to spikeCh which the Execute loop drains; this keeps
	// notifyState access single-threaded (only the Execute goroutine calls
	// SendNotification). Dashboard ring buffer and SSE broker calls are
	// thread-safe, so they run directly from the subsystem goroutine without
	// passing through spikeCh.
	host, _ := os.Hostname()
	spikeCh = make(chan dc.SpikePayload, 16)
	evtSpikeSub := evtspike.New(fullCfg.EvtSpike, host)
	evtSpikeSub.OnSpike = func(p dc.SpikePayload) {
		if dashState != nil && dashState.OnEvtSpikeIngest != nil {
			dashState.OnEvtSpikeIngest(p)
		}
		select {
		case spikeCh <- p:
		default:
			slog.Warn("evtspike=spike_dropped reason=channel_full",
				"host", p.Host, "channel", p.Channel)
		}
	}
	evtSpikeSub.OnStatusChange = func(status evtspike.DetectorStatus) {
		if dashState != nil && dashState.OnEvtSpikeStatus != nil {
			dashState.OnEvtSpikeStatus(status)
		}
	}
	if dashState != nil && dashState.RegisterEvtSpikeStatusFunc != nil {
		localHost := host
		remoteLookup := dashState.GetRemoteEvtSpikeStatus
		dashState.RegisterEvtSpikeStatusFunc(func(h string) evtspike.DetectorStatus {
			if h != localHost {
				if remoteLookup != nil {
					return remoteLookup(h)
				}
				return evtspike.DetectorStatus{}
			}
			return evtSpikeSub.Status()
		})
	}
	if err := evtSpikeSub.Start(ctx); err != nil {
		slog.Warn("evtspike=start_failed", "error", err)
	} else if fullCfg.EvtSpike.Enabled {
		slog.Info("evtspike=enabled", "host", host)
	}
	// Expose the subsystem to the pipe handler so `drainctl baseline reset`
	// can reach it. Stored as an atomic.Pointer because the pipe server
	// runs on its own goroutine; Execute mutates this slot once at startup
	// and the pipe handler reads it on demand.
	handler.evtSpikeSub.Store(evtSpikeSub)

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	// Report running immediately so the SCM doesn't wait on network I/O.
	// Dashboard registration + config fetch happen asynchronously on the
	// first poll tick (fired immediately below).
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	// Run initial check (skip in dashboard-only mode).
	if !cfg.DashboardOnly {
		svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub)
	}
	slog.Info("service=running",
		slog.Int("event_id", EvtServiceStarted),
		"version", dc.Version,
		"poll", cfg.PollInterval,
		"grace", cfg.GracePeriod,
		"retention_days", cfg.RetentionDays)

	// Fire dashboard registration + config fetch shortly after startup
	// instead of waiting for the first full poll interval.
	dashBootstrapSub := newDashboardBootstrapSubsystem(dashCfg.URL != "" && !dashRegistered, 5*time.Second)
	if err := dashBootstrapSub.Start(ctx); err != nil {
		slog.Warn("dashboard bootstrap subsystem failed to start", "error", err)
	}
	dashBootstrap := dashBootstrapSub.Events()

	// fetchRemoteConfig pulls from the dashboard, applies results, and
	// reconciles the evtspike/perf/ticker subsystems. Extracted so that both
	// the every-poll fetch AND inline-after-successful-registration paths
	// exercise exactly the same code path. Returns early when no dashboard
	// URL is configured or when we're still inside a failure-backoff window.
	fetchRemoteConfig := func() {
		if dashCfg.URL == "" {
			return
		}
		if dashConfigFailures > 0 &&
			time.Since(lastConfigFetch) < backoffDuration(dashCfg.FetchInterval, dashConfigFailures) {
			return
		}
		lastConfigFetch = time.Now()
		slog.Debug("dashboard config fetch", "url", dashCfg.URL)
		var cfgRemote *dashboard.RemoteSettings
		var cfgErr error
		if dashState != nil {
			cfgRemote, cfgErr = dashboard.GetSettings()
		} else {
			cfgRemote, cfgErr = dashboard.FetchSettings(ctx, dashCfg.URL)
		}
		if cfgErr != nil {
			dashConfigFailures++
			nextIn := backoffDuration(dashCfg.FetchInterval, dashConfigFailures)
			slog.Warn("dashboard: settings refresh failed, using cached",
				"error", cfgErr, "next_retry_in", nextIn.Round(time.Second))
			return
		}
		dashConfigFailures = 0
		useRemoteConfig = true
		lastRemote = cfgRemote
		oldPollInterval := cfg.PollInterval
		effectiveEvtSpike := prevEvtSpikeCfg
		applyRemoteConfig(cfgRemote, &cfg, &notifyTargets, &effectiveEvtSpike)
		handler.cfg.Store(&cfg)
		perfSub.Reload(cfg.Performance)
		if cfg.PollInterval != oldPollInterval {
			pollTicker.Reset(cfg.PollInterval)
			slog.Info("config=poll-interval-remote",
				"old", oldPollInterval, "new", cfg.PollInterval)
		}
		if err := applyEvtSpikeConfigReload(evtSpikeSub, prevEvtSpikeCfg, effectiveEvtSpike); err != nil {
			slog.Warn("evtspike remote reload failed", "error", err)
		} else {
			prevEvtSpikeCfg = effectiveEvtSpike
		}
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				slog.Info("service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				dashBootstrapSub.Stop()
				if evtSpikeSub != nil {
					evtSpikeSub.Stop()
				}
				updaterSub.Stop()
				perfSub.Stop()
				selfMetricsSub.Stop()
				pprofSub.Stop()
				pipeSub.Stop()
				registrationSub.Stop()
				regSub.Stop()
				configSub.Stop()
				if dashSub != nil {
					dashSub.Stop()
				}
				waitWithTimeout("telemetry", telSub.Stop, 10*time.Second)
				waitWithTimeout("spike_report", spikeSub.Stop, 10*time.Second)
				slog.Info("service=stopped", slog.Int("event_id", EvtServiceStopped))
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			if !cfg.DashboardOnly {
				slog.Info("trigger=registry_change")
				svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub)
			}

		case spike := <-spikeCh:
			spikeResult := &dc.CheckResult{
				Version:   dc.Version,
				Timestamp: time.Now(),
				Host:      spike.Host,
				Spike:     &spike,
			}
			dc.SendNotification(notifyTargets, notifyState, spikeResult, dc.TriggerEventSpike, "")
			// Propagate to a remote central dashboard. The local-dashboard path
			// (dashState != nil) already appended via OnEvtSpikeIngest when the
			// subsystem fired OnSpike — reporting again would double-insert.
			if dashState == nil && dashCfg.URL != "" && dashRegistered {
				spikeCopy := spike
				spikeSub.Forward(dashCfg.URL, &spikeCopy)
			}

		case <-dashBootstrap:
			// First async dashboard registration attempt after startup.
			dashBootstrap = nil // one-shot
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(ctx, &dashCfg) {
					dashRegistered = true
					dashConfigFailures = 0
					// Pull config immediately on successful registration so
					// the agent doesn't spend up to one PollInterval running
					// local defaults before the dashboard authoritative
					// settings arrive.
					fetchRemoteConfig()
				}
			}

		case <-pollTicker.C:
			// Retry registration if a previous attempt failed. Once registered,
			// dashRegistered stays true and this branch is skipped.
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(ctx, &dashCfg) {
					dashRegistered = true
					dashConfigFailures = 0
					// Same register-triggered fetch as the bootstrap path.
					fetchRemoteConfig()
				}
			}

			// Pull remote config on every poll (Option B). Cheap — one
			// LoadConfig + JSON encode per agent per poll. Failures back off
			// exponentially from FetchInterval inside fetchRemoteConfig so a
			// downed dashboard doesn't spam warnings every tick.
			fetchRemoteConfig()
			if !cfg.DashboardOnly {
				slog.Debug("diag: step=svc_run_check")
				svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub)
			}

		case <-configCh:
			newFullCfg, err := dc.LoadConfig()
			if err != nil {
				slog.Warn("config reload failed", "error", err)
				continue
			}
			// Apply log levels before emitting any log messages about the reload.
			if fl, err := logging.ParseLevel(newFullCfg.LogFileLevel); err == nil {
				s.fileLevel.Set(fl)
			}
			if el, err := logging.ParseLevel(newFullCfg.LogEventLevel); err == nil {
				s.etwLevel.Set(el)
			}
			if newFullCfg.MemoryLimitMB != memLimitMB {
				memLimitMB = newFullCfg.MemoryLimitMB
				debug.SetMemoryLimit(int64(memLimitMB) << 20)
				slog.Info("memory limit updated", "mb", memLimitMB)
			}
			newCfg := newFullCfg.ToServiceConfig()
			effectiveEvtSpike := newFullCfg.EvtSpike
			// Synchronously refresh the dashboard-authoritative snapshot before
			// merging. The cached `lastRemote` is populated on the next poll
			// tick (up to one PollInterval away), so without this refresh the
			// newly-loaded local config runs with stale (or missing) remote
			// overrides — a server currently in ALERT on CPU thresholds would
			// report HEALTHY for one poll because the local config file
			// has no thresholds of its own. Dashboard URL unchanged from the
			// live config is a precondition: a URL change is handled below and
			// will re-initialize the client + re-register before the next
			// pollTicker fires its own fetchRemoteConfig.
			if newFullCfg.Dashboard.URL != "" && newFullCfg.Dashboard.URL == dashCfg.URL {
				var fresh *dashboard.RemoteSettings
				var ferr error
				if dashState != nil {
					fresh, ferr = dashboard.GetSettings()
				} else {
					fresh, ferr = dashboard.FetchSettings(ctx, newFullCfg.Dashboard.URL)
				}
				if ferr == nil {
					lastRemote = fresh
					useRemoteConfig = true
					dashConfigFailures = 0
					lastConfigFetch = time.Now()
				} else {
					slog.Warn("dashboard: reload-time fetch failed, using cached", "error", ferr)
				}
			}
			// Merge the dashboard-authoritative values on top of the freshly-
			// loaded local config. If the inline fetch above succeeded,
			// `lastRemote` is fresh; otherwise we fall back to the cached
			// snapshot (bounded by the existing up-to-FetchInterval staleness
			// window — no worse than the previous behavior).
			if useRemoteConfig && lastRemote != nil && newFullCfg.Dashboard.URL != "" {
				applyRemoteConfig(lastRemote, &newCfg, &notifyTargets, &effectiveEvtSpike)
			}
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
			cfg = newCfg
			handler.cfg.Store(&cfg)
			// Push the new UpdateConfig into the running updater
			// subsystem so an operator's enabled / channel flip is
			// honored within seconds (the wakeCh interrupts the
			// poll-loop's current sleep). Without this call the
			// updater's view of cfg stays frozen at service start.
			updaterSub.UpdateConfig(newFullCfg.Update)
			newDashCfg := newFullCfg.ToDashboardConfig()

			// Handle dashboard URL or TLS fingerprint changes.
			if newDashCfg.URL != dashCfg.URL || newDashCfg.TLSFingerprint != dashCfg.TLSFingerprint {
				if newDashCfg.URL != "" {
					dashboard.InitDashClient(newDashCfg.TLSFingerprint)
					lastConfigFetch = time.Time{} // force re-fetch on next poll
					dashConfigFailures = 0        // reset backoff on URL change
					dashRegistered = false        // re-register against new URL
				}
			}

			// If dashboard URL was removed, revert to local targets.
			if newDashCfg.URL == "" && useRemoteConfig {
				useRemoteConfig = false
				notifyTargets = newFullCfg.Notifications
				pruneNotifyState(notifyState, notifyTargets)
			} else if !useRemoteConfig {
				notifyTargets = newFullCfg.Notifications
				pruneNotifyState(notifyState, notifyTargets)
			}

			// Start dashboard on hot-reload if it was just enabled.
			// Also self-register the local host if dashboard URL points here.
			if newDashCfg.Enabled && !dashCfg.Enabled {
				sub := dashboard.NewSubsystem(newDashCfg, dc.DefaultDataDir(), metricsStore, auditStore, maintenanceStore, serverStore, eventSpikeStore)
				if err := sub.Start(ctx); err != nil {
					slog.Warn("dashboard failed to start on config reload", "error", err)
				} else {
					dashSub = sub
					dashState = sub.State()
					handler.dashState = dashState
					slog.Info("dashboard=started", "port", newDashCfg.Port, "reason", "late start")
					if isLocalDashboard(newDashCfg.URL) {
						if h, _ := os.Hostname(); h != "" {
							dashState.Register(h)
							dashRegistered = true
							slog.Info("dashboard=self-registered", "host", h)
						}
					}
				}
			} else if dashState != nil && !dashRegistered && !cfg.DashboardOnly &&
				isLocalDashboard(newDashCfg.URL) && !isLocalDashboard(dashCfg.URL) {
				// URL just changed to point at this machine — self-register in
				// memory immediately rather than waiting for the next poll tick.
				if h, _ := os.Hostname(); h != "" {
					dashState.Register(h)
					dashRegistered = true
					slog.Info("dashboard=self-registered", "host", h)
				}
			}

			// Preserve SRV-discovered URL if the config file doesn't set one.
			if newDashCfg.URL == "" && dashCfg.URL != "" {
				newDashCfg.URL = dashCfg.URL
			}
			dashCfg = newDashCfg
			slog.Info("config=reloaded")

			// Hot-apply evtspike config changes to the running subsystem.
			// effectiveEvtSpike is the local-reloaded EvtSpikeConfig with any
			// cached-remote Enabled override applied on top, so an admin-edited
			// config.json doesn't clobber the dashboard's evtspike toggle.
			if err := applyEvtSpikeConfigReload(evtSpikeSub, prevEvtSpikeCfg, effectiveEvtSpike); err != nil {
				slog.Warn("evtspike reload failed", "error", err)
			}
			prevEvtSpikeCfg = effectiveEvtSpike

			// Sync performance collector with new config. Placed after dashCfg
			// update so the immediate svcRunCheck reports to the current URL.
			if !cfg.DashboardOnly && perfSub.Reload(cfg.Performance) {
				svcRunCheck(ctx, handler, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfSub.Collector(), perfSub.TriggerState(), evtSpikeSub)
			}
			slog.Info("config=reloaded-etw", slog.Int("event_id", EvtConfigReloaded))
		}
	}
}

// applyEvtSpikeConfigReload calls sub.Reload when the EvtSpike block has
// changed between loads. Execute constructs and Start()s the subsystem
// unconditionally, so an Enabled toggle rides through Reload's Enabled-diff
// branch rather than a nil-sub shortcut. Equality is reflect.DeepEqual so the
// helper accepts two whole EvtSpikeConfig snapshots — both scalar and
// channel-list mutations are detected by one compare.
//
// Split out from Execute's configCh branch so the T068 integration test can
// drive the exact code path Execute wires in, without a runnable SCM.
func applyEvtSpikeConfigReload(sub *evtspike.Subsystem, oldCfg, newCfg dc.EvtSpikeConfig) error {
	if reflect.DeepEqual(oldCfg, newCfg) {
		return nil
	}
	slog.Info("config=reloaded-evtspike")
	return sub.Reload(newCfg)
}

// registerWithDashboard registers this host with the dashboard and performs
// auto-pin if enabled. Returns true on success. Safe to call multiple times;
// the dashboard treats re-registration as a no-op for already-known hosts.
func registerWithDashboard(ctx context.Context, dashCfg *dc.DashboardConfig) bool {
	slog.Debug("dashboard registration attempt", "url", dashCfg.URL)
	regResult, err := dashboard.Register(ctx, dashCfg.URL)
	if err != nil {
		slog.Warn("dashboard: registration failed, will retry on next poll", "error", err)
		return false
	}
	slog.Info("dashboard=registered")
	if refuse, _ := shouldRefuseFingerprintUpdate(dashCfg.TLSFingerprint, regResult.TLSFingerprint); refuse {
		slog.Error("dashboard=fingerprint-mismatch", "saved", dashCfg.TLSFingerprint, "offered", regResult.TLSFingerprint)
		return false
	}
	// Auto-pin: save the dashboard's TLS fingerprint if enabled and we don't have one yet.
	if dashCfg.AutoPin && dashCfg.TLSFingerprint == "" && regResult.TLSFingerprint != "" {
		dashCfg.TLSFingerprint = regResult.TLSFingerprint
		dashboard.InitDashClient(dashCfg.TLSFingerprint)
		slog.Info("dashboard=auto-pinned", "fingerprint", dashCfg.TLSFingerprint)
		// Persist to config.json so pinning survives restarts.
		if fileCfg, err := dc.LoadConfig(); err == nil {
			fileCfg.Dashboard.TLSFingerprint = dashCfg.TLSFingerprint
			if err := dc.SaveConfig(fileCfg); err != nil {
				slog.Warn("dashboard: failed to save fingerprint to config", "error", err)
			}
		}
	}
	return true
}

func shouldRefuseFingerprintUpdate(saved, offered string) (bool, error) {
	if saved != "" && offered != "" && saved != offered {
		return true, fmt.Errorf("dashboard fingerprint mismatch: saved=%s  offered=%s", saved, offered)
	}
	return false, nil
}

// RunService starts the Windows service. Called by the CLI's hidden
// "service run" subcommand.
func RunService() error {
	// Initialise log level vars from config (fall back to defaults if config
	// is unavailable — Execute() will re-load config and apply correct values).
	fileLevel := &slog.LevelVar{}
	fileLevel.Set(slog.LevelDebug)
	etwLevel := &slog.LevelVar{}
	etwLevel.Set(slog.LevelInfo)

	if startCfg, err := dc.LoadConfig(); err == nil {
		if fl, err := logging.ParseLevel(startCfg.LogFileLevel); err == nil {
			fileLevel.Set(fl)
		}
		if el, err := logging.ParseLevel(startCfg.LogEventLevel); err == nil {
			etwLevel.Set(el)
		}
	}

	// Logging subsystem owns the ETW handler + filelog writer and installs
	// itself as slog.Default. Deferred Stop runs after svc.Run returns so
	// SCM-stop log lines still land. See internal/logging/subsystem_windows.go.
	logSub := logging.NewSubsystem(dc.DefaultDataDir()+`\drainctl.log`, 7, fileLevel, etwLevel)
	defer logSub.Stop()

	return svc.Run(dc.ServiceName, &drainService{fileLevel: fileLevel, etwLevel: etwLevel})
}

// newRetentionProvider returns a closure the retention worker calls at the
// start of every pass to fetch current per-tier retention windows. Reloading
// config from disk keeps the worker honoring live edits to config.json
// without a separate hot-reload path; if the reload fails the startup values
// are reused so a transient disk error cannot widen retention.
func newRetentionProvider(startup *dc.Config) func() telemetry.RetentionSettings {
	startupMetrics := startup.Retention.MetricsDays
	startupAudit := startup.Retention.AuditDays
	return func() telemetry.RetentionSettings {
		c, err := dc.LoadConfig()
		if err != nil {
			slog.Warn("telemetry: retention provider load config failed, using startup values",
				"error", err, "metrics_days", startupMetrics, "audit_days", startupAudit)
			return telemetry.RetentionSettings{
				MetricsDays: startupMetrics,
				AuditDays:   startupAudit,
			}
		}
		return telemetry.RetentionSettings{
			MetricsDays: c.Retention.MetricsDays,
			AuditDays:   c.Retention.AuditDays,
		}
	}
}

// waitWithTimeout invokes wait (typically a Subsystem.Stop) on a
// goroutine and blocks until either it returns or timeout elapses. A log
// warning is emitted when the bound is exceeded so operators see a stuck
// worker rather than a silent service-stop hang. label distinguishes
// call sites in the warning.
func waitWithTimeout(label string, wait func(), timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn(label+": did not exit within shutdown budget",
			"timeout", timeout, slog.Int("event_id", EvtGenericWarning))
	}
}

// isLocalDashboard returns true if the dashboard URL points to this machine.
func isLocalDashboard(dashURL string) bool {
	u, err := url.Parse(dashURL)
	if err != nil {
		return false
	}
	local, err := os.Hostname()
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), local)
}
