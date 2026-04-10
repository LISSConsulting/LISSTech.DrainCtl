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
	"golang.org/x/sys/windows/svc/eventlog"
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

// Event IDs matching assets/drainctl.mc message file.
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
)

// elogHandler implements slog.Handler, routing slog records to the Windows
// Event Log using the generic message event IDs (1099/2099/3099).
type elogHandler struct {
	elog  *eventlog.Log
	level *slog.LevelVar
}

func newElogHandler(elog *eventlog.Log, level *slog.LevelVar) *elogHandler {
	return &elogHandler{elog: elog, level: level}
}

func (h *elogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *elogHandler) Handle(_ context.Context, r slog.Record) error {
	var buf strings.Builder
	if r.Message != "" {
		buf.WriteString(r.Message)
	}
	r.Attrs(func(a slog.Attr) bool {
		buf.WriteString(" ")
		buf.WriteString(a.Key)
		buf.WriteString("=")
		fmt.Fprintf(&buf, "%v", a.Value.Any())
		return true
	})
	msg := buf.String()
	switch {
	case r.Level >= slog.LevelError:
		_ = h.elog.Error(EvtGenericError, msg)
	case r.Level >= slog.LevelWarn:
		_ = h.elog.Warning(EvtGenericWarning, msg)
	default:
		_ = h.elog.Info(EvtGenericInfo, msg)
	}
	return nil
}

func (h *elogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *elogHandler) WithGroup(_ string) slog.Handler      { return h }

