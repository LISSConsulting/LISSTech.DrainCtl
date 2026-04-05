//go:build windows

package svc

import (
	"context"
	"fmt"
	"strings"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// configFetchInterval is the baseline number of poll ticks between dashboard
// notification-config refreshes (~5 min at the default 30 s poll interval).
// The actual interval grows exponentially after consecutive failures.
const (
	configFetchInterval    = 10  // baseline ticks
	maxConfigFetchInterval = 320 // ceiling ticks (~160 min at 30 s)
)

// backoffTicks returns the number of poll ticks to wait before the next
// dashboard config fetch attempt.  failures is the count of consecutive
// failures; the interval doubles per failure up to maxConfigFetchInterval.
func backoffTicks(failures int) int {
	if failures <= 0 {
		return configFetchInterval
	}
	shift := failures
	if shift > 5 {
		shift = 5 // cap doubling at 2^5 = 32×
	}
	n := configFetchInterval << shift
	if n > maxConfigFetchInterval {
		return maxConfigFetchInterval
	}
	return n
}

// Event IDs matching assets/drainctl.mc message file.
const (
	EvtServiceStarted = 1000
	EvtServiceStopped = 1001
	EvtCheckHealthy   = 1002
	EvtConfigReloaded = 1003
	EvtTransition     = 1004
	EvtCheckGrace     = 2000
	EvtCheckAlert     = 3000
	EvtRegistryFailed = 3001
	EvtServiceError   = 3002
)

// EventLogLogger returns a LogFunc that writes to the Windows Event Log
// using generic event ID 1/2/3 for structured log messages.
func EventLogLogger(elog *eventlog.Log) dc.LogFunc {
	return func(l dc.Level, fields ...string) {
		msg := strings.Join(fields, " ")
		switch l {
		case dc.LvlERR:
			_ = elog.Error(3, msg)
		case dc.LvlWRN:
			_ = elog.Warning(2, msg)
		default:
			_ = elog.Info(1, msg)
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
type serviceHandler struct {
	store *store.MemAuditStore
	cfg   *dc.ServiceConfig
}

func (h *serviceHandler) HandleStatus(gracePeriod time.Duration) *dc.CheckResult {
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

	gp := h.cfg.GracePeriod
	if gracePeriod > 0 {
		gp = gracePeriod
	}

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

	res.Status, res.Message, res.ExitCode = classifyState(drainActive, stateDur, gp)

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
	notifyState := &dc.NotifyState{LastAlertNotify: make(map[string]time.Time)}

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
	handler := &serviceHandler{store: st, cfg: &cfg}
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
	pollsSinceConfigFetch := 0
	dashConfigFailures := 0 // consecutive dashboard config-fetch failures

	if dashCfg.URL != "" {
		dashboard.InitDashClient(dashCfg.TLSFingerprint)
		regResult, regErr := dashboard.Register(dashCfg.URL, s.log)
		if regErr != nil {
			dc.LogMsg(s.log, dc.LvlWRN, "dashboard registration failed (will retry on report)", fmt.Sprintf("error=%q", regErr))
		} else {
			s.log(dc.LvlINF, "dashboard=registered")
			// Auto-pin: save the dashboard's TLS fingerprint if enabled and we don't have one yet.
			if dashCfg.AutoPin && dashCfg.TLSFingerprint == "" && regResult.TLSFingerprint != "" {
				dashCfg.TLSFingerprint = regResult.TLSFingerprint
				dashboard.InitDashClient(dashCfg.TLSFingerprint)
				s.log(dc.LvlINF, fmt.Sprintf("dashboard=auto-pinned fingerprint=%s", dashCfg.TLSFingerprint))
				// Persist to config.json so pinning survives restarts.
				if fileCfg, err := dc.LoadConfig(s.log); err == nil {
					fileCfg.Dashboard.TLSFingerprint = dashCfg.TLSFingerprint
					if err := dc.SaveConfig(fileCfg, s.log); err != nil {
						dc.LogMsg(s.log, dc.LvlWRN, "dashboard: failed to save fingerprint to config", fmt.Sprintf("error=%q", err))
					}
				}
			}
		}

		// Fetch notification config from dashboard (replaces local targets).
		if remote, err := dashboard.FetchNotifyConfig(dashCfg.URL, s.log); err != nil {
			dc.LogMsg(s.log, dc.LvlWRN, "dashboard: failed to fetch notify config, using local targets", fmt.Sprintf("error=%q", err))
		} else {
			dashConfigFailures = 0
			useRemoteConfig = true
			applyRemoteConfig(remote, &cfg, &notifyTargets)
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
			// Periodically re-fetch notification config from dashboard.
			// The interval doubles on each consecutive failure (exponential backoff)
			// so a downed dashboard does not generate log spam every poll.
			if dashCfg.URL != "" {
				pollsSinceConfigFetch++
				if pollsSinceConfigFetch >= backoffTicks(dashConfigFailures) {
					pollsSinceConfigFetch = 0
					if remote, err := dashboard.FetchNotifyConfig(dashCfg.URL, s.log); err != nil {
						dashConfigFailures++
						nextIn := backoffTicks(dashConfigFailures)
						dc.LogMsg(s.log, dc.LvlWRN, "dashboard: notify config refresh failed, using cached",
							fmt.Sprintf("error=%q next_retry_polls=%d", err, nextIn))
					} else {
						dashConfigFailures = 0
						useRemoteConfig = true
						applyRemoteConfig(remote, &cfg, &notifyTargets)
					}
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
			handler.cfg = &cfg
			newDashCfg := newFullCfg.ToDashboardConfig()

			// Handle dashboard URL or TLS fingerprint changes.
			if newDashCfg.URL != dashCfg.URL || newDashCfg.TLSFingerprint != dashCfg.TLSFingerprint {
				if newDashCfg.URL != "" {
					dashboard.InitDashClient(newDashCfg.TLSFingerprint)
					pollsSinceConfigFetch = configFetchInterval // force re-fetch next poll
					dashConfigFailures = 0                      // reset backoff on URL change
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

			dashCfg = newDashCfg
			s.log(dc.LvlINF, "config=reloaded")
			_ = s.elog.Info(EvtConfigReloaded, "Configuration reloaded from config.json.")

		case <-flushTicker.C:
			_ = st.FlushIfDirty()
		}
	}
}

// RunService starts the Windows service. Called by the CLI's hidden
// "service run" subcommand.
func RunService() error {
	elog, err := eventlog.Open(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = elog.Close() }()

	log := EventLogLogger(elog)
	return svc.Run(dc.ServiceName, &drainService{log: log, elog: elog})
}
