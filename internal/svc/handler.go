//go:build windows

package svc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/filelog"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
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
	etw       *logging.ETWHandler
	fileLevel *slog.LevelVar // min level for the file sink
	etwLevel  *slog.LevelVar // min level for the ETW sink
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
// cfg is accessed from two goroutines (Execute loop + pipe server) and
// must be read/written via atomic.Pointer to avoid a data race.
type serviceHandler struct {
	store        *store.MemAuditStore
	cfg          atomic.Pointer[dc.ServiceConfig]
	dashState    *dashboard.ServerState // nil if dashboard not enabled
	lastPerf     atomic.Pointer[dc.PerfSnapshot]
	lastSessions atomic.Pointer[dc.SessionSummary]
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

	// State duration from in-memory store.
	if since := h.store.StateSince(state.Mode); since != nil {
		s := since.Local()
		res.StateSince = &s
		dur := time.Since(*since).Seconds()
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
	if last := h.store.LastObservation(); last != nil && last.DrainMode != state.Mode {
		res.Transition = true
		res.TransitionFrom = last.DrainMode.String()
	}

	return res
}

func (h *serviceHandler) HandleHistory(limit int, changesOnly bool) []dc.AuditRecord {
	if changesOnly {
		return h.store.Changes(limit)
	}
	return h.store.History(limit)
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
	fullCfg, err := dc.LoadConfig()
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}

	cfg := fullCfg.ToServiceConfig()
	dashCfg := fullCfg.ToDashboardConfig()

	// Apply Go runtime soft memory limit. Makes the GC more aggressive about
	// returning pages to the OS, which matters on memory-constrained RDS hosts.
	memLimitMB := fullCfg.MemoryLimitMB
	debug.SetMemoryLimit(int64(memLimitMB) << 20)

	notifyTargets := fullCfg.Notifications
	notifyState := &dc.NotifyState{
		LastAlertNotify:       make(map[string]time.Time),
		LastSessionWarnNotify: make(map[string]time.Time),
	}

	slog.Info("service=starting", "version", dc.Version, "grace", cfg.GracePeriod, "poll", cfg.PollInterval, "retention_days", cfg.RetentionDays, "fetch_interval", dashCfg.FetchInterval, "memory_limit_mb", memLimitMB)

	// Open in-memory audit store.
	st, err := store.OpenMemAuditStore(cfg.AuditPath)
	if err != nil {
		slog.Error("service=failed", "error", err)
		return false, 1
	}
	defer func() { _ = st.Close() }()

	// Prune on startup.
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	if pruned, err := st.Prune(retention); err != nil {
		slog.Warn("startup prune failed", "error", err)
	} else if pruned > 0 {
		slog.Info("startup_prune", "count", pruned)
	}

	// Start registry watcher.
	regCh, err := watcher.WatchDrainModeKey(ctx)
	if err != nil {
		slog.Warn("registry watcher failed, polling only", "error", err)
		regCh = make(chan struct{}) // never fires
	}

	// Start config file watcher for hot-reload.
	configCh, err := watcher.WatchConfigFile(ctx, dc.DefaultConfigPath())
	if err != nil {
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

	// Start performance monitoring if enabled.
	var perfCollector *perfmon.Collector
	var perfTriggerState *perfmon.PerfTriggerState
	if cfg.Performance.Enabled {
		pc, err := perfmon.Open(cfg.Performance)
		if err != nil {
			slog.Warn("performance monitoring failed to start", "error", err)
		} else {
			if err := pc.Prime(); err != nil {
				slog.Warn("performance monitoring prime failed", "error", err)
				pc.Close()
			} else {
				perfCollector = pc
				perfTriggerState = &perfmon.PerfTriggerState{}
				slog.Info("perfmon=started")
			}
		}
	}
	defer func() {
		if perfCollector != nil {
			perfCollector.Close()
		}
	}()

	// Start pipe server.
	handler := &serviceHandler{store: st}
	handler.cfg.Store(&cfg)
	go pipe.ServePipe(ctx, handler)

	// Start dashboard if enabled.
	var dashState *dashboard.ServerState
	if dashCfg.Enabled {
		st, err := dashboard.StartDashboard(ctx, dashCfg, dc.DefaultDataDir())
		if err != nil {
			slog.Warn("dashboard failed to start", "error", err)
		} else {
			dashState = st
			handler.dashState = st
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
	dashConfigFailures := 0
	dashRegistered := false

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)

		// Local self-registration is instant (no network) — safe to do here.
		if dashState != nil && isLocalDashboard(dashCfg.URL) {
			hostname, _ := os.Hostname()
			if hostname != "" {
				dashState.Register(hostname)
				dashRegistered = true
				slog.Info("dashboard=self-registered", "host", hostname)
			}
		}
		// Remote registration + config fetch happen on the first poll tick
		// (lastConfigFetch is zero, dashRegistered is false).
	}

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(30 * time.Second)
	defer flushTicker.Stop()

	// Report running immediately so the SCM doesn't wait on network I/O.
	// Dashboard registration + config fetch happen asynchronously on the
	// first poll tick (fired immediately below).
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	// Run initial check.
	svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions)
	slog.Info("service=running",
		slog.Int("event_id", EvtServiceStarted),
		"version", dc.Version,
		"poll", cfg.PollInterval,
		"grace", cfg.GracePeriod,
		"retention_days", cfg.RetentionDays)

	// Fire dashboard registration + config fetch shortly after startup
	// instead of waiting for the first full poll interval.
	var dashBootstrap <-chan time.Time
	if dashCfg.URL != "" && !dashRegistered {
		dashBootstrap = time.After(5 * time.Second)
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				slog.Info("service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				_ = st.Flush()
				slog.Info("service=stopped", slog.Int("event_id", EvtServiceStopped))
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			slog.Info("trigger=registry_change")
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions)
			_ = st.Flush() // immediate flush on change

		case <-dashBootstrap:
			// First async dashboard registration attempt after startup.
			dashBootstrap = nil // one-shot
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(&dashCfg) {
					dashRegistered = true
					lastConfigFetch = time.Time{} // trigger config fetch on next poll
					dashConfigFailures = 0
				}
			}

		case <-pollTicker.C:
			// Retry registration if a previous attempt failed. Once registered,
			// dashRegistered stays true and this branch is skipped.
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(&dashCfg) {
					dashRegistered = true
					lastConfigFetch = time.Time{} // trigger config fetch on this tick
					dashConfigFailures = 0
				}
			}

			// Periodically re-fetch settings from dashboard.
			// Uses wall-clock time so the interval is independent of PollInterval
			// changes at runtime. The interval doubles on each consecutive failure
			// (exponential backoff) so a downed dashboard does not generate log
			// spam every poll.
			if dashCfg.URL != "" && time.Since(lastConfigFetch) >= backoffDuration(dashCfg.FetchInterval, dashConfigFailures) {
				lastConfigFetch = time.Now()
				slog.Debug("dashboard config fetch", "url", dashCfg.URL)
				var cfgRemote *dashboard.RemoteSettings
				var cfgErr error
				if dashState != nil {
					cfgRemote, cfgErr = dashboard.GetSettings()
				} else {
					cfgRemote, cfgErr = dashboard.FetchSettings(dashCfg.URL)
				}
				if cfgErr != nil {
					dashConfigFailures++
					nextIn := backoffDuration(dashCfg.FetchInterval, dashConfigFailures)
					slog.Warn("dashboard: settings refresh failed, using cached", "error", cfgErr, "next_retry_in", nextIn.Round(time.Minute))
				} else {
					dashConfigFailures = 0
					useRemoteConfig = true
					slog.Debug("diag: step=apply_remote_config")
					oldPerfCfg := cfg.Performance
					applyRemoteConfig(cfgRemote, &cfg, &notifyTargets)
					handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
					slog.Debug("diag: step=sync_perf_collector", "old_enabled", oldPerfCfg.Enabled, "new_enabled", cfg.Performance.Enabled)
					syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf)
					slog.Debug("diag: step=sync_perf_done")
				}
			}
			slog.Debug("diag: step=svc_run_check")
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions)

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
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
			oldPerfCfg := cfg.Performance
			cfg = newCfg
			handler.cfg.Store(&cfg)
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
				if st, err := dashboard.StartDashboard(ctx, newDashCfg, dc.DefaultDataDir()); err != nil {
					slog.Warn("dashboard failed to start on config reload", "error", err)
				} else {
					dashState = st
					handler.dashState = st
					slog.Info("dashboard=started", "port", newDashCfg.Port, "reason", "late start")
					if isLocalDashboard(newDashCfg.URL) {
						if h, _ := os.Hostname(); h != "" {
							dashState.Register(h)
							dashRegistered = true
							slog.Info("dashboard=self-registered", "host", h)
						}
					}
				}
			}

			// Preserve SRV-discovered URL if the config file doesn't set one.
			if newDashCfg.URL == "" && dashCfg.URL != "" {
				newDashCfg.URL = dashCfg.URL
			}
			dashCfg = newDashCfg
			slog.Info("config=reloaded")

			// Sync performance collector with new config. Placed after dashCfg
			// update so the immediate svcRunCheck reports to the current URL.
			if syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf) {
				svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions)
			}
			slog.Info("config=reloaded-etw", slog.Int("event_id", EvtConfigReloaded))

		case <-flushTicker.C:
			_ = st.FlushIfDirty()
		}
	}
}

