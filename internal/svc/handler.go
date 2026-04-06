//go:build windows

package svc

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/filelog"
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
	configFetchBase = 5 * time.Minute   // baseline re-fetch interval
	configFetchMax  = 160 * time.Minute // ceiling (~maxConfigFetchInterval * 30s)
)

// backoffDuration returns the time interval to wait before the next dashboard
// config fetch attempt. failures is the count of consecutive failures; the
// interval doubles per failure up to configFetchMax.
func backoffDuration(failures int) time.Duration {
	if failures <= 0 {
		return configFetchBase
	}
	shift := failures
	if shift > 5 {
		shift = 5 // cap doubling at 2^5 = 32×
	}
	d := configFetchBase << shift
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

// EventLogLogger returns a LogFunc that writes to the Windows Event Log
// using generic event IDs (1099/2099/3099) for structured log messages.
func EventLogLogger(elog *eventlog.Log) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		msg := strings.Join(fields, " ")
		switch l {
		case dc.LvlERR:
			_ = elog.Error(EvtGenericError, msg)
		case dc.LvlWRN:
			_ = elog.Warning(EvtGenericWarning, msg)
		default:
			_ = elog.Info(EvtGenericInfo, msg)
		}
	}
}

// FileLogger returns a LogFunc that writes timestamped structured lines to w.
// All levels including LvlDBG are written.
func FileLogger(w io.Writer) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		_, _ = fmt.Fprintf(w, "%s %s %s\n", ts, l, strings.Join(fields, " "))
	}
}

// MultiLogger fans out log calls to both an event log sink (INF+ only)
// and a file log sink (all levels including DBG).
func MultiLogger(eventLog, fileLog dc.LogFunc) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		fileLog(l, fields...)
		if l != dc.LvlDBG {
			eventLog(l, fields...)
		}
	}
}

// drainService implements svc.Handler.
type drainService struct {
	log  dc.LogFunc
	elog *eventlog.Log
}

