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

	if drainActive && stateDur > gp {
		res.Status = "Alert"
		res.Message = fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.",
			stateDur.Truncate(time.Second), gp)
		res.ExitCode = 1
	} else if drainActive {
		remaining := gp - stateDur
		res.Status = "Grace"
		res.Message = fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second))
		res.ExitCode = 0
	} else {
		res.Status = "Healthy"
		res.Message = "All connections allowed."
		res.ExitCode = 0
	}

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
			} else if !useRemoteConfig {
				notifyTargets = newFullCfg.Notifications
			}

			dashCfg = newDashCfg
			s.log(dc.LvlINF, "config=reloaded")
			_ = s.elog.Info(EvtConfigReloaded, "Configuration reloaded from config.json.")

		case <-flushTicker.C:
			_ = st.FlushIfDirty()
		}
	}
}

// svcRunCheck performs a single check cycle in service mode.
func svcRunCheck(st *store.MemAuditStore, cfg *dc.ServiceConfig, targets []dc.NotificationTarget, notifyState *dc.NotifyState, dashCfg *dc.DashboardConfig, evtSub *watcher.EventSubscriber, log dc.LogFunc, elog *eventlog.Log) {
	state, err := dc.ReadDrainMode()
	if err != nil {
		dc.LogMsg(log, dc.LvlERR, "registry read failed", fmt.Sprintf("error=%q", err))
		_ = elog.Error(EvtRegistryFailed, fmt.Sprintf("Failed to read drain mode registry value: %s", err))
		return
	}

	transition := false
	transitionFrom := ""
	changedBy := ""

	if last := st.LastObservation(); last != nil && last.DrainMode != state.Mode {
		transition = true
		transitionFrom = last.DrainMode.String()
		log(dc.LvlWRN,
			"transition=true",
			fmt.Sprintf("from=%s", last.DrainMode),
			fmt.Sprintf("to=%s", state.Mode),
		)

		beforeTransition := time.Now().Add(-5 * time.Second)

		if evtSub != nil {
			// Wait up to 3 seconds for the 4657 event to arrive via push.
			changedBy = evtSub.WaitAttribution(beforeTransition, 3*time.Second)
		}
		if changedBy == "" {
			// Fallback: query wevtutil (covers cases where EvtSubscribe
			// missed the event or wasn't available).
			if lookback := time.Since(last.Timestamp); lookback < 24*time.Hour {
				changedBy = dc.QueryRegistryChangeUser(last.Timestamp)
			}
		}
		if changedBy != "" {
			log(dc.LvlINF, fmt.Sprintf("changed_by=%s", changedBy))
		}
	}

	// Determine exit code and status.
	drainActive := state.Mode != dc.AllowAll
	exitCode := 0
	stateDur := time.Duration(0)
	if since := st.StateSince(state.Mode); since != nil {
		stateDur = time.Since(*since).Truncate(time.Second)
	}
	if drainActive {
		if stateDur > cfg.GracePeriod {
			exitCode = 1
		}
	}

	// Session tracking.
	sess := dc.GetSessionSummary()

	rec := &dc.AuditRecord{
		Timestamp:   time.Now(),
		Host:        state.Host,
		DrainMode:   state.Mode,
		DrainLabel:  state.Mode.String(),
		KeyModified: state.KeyModified,
		Changed:     transition,
		ChangedBy:   changedBy,
		ExitCode:    exitCode,
	}
	if sess != nil {
		rec.ActiveSessions = sess.ActiveSessions
		rec.TotalSessions = sess.TotalSessions
		rec.MaxSessions = sess.MaxSessions
	}
	st.Append(rec)

	if state.Mode == dc.AllowAll {
		log(dc.LvlINF, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
	} else {
		log(dc.LvlWRN, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("exit=%d", exitCode))
	}

	// Write state-specific event log entries.
	if transition {
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		_ = elog.Info(EvtTransition, fmt.Sprintf(
			"State transition detected on %s: %s -> %s. Changed by: %s.",
			state.Host, transitionFrom, state.Mode, cb))
	}

	if drainActive && exitCode == 1 {
		// Alert: drain mode exceeded grace period.
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		_ = elog.Error(EvtCheckAlert, fmt.Sprintf(
			"ALERT: Drain mode active on %s for %s, exceeding grace period of %s. Mode: %s. Changed by: %s.",
			state.Host, stateDur, cfg.GracePeriod, state.Mode, cb))
	} else if drainActive {
		// Grace: within grace period.
		remaining := cfg.GracePeriod - stateDur
		_ = elog.Warning(EvtCheckGrace, fmt.Sprintf(
			"Drain mode active on %s, within grace period (%s remaining). Mode: %s.",
			state.Host, remaining.Truncate(time.Second), state.Mode))
	} else {
		// Healthy.
		_ = elog.Info(EvtCheckHealthy, fmt.Sprintf(
			"Drain mode check: %s on %s. All connections allowed. State duration: %s.",
			state.Mode, state.Host, stateDur))
	}

	// Build CheckResult for notifications and dashboard reporting.
	status := "Healthy"
	message := "All connections allowed."
	if drainActive && exitCode == 1 {
		status = "Alert"
		message = fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.",
			stateDur, cfg.GracePeriod)
	} else if drainActive {
		remaining := cfg.GracePeriod - stateDur
		status = "Grace"
		message = fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second))
	}

	dur := stateDur.Seconds()
	connAllowed := !drainActive
	result := &dc.CheckResult{
		Version:              dc.Version,
		Timestamp:            time.Now(),
		Host:                 state.Host,
		DrainModeLabel:       state.Mode.String(),
		DrainModeValue:       uint32(state.Mode),
		GracePeriodSeconds:   int(cfg.GracePeriod.Seconds()),
		StateDurationSeconds: &dur,
		Status:               status,
		ConnectionsAllowed:   &connAllowed,
		Transition:           transition,
		TransitionFrom:       transitionFrom,
		ChangedBy:            changedBy,
		Sessions:             sess,
		Message:              message,
		ExitCode:             exitCode,
	}

	// Determine triggers and send notifications.
	if len(targets) > 0 {
		var triggers []dc.Trigger

		if transition {
			if state.Mode != dc.AllowAll {
				triggers = append(triggers, dc.TriggerDrainOn)
			} else {
				triggers = append(triggers, dc.TriggerDrainOff)
			}
		}
		if status == "Grace" && transition {
			triggers = append(triggers, dc.TriggerGraceEntered)
		}
		if status == "Alert" {
			triggers = append(triggers, dc.TriggerAlert)
		}
		if status == "Healthy" && transition {
			triggers = append(triggers, dc.TriggerHealthy)
		}

		// Session utilization warning.
		if sess != nil && cfg.SessionWarningThreshold > 0 && sess.MaxSessions > 0 {
			if sess.UtilizationPct >= cfg.SessionWarningThreshold {
				triggers = append(triggers, dc.TriggerSessionWarning)
			}
		}

		for _, trigger := range triggers {
			dc.SendNotification(targets, notifyState, result, trigger, changedBy, log)
		}
	}

	// Report to dashboard if configured.
	if dashCfg != nil && dashCfg.URL != "" {
		dashboard.ReportState(dashCfg.URL, result, log)
	}
}

// applyRemoteConfig updates service config from dashboard-sourced notification settings.
func applyRemoteConfig(remote *dashboard.RemoteNotifyConfig, cfg *dc.ServiceConfig, targets *[]dc.NotificationTarget) {
	*targets = remote.Notifications
	if remote.SessionWarningThreshold >= 0 {
		cfg.SessionWarningThreshold = remote.SessionWarningThreshold
	}
	if remote.GracePeriod > 0 {
		cfg.GracePeriod = time.Duration(remote.GracePeriod) * time.Minute
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