// syncPerfCollector reconciles the running performance collector with a config
// change. It tears down the old collector (if any), clears the cached snapshot,
// and starts a new collector when the new config enables perfmon.
// Returns true if the config actually changed (and action was taken).
func syncPerfCollector(
	oldPerf, newPerf dc.PerformanceConfig,
	perfCollector **perfmon.Collector,
	perfTriggerState **perfmon.PerfTriggerState,
	lastPerf *atomic.Pointer[dc.PerfSnapshot],
) bool {
	if oldPerf == newPerf {
		return false
	}

	// Tear down old collector.
	if *perfCollector != nil {
		(*perfCollector).Close()
		*perfCollector = nil
		*perfTriggerState = nil
	}

	// Always clear cached snapshot so stale data is never served.
	lastPerf.Store(nil)

	if newPerf.Enabled {
		pc, err := perfmon.Open(newPerf)
		if err != nil {
			slog.Warn("perfmon start failed on config change", "error", err)
			return true
		}
		if err := pc.Prime(); err != nil {
			slog.Warn("perfmon prime failed on config change", "error", err)
			pc.Close()
			return true
		}
		*perfCollector = pc
		*perfTriggerState = &perfmon.PerfTriggerState{}
		slog.Info("perfmon=restarted")
	} else {
		slog.Info("perfmon=stopped")
	}
	return true
}