// drainService implements svc.Handler.
type drainService struct {
	elog *eventlog.Log
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
			slog.Error("PANIC", "panic", fmt.Sprintf("%v", r), "stack", stack)
			_ = s.elog.Error(EvtServiceError, fmt.Sprintf("Service panicked: %v\n%s", r, stack))
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
	notifyTargets := fullCfg.Notifications
	notifyState := &dc.NotifyState{
		LastAlertNotify:       make(map[string]time.Time),
		LastSessionWarnNotify: make(map[string]time.Time),
	}

	slog.Info("service=starting", "version", dc.Version, "grace", cfg.GracePeriod, "poll", cfg.PollInterval, "retention_days", cfg.RetentionDays, "fetch_interval", dashCfg.FetchInterval)

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

	// Auto-register with dashboard if URL is configured (or discovered).
	var useRemoteConfig bool
	var lastConfigFetch time.Time
	dashConfigFailures := 0
	dashRegistered := false

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)

		// If the dashboard runs in this process, register directly (no HTTP/SSPI).
		if dashState != nil && isLocalDashboard(dashCfg.URL) {
			hostname, _ := os.Hostname()
			if hostname != "" {
				dashState.Register(hostname)
				dashRegistered = true
				slog.Info("dashboard=self-registered", "host", hostname)
			}
		}
		if !dashRegistered {
			dashRegistered = registerWithDashboard(&dashCfg)
		}

		// Fetch notification config from dashboard (replaces local targets).
		var remote *dashboard.RemoteNotifyConfig
		var fetchErr error
		if dashState != nil {
			slog.Debug("dashboard config fetch (local)")
			remote, fetchErr = dashboard.GetNotifyConfig()
		} else {
			slog.Debug("dashboard config fetch (remote)", "url", dashCfg.URL)
			remote, fetchErr = dashboard.FetchNotifyConfig(dashCfg.URL)
		}
		if fetchErr != nil {
			slog.Warn("dashboard: failed to fetch notify config, using local targets", "error", fetchErr)
		} else {
			dashConfigFailures = 0
			useRemoteConfig = true
			oldPerfCfg := cfg.Performance
			applyRemoteConfig(remote, &cfg, &notifyTargets)
			handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
			syncPerfCollector(oldPerfCfg, cfg.Performance, &perfCollector, &perfTriggerState, &handler.lastPerf)
			slog.Info("dashboard=notify-config-fetched", "targets", len(remote.Notifications), "threshold", remote.SessionWarningThreshold, "grace", remote.GracePeriod)
		}
	}

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(30 * time.Second)
	defer flushTicker.Stop()

	// Run initial check.
	svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions, s.elog)

	// Report running.
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	slog.Info("service=running")
	_ = s.elog.Info(EvtServiceStarted, fmt.Sprintf(
		"Service started.\nVersion: %s\nPoll interval: %s\nGrace period: %s\nRetention: %d days",
		dc.Version, cfg.PollInterval, cfg.GracePeriod, cfg.RetentionDays))

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				slog.Info("service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				_ = st.Flush()
				_ = s.elog.Info(EvtServiceStopped, "Service stopped.")
				slog.Info("service=stopped")
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			slog.Info("trigger=registry_change")
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions, s.elog)
			_ = st.Flush() // immediate flush on change

		case <-pollTicker.C:
			// Retry registration if the startup attempt failed (e.g. dashboard was
			// not yet available). Once registered, dashRegistered stays true and
			// this branch is skipped for the lifetime of the service. Forcing
			// lastConfigFetch to zero ensures a config fetch follows immediately.
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(&dashCfg) {
					dashRegistered = true
					lastConfigFetch = time.Time{} // trigger config fetch on this tick
					dashConfigFailures = 0
				}
			}

			// Periodically re-fetch notification config from dashboard.
			// Uses wall-clock time so the interval is independent of PollInterval
			// changes at runtime. The interval doubles on each consecutive failure
			// (exponential backoff) so a downed dashboard does not generate log
			// spam every poll.
			if dashCfg.URL != "" && time.Since(lastConfigFetch) >= backoffDuration(dashCfg.FetchInterval, dashConfigFailures) {
				lastConfigFetch = time.Now()
				slog.Debug("dashboard config fetch", "url", dashCfg.URL)
				var cfgRemote *dashboard.RemoteNotifyConfig
				var cfgErr error
				if dashState != nil {
					cfgRemote, cfgErr = dashboard.GetNotifyConfig()
				} else {
					cfgRemote, cfgErr = dashboard.FetchNotifyConfig(dashCfg.URL)
				}
				if cfgErr != nil {
					dashConfigFailures++
					nextIn := backoffDuration(dashCfg.FetchInterval, dashConfigFailures)
					slog.Warn("dashboard: notify config refresh failed, using cached", "error", cfgErr, "next_retry_in", nextIn.Round(time.Minute))
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
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions, s.elog)

		case <-configCh:
			newFullCfg, err := dc.LoadConfig()
			if err != nil {
				slog.Warn("config reload failed", "error", err)
				continue
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
				svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, dashState, evtSub, perfCollector, perfTriggerState, &handler.lastPerf, &handler.lastSessions, s.elog)
			}
			_ = s.elog.Info(EvtConfigReloaded, "Configuration reloaded from config.json.")

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
	elog, err := eventlog.Open(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = elog.Close() }()

	fw, err := filelog.New(dc.DefaultDataDir()+`\drainctl.log`, 10<<20, 7) // 10 MB, 7 old files
	fileLevel := &slog.LevelVar{}
	fileLevel.Set(slog.LevelDebug)
	elogLevel := &slog.LevelVar{}
	elogLevel.Set(slog.LevelInfo)
	if err != nil {
		// File log unavailable — fall back to event log only.
		slog.Warn("file log unavailable, using event log only", "error", err)
		elogH := newElogHandler(elog, elogLevel)
		slog.SetDefault(slog.New(elogH))
		_ = elog.Warning(EvtGenericWarning, fmt.Sprintf("File log unavailable: %v", err))
		return svc.Run(dc.ServiceName, &drainService{elog: elog})
	}
	defer func() { _ = fw.Close() }()

	fileH := logging.NewFileHandler(fw, fileLevel)
	elogH := newElogHandler(elog, elogLevel)
	slog.SetDefault(slog.New(logging.NewMultiHandler(fileH, elogH)))
	return svc.Run(dc.ServiceName, &drainService{elog: elog})
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
