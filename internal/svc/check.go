//go:build windows

package svc

import (
	"fmt"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/store"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"

	"golang.org/x/sys/windows/svc/eventlog"
)

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
	stateDur := time.Duration(0)
	if since := st.StateSince(state.Mode); since != nil {
		stateDur = time.Since(*since).Truncate(time.Second)
	}

	var status, message string
	var exitCode int
	status, message, exitCode = dc.ClassifyState(drainActive, stateDur, cfg.GracePeriod)

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

	switch status {
	case "Alert":
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		_ = elog.Error(EvtCheckAlert, fmt.Sprintf(
			"ALERT: Drain mode active on %s for %s, exceeding grace period of %s. Mode: %s. Changed by: %s.",
			state.Host, stateDur, cfg.GracePeriod, state.Mode, cb))
	case "Grace":
		remaining := cfg.GracePeriod - stateDur
		_ = elog.Warning(EvtCheckGrace, fmt.Sprintf(
			"Drain mode active on %s, within grace period (%s remaining). Mode: %s.",
			state.Host, remaining.Truncate(time.Second), state.Mode))
	default:
		_ = elog.Info(EvtCheckHealthy, fmt.Sprintf(
			"Drain mode check: %s on %s. All connections allowed. State duration: %s.",
			state.Mode, state.Host, stateDur))
	}

	// Build CheckResult for notifications and dashboard reporting.

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
		if sess != nil && cfg.SessionWarningThreshold > 0 && sess.MaxSessions > 0 && sess.UtilizationPct >= cfg.SessionWarningThreshold {
			triggers = append(triggers, dc.TriggerSessionWarning)
		} else {
			resetSessionWarnCooldown(notifyState, cfg)
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

// pruneNotifyState removes per-URL entries from state that no longer correspond
// to any active notification target, preventing stale rate-limit state from
// affecting new or renamed targets.
func pruneNotifyState(state *dc.NotifyState, targets []dc.NotificationTarget) {
	active := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.URL != "" {
			active[t.URL] = true
		}
	}
	for k := range state.LastAlertNotify {
		if !active[k] {
			delete(state.LastAlertNotify, k)
		}
	}
	for k := range state.LastSessionWarnNotify {
		if !active[k] {
			delete(state.LastSessionWarnNotify, k)
		}
	}
}

// applyRemoteConfig updates service config from dashboard-sourced notification settings.
// Values are clamped to their valid ranges so a misconfigured or compromised dashboard
// cannot inject out-of-range values into the service.
func applyRemoteConfig(remote *dashboard.RemoteNotifyConfig, cfg *dc.ServiceConfig, targets *[]dc.NotificationTarget) {
	*targets = remote.Notifications
	if remote.SessionWarningThreshold >= 0 {
		t := remote.SessionWarningThreshold
		if t > 100 {
			t = 100
		}
		cfg.SessionWarningThreshold = t
	}
	if remote.GracePeriod > 0 {
		gp := remote.GracePeriod
		if gp > 1440 {
			gp = 1440
		}
		cfg.GracePeriod = time.Duration(gp) * time.Minute
	}
}

// resetSessionWarnCooldown clears per-target session-warning cooldown entries
// when session monitoring is enabled but utilization is currently at or below
// the threshold. This ensures the warning fires again the next time utilization
// rises above the threshold, rather than remaining suppressed indefinitely.
func resetSessionWarnCooldown(state *dc.NotifyState, cfg *dc.ServiceConfig) {
	if cfg.SessionWarningThreshold <= 0 {
		return
	}
	clear(state.LastSessionWarnNotify)
}