// registerWithDashboard registers this host with the dashboard and performs
// auto-pin if enabled. Returns true on success. Safe to call multiple times;
// the dashboard treats re-registration as a no-op for already-known hosts.
func registerWithDashboard(dashCfg *dc.DashboardConfig) bool {
	slog.Debug("dashboard registration attempt", "url", dashCfg.URL)
	regResult, err := dashboard.Register(dashCfg.URL)
	if err != nil {
		slog.Warn("dashboard: registration failed, will retry on next poll", "error", err)
		return false
	}
	slog.Info("dashboard=registered")
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

	// Create the ETW handler.  If the manifest has not been installed yet
	// (e.g. first-run before the MSI registers the provider), ETWHandler
	// starts in degraded mode and writes are silently dropped until the
	// provider is registered and the service is restarted.
	etwH := logging.NewETWHandler(etwLevel)

	fw, err := filelog.New(dc.DefaultDataDir()+`\drainctl.log`, 10<<20, 7) // 10 MB, 7 old files
	if err != nil {
		// File log unavailable — fall back to ETW only.
		slog.Warn("file log unavailable, using ETW only", "error", err)
		slog.SetDefault(slog.New(etwH))
		return svc.Run(dc.ServiceName, &drainService{etw: etwH, fileLevel: fileLevel, etwLevel: etwLevel})
	}
	defer func() { _ = fw.Close() }()

	fileH := logging.NewFileHandler(fw, fileLevel)
	slog.SetDefault(slog.New(logging.NewMultiHandler(fileH, etwH)))
	return svc.Run(dc.ServiceName, &drainService{etw: etwH, fileLevel: fileLevel, etwLevel: etwLevel})
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