// serviceHandler is the pipe handler bridge between the service state
// and the named pipe server.
// cfg is accessed from two goroutines (Execute loop + pipe server) and
// must be read/written via atomic.Pointer to avoid a data race.
type serviceHandler struct {
	store *store.MemAuditStore
	cfg   atomic.Pointer[dc.ServiceConfig]
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

	// Session tracking.
	if sess := dc.GetSessionSummary(); sess != nil {
		res.Sessions = sess
	}

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

// Execute is the Windows service main loop.
func (s *drainService) Execute(args []string, r <-chan svc.ChangeRequest, statusCh chan<- svc.Status) (bool, uint32) {
	statusCh <- svc.Status{State: svc.StartPending, WaitHint: 10000}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load config from config.json (migrates from registry if needed).
	fullCfg, err := dc.LoadConfig(s.log)
	if err != nil {
		s.log(dc.LvlERR, fmt.Sprintf("service=failed error=%q", err))
		return false, 1
	}

	cfg := fullCfg.ToServiceConfig()
	dashCfg := fullCfg.ToDashboardConfig()
	notifyTargets := fullCfg.Notifications
	notifyState := &dc.NotifyState{
		LastAlertNotify:       make(map[string]time.Time),
		LastSessionWarnNotify: make(map[string]time.Time),
	}

	s.log(dc.LvlINF, fmt.Sprintf("service=starting version=%s grace=%s poll=%s retention=%dd",
		dc.Version, cfg.GracePeriod, cfg.PollInterval, cfg.RetentionDays))

	// Open in-memory audit store.
	st, err := store.OpenMemAuditStore(cfg.AuditPath, s.log)
	if err != nil {
		s.log(dc.LvlERR, fmt.Sprintf("service=failed error=%q", err))
		return false, 1
	}
	defer func() { _ = st.Close() }()

	// Prune on startup.
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	if pruned, err := st.Prune(retention); err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "startup prune failed", fmt.Sprintf("error=%q", err))
	} else if pruned > 0 {
		s.log(dc.LvlINF, fmt.Sprintf("startup_prune=%d", pruned))
	}

	// Start registry watcher.
	regCh, err := watcher.WatchDrainModeKey(ctx, s.log)
	if err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "registry watcher failed, polling only", fmt.Sprintf("error=%q", err))
		regCh = make(chan struct{}) // never fires
	}

	// Start config file watcher for hot-reload.
	configCh, err := watcher.WatchConfigFile(ctx, dc.DefaultConfigPath(), s.log)
	if err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "config watcher failed", fmt.Sprintf("error=%q", err))
		configCh = make(chan struct{}) // never fires
	}

	// Start event log subscriber for change attribution.
	var evtSub *watcher.EventSubscriber
	if sub, err := watcher.NewEventSubscriber(ctx, s.log); err != nil {
		dc.LogMsg(s.log, dc.LvlWRN, "event subscriber failed, attribution via wevtutil fallback", fmt.Sprintf("error=%q", err))
	} else {
		evtSub = sub
	}

	// Start pipe server.
	handler := &serviceHandler{store: st}
	handler.cfg.Store(&cfg)
	go pipe.ServePipe(ctx, handler, s.log)

	// Start dashboard if enabled.
	if dashCfg.Enabled {
		_, err := dashboard.StartDashboard(ctx, dashCfg, dc.DefaultDataDir(), s.log)
		if err != nil {
			dc.LogMsg(s.log, dc.LvlWRN, "dashboard failed to start", fmt.Sprintf("error=%q", err))
		} else {
			s.log(dc.LvlINF, fmt.Sprintf("dashboard=started port=%d", dashCfg.Port))
		}
	}

	// Auto-discover dashboard via SRV if URL is not explicitly configured.
	if dashCfg.URL == "" {
		if discovered := dashboard.DiscoverDashboardURL(s.log); discovered != "" {
			dashCfg.URL = discovered
		}
	}

	// Auto-register with dashboard if URL is configured (or discovered).
	// When the dashboard URL is set, notification config is pulled from
	// the dashboard and replaces local targets. On fetch failure the
	// service keeps using the last successfully fetched targets (or falls
	// back to local config.json targets if no fetch ever succeeded).
	var useRemoteConfig bool
	var lastConfigFetch time.Time // zero → fetch on first poll
	dashConfigFailures := 0       // consecutive dashboard config-fetch failures
	dashRegistered := false       // true once Register succeeds; retried on each poll until it does

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)
		dashRegistered = registerWithDashboard(&dashCfg, s.log)

		// Fetch notification config from dashboard (replaces local targets).
		// Independent of registration — global config, not host-specific.
		if remote, err := dashboard.FetchNotifyConfig(dashCfg.URL, s.log); err != nil {
			dc.LogMsg(s.log, dc.LvlWRN, "dashboard: failed to fetch notify config, using local targets", fmt.Sprintf("error=%q", err))
		} else {
			dashConfigFailures = 0
			useRemoteConfig = true
			applyRemoteConfig(remote, &cfg, &notifyTargets)
			handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
			s.log(dc.LvlINF, fmt.Sprintf("dashboard=notify-config-fetched targets=%d threshold=%d grace=%d",
				len(remote.Notifications), remote.SessionWarningThreshold, remote.GracePeriod))
		}
	}

	// Timers.
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()

	flushTicker := time.NewTicker(30 * time.Second)
	defer flushTicker.Stop()

	// Run initial check.
	svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, evtSub, s.log, s.elog)

	// Report running.
	statusCh <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	s.log(dc.LvlOK, "service=running")
	_ = s.elog.Info(EvtServiceStarted, fmt.Sprintf(
		"Service started.\nVersion: %s\nPoll interval: %s\nGrace period: %s\nRetention: %d days",
		dc.Version, cfg.PollInterval, cfg.GracePeriod, cfg.RetentionDays))

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s.log(dc.LvlINF, "service=stopping")
				statusCh <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				_ = st.Flush()
				_ = s.elog.Info(EvtServiceStopped, "Service stopped.")
				s.log(dc.LvlOK, "service=stopped")
				return false, 0
			case svc.Interrogate:
				statusCh <- c.CurrentStatus
			}

		case <-regCh:
			s.log(dc.LvlINF, "trigger=registry_change")
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, evtSub, s.log, s.elog)
			_ = st.Flush() // immediate flush on change

		case <-pollTicker.C:
			// Retry registration if the startup attempt failed (e.g. dashboard was
			// not yet available). Once registered, dashRegistered stays true and
			// this branch is skipped for the lifetime of the service. Forcing
			// lastConfigFetch to zero ensures a config fetch follows immediately.
			if dashCfg.URL != "" && !dashRegistered {
				if registerWithDashboard(&dashCfg, s.log) {
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
			if dashCfg.URL != "" && time.Since(lastConfigFetch) >= backoffDuration(dashConfigFailures) {
				lastConfigFetch = time.Now()
				s.log(dc.LvlDBG, "msg=\"dashboard config fetch\"", fmt.Sprintf("url=%s", dashCfg.URL))
				if remote, err := dashboard.FetchNotifyConfig(dashCfg.URL, s.log); err != nil {
					dashConfigFailures++
					nextIn := backoffDuration(dashConfigFailures)
					dc.LogMsg(s.log, dc.LvlWRN, "dashboard: notify config refresh failed, using cached",
						fmt.Sprintf("error=%q next_retry_in=%s", err, nextIn.Round(time.Minute)))
				} else {
					dashConfigFailures = 0
					useRemoteConfig = true
					applyRemoteConfig(remote, &cfg, &notifyTargets)
					handler.cfg.Store(&cfg) // sync updated GracePeriod/threshold to pipe handler
				}
			}
			svcRunCheck(st, &cfg, notifyTargets, notifyState, &dashCfg, evtSub, s.log, s.elog)

		case <-configCh:
			newFullCfg, err := dc.LoadConfig(s.log)
			if err != nil {
				dc.LogMsg(s.log, dc.LvlWRN, "config reload failed", fmt.Sprintf("error=%q", err))
				continue
			}
			newCfg := newFullCfg.ToServiceConfig()
			if newCfg.PollInterval != cfg.PollInterval {
				pollTicker.Reset(newCfg.PollInterval)
			}
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
			if newDashCfg.Enabled && !dashCfg.Enabled {
				_, err := dashboard.StartDashboard(ctx, newDashCfg, dc.DefaultDataDir(), s.log)
				if err != nil {
					dc.LogMsg(s.log, dc.LvlWRN, "dashboard failed to start on config reload", fmt.Sprintf("error=%q", err))
				} else {
					s.log(dc.LvlINF, fmt.Sprintf("dashboard=started port=%d (late start)", newDashCfg.Port))
				}
			}

			dashCfg = newDashCfg
			s.log(dc.LvlINF, "config=reloaded")
			_ = s.elog.Info(EvtConfigReloaded, "Configuration reloaded from config.json.")

		case <-flushTicker.C:
			_ = st.FlushIfDirty()
		}
	}
}

// registerWithDashboard registers this host with the dashboard and performs
// auto-pin if enabled. Returns true on success. Safe to call multiple times;
// the dashboard treats re-registration as a no-op for already-known hosts.
func registerWithDashboard(dashCfg *dc.DashboardConfig, log dc.LogFunc) bool {
	log(dc.LvlDBG, "msg=\"dashboard registration attempt\"", fmt.Sprintf("url=%s", dashCfg.URL))
	regResult, err := dashboard.Register(dashCfg.URL, log)
	if err != nil {
		dc.LogMsg(log, dc.LvlWRN, "dashboard: registration failed, will retry on next poll", fmt.Sprintf("error=%q", err))
		return false
	}
	log(dc.LvlINF, "dashboard=registered")
	// Auto-pin: save the dashboard's TLS fingerprint if enabled and we don't have one yet.
	if dashCfg.AutoPin && dashCfg.TLSFingerprint == "" && regResult.TLSFingerprint != "" {
		dashCfg.TLSFingerprint = regResult.TLSFingerprint
		dashboard.InitDashClient(dashCfg.TLSFingerprint)
		log(dc.LvlINF, fmt.Sprintf("dashboard=auto-pinned fingerprint=%s", dashCfg.TLSFingerprint))
		// Persist to config.json so pinning survives restarts.
		if fileCfg, err := dc.LoadConfig(log); err == nil {
			fileCfg.Dashboard.TLSFingerprint = dashCfg.TLSFingerprint
			if err := dc.SaveConfig(fileCfg, log); err != nil {
				dc.LogMsg(log, dc.LvlWRN, "dashboard: failed to save fingerprint to config", fmt.Sprintf("error=%q", err))
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
	if err != nil {
		// File log failure is non-fatal — fall back to event log only.
		log := EventLogLogger(elog)
		log(dc.LvlWRN, fmt.Sprintf("msg=%q error=%q", "file log unavailable, using event log only", err))
		return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
	}
	defer func() { _ = fw.Close() }()

	log := MultiLogger(EventLogLogger(elog), FileLogger(fw))
	return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
}
