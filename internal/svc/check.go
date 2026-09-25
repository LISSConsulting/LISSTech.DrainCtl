//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/perfmon"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/updater"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/watcher"
)

// svcRunCheck performs a single check cycle in service mode.
// dashState is non-nil when the dashboard runs in this process (local reporting).
// The handler owns the in-memory observation cache (transition detection,
// live state duration) and the persistent SQLite audit writer (T026/T059);
// drain-mode transitions are appended there, never to JSONL.
//
// updaterSub is non-nil when the auto-update subsystem is active. The svc
// loop passes it through so a force-update command can invoke the updater.
func svcRunCheck(
	ctx context.Context,
	h *serviceHandler,
	cfg *dc.ServiceConfig,
	targets []dc.NotificationTarget,
	exclusions []string,
	notifyState *dc.NotifyState,
	dashCfg *dc.DashboardConfig,
	dashState *dashboard.ServerState,
	evtSub *watcher.EventSubscriber,
	perfCollector *perfmon.Collector,
	perfTriggerState *perfmon.PerfTriggerState,
	evtSpikeSub *evtspike.Subsystem,
	updaterSub *updater.Subsystem,
) {
	checkStart := time.Now()
	slog.Debug("diag: check=read_drain_mode")
	state, err := dc.ReadDrainMode()
	if err != nil {
		slog.Error("registry read failed", "error", err, slog.Int("event_id", EvtRegistryFailed))
		return
	}

	transition := false
	transitionFrom := ""
	changedBy := ""
	prevState := dc.DrainMode(0)

	prev := h.observed.Load()
	if prev != nil && prev.DrainMode != state.Mode {
		transition = true
		transitionFrom = prev.DrainMode.String()
		prevState = prev.DrainMode
		slog.Warn("transition=true", "from", prev.DrainMode, "to", state.Mode)

		beforeTransition := time.Now().Add(-5 * time.Second)

		if evtSub != nil {
			// Wait up to 3 seconds for the 4657 event to arrive via push.
			changedBy = evtSub.WaitAttribution(beforeTransition, 3*time.Second)
		}
		if changedBy == "" {
			// Fallback: query wevtutil (covers cases where EvtSubscribe
			// missed the event or wasn't available).
			if lookback := time.Since(prev.Timestamp); lookback < 24*time.Hour {
				changedBy = dc.QueryRegistryChangeUser(prev.Timestamp)
			}
		}
		if changedBy != "" {
			slog.Info("changed_by", "user", changedBy)
		}
	}

	// On non-transition ticks, inherit the principal from the prior observation
	// so the dashboard `Changed By` and the CheckResult sent upstream reflect
	// the actor who set the current state. Without this, every post-transition
	// tick would clear ChangedBy to "" and the UI would flip to `—` on refresh.
	if !transition && prev != nil {
		changedBy = prev.ChangedBy
	}

	// Determine exit code and status.
	drainActive := state.Mode != dc.AllowAll
	tickTime := time.Now()
	stateBegan := tickTime
	if prev != nil && prev.DrainMode == state.Mode {
		stateBegan = prev.StateSince
	}
	stateDur := time.Since(stateBegan).Truncate(time.Second)
	s := stateBegan.Local()
	stateSince := &s

	var status, message string
	var exitCode int
	status, message, exitCode = dc.ClassifyState(drainActive, stateDur, cfg.GracePeriod)
	statusChanged := prev == nil || transition
	if prev != nil && !transition {
		previousDuration := prev.Timestamp.Sub(prev.StateSince)
		if previousDuration < 0 {
			previousDuration = 0
		}
		previousStatus, _, _ := dc.ClassifyState(
			prev.DrainMode != dc.AllowAll,
			previousDuration.Truncate(time.Second),
			cfg.GracePeriod,
		)
		statusChanged = previousStatus != status
	}

	// Session tracking.
	slog.Debug("diag: check=get_sessions")
	sess := dc.GetSessionSummary()
	if sess == nil {
		slog.Warn("session enumeration failed", "hint", "verify service runs as LocalSystem")
	}
	h.lastSessions.Store(sess)

	// Performance counters (service-mode only).
	slog.Debug("diag: check=perfmon", "collector_nil", perfCollector == nil)
	var perfSnap *dc.PerfSnapshot
	if perfCollector != nil {
		snap, err := perfCollector.Collect()
		if err != nil {
			slog.Warn("perfmon collect failed", "error", err)
		} else {
			perfSnap = snap
			h.lastPerf.Store(snap)
		}
	}

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
		rec.DisconnectedSessions = sess.DisconnectedSessions
		rec.TotalSessions = sess.TotalSessions
		rec.MaxSessions = sess.MaxSessions
	}
	if perfSnap != nil {
		rec.CPUPct = perfSnap.CPUPct
		rec.InputDelayMax = perfSnap.InputDelayMax
		rec.MemAvailMB = perfSnap.MemAvailMB
		rec.MemTotalMB = perfSnap.MemTotalMB
		rec.DiskQueue = perfSnap.DiskQueue
		rec.TCPRetransSec = perfSnap.TCPRetrans
	}
	// Update the in-memory observation cache so the next tick (and concurrent
	// HandleStatus calls) see this sample. StateSince stays pinned to when the
	// current state began: preserved from prev on no transition, reset to now
	// when the mode flipped.
	h.observed.Store(&observation{
		Timestamp:  tickTime,
		DrainMode:  state.Mode,
		StateSince: stateBegan,
		ChangedBy:  changedBy,
	})
	slog.Debug("diag: check=observed_updated")

	// Persist drain-mode transitions to the SQLite audit table (T026, FR-001).
	// Only transitions are written — audit is "one row per drain-mode transition"
	// (data-model.md).
	if transition && h.audit != nil {
		trec := telemetry.AuditRecord{
			Ts:        rec.Timestamp,
			Host:      rec.Host,
			PrevState: int(prevState),
			NewState:  int(rec.DrainMode),
			Principal: changedBy,
			ChangedBy: changedBy,
		}
		if !rec.KeyModified.IsZero() {
			km := rec.KeyModified
			trec.KeyModifiedTs = &km
		}
		if err := h.audit.Append(ctx, trec); err != nil {
			slog.Warn("telemetry audit append failed", "error", err, "host", rec.Host)
		}
	}

	slog.Debug("drain mode sampled", "mode", state.Mode, "exit", exitCode)

	// Emit state-specific ETW events via slog with event_id attribute.
	if transition {
		cb := changedBy
		if cb == "" {
			cb = "unknown"
		}
		slog.Info(fmt.Sprintf("State transition detected on %s: %s -> %s. Changed by: %s.",
			state.Host, transitionFrom, state.Mode, cb),
			slog.Int("event_id", EvtTransition))
	}

	if !statusChanged {
		slog.Debug("drain state unchanged",
			"mode", state.Mode,
			"status", status,
			"duration", stateDur)
	} else {
		switch status {
		case "Alert":
			cb := changedBy
			if cb == "" {
				cb = "unknown"
			}
			slog.Error(fmt.Sprintf("ALERT: Drain mode active on %s for %s, exceeding grace period of %s. Mode: %s. Changed by: %s.",
				state.Host, stateDur, cfg.GracePeriod, state.Mode, cb),
				slog.Int("event_id", EvtCheckAlert))
		case "Grace":
			remaining := cfg.GracePeriod - stateDur
			slog.Warn(fmt.Sprintf("Drain mode active on %s, within grace period (%s remaining). Mode: %s.",
				state.Host, remaining.Truncate(time.Second), state.Mode),
				slog.Int("event_id", EvtCheckGrace))
		default:
			slog.Info(fmt.Sprintf("Drain mode check: %s on %s. All connections allowed. State duration: %s.",
				state.Mode, state.Host, stateDur),
				slog.Int("event_id", EvtCheckHealthy))
		}
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
		StateSince:           stateSince,
		StateDurationSeconds: &dur,
		Status:               status,
		ConnectionsAllowed:   &connAllowed,
		Transition:           transition,
		TransitionFrom:       transitionFrom,
		ChangedBy:            changedBy,
		Sessions:             sess,
		Performance:          perfSnap,
		Message:              message,
		ExitCode:             exitCode,
	}

	// Attach the evtspike detector status so the central dashboard renders the
	// pill for remote hosts. Subsystem.Status() returns State=disabled when
	// off, so forwarding unconditionally is safe and keeps the central
	// dashboard's pill accurate when an operator flips enabled off.
	if evtSpikeSub != nil {
		status := evtSpikeSub.Status()
		result.EvtSpikeStatus = &status
	}

	// Build the trigger set from the current sample. Triggers drive two
	// independent outcomes: (a) outbound notifications to configured targets
	// and (b) the Status field the dashboard displays. Previously this whole
	// block was gated on `len(targets) > 0` — which meant a dashboard with
	// zero notification targets also disabled perf-threshold evaluation and
	// the Status promotion, so a server actively over its CPU/memory/input
	// limits kept reporting "Healthy". Evaluate unconditionally; only gate
	// the dispatch loop on target presence.
	slog.Debug("diag: check=notifications", "targets", len(targets))
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
	}

	// Performance threshold evaluation. Also updates perfTriggerState's
	// edge-detection bookkeeping — must run every tick or the next "enter"
	// edge is lost after a targetless period.
	if perfSnap != nil && perfTriggerState != nil {
		perfTriggers := perfmon.EvaluateThresholds(perfSnap, cfg.Performance, perfTriggerState, cfg.PollInterval)
		triggers = append(triggers, perfTriggers...)
	}

	// Dispatch notifications. For perf triggers, set the message to the
	// trigger-specific detail so each notification carries its own context
	// (not the last trigger's message).
	if len(targets) > 0 {
		savedMessage := result.Message
		for _, trigger := range triggers {
			if perfmon.IsPerfTrigger(trigger) {
				result.Message = perfmon.TriggerMessage(trigger, perfSnap, cfg.Performance)
			} else {
				result.Message = savedMessage
			}
			dc.SendNotificationWithExclusions(targets, exclusions, notifyState, result, trigger, changedBy)
		}
		result.Message = savedMessage
	}

	// Promote status based on active perf triggers so the dashboard reflects
	// overall health, not just drain mode. Runs regardless of notification
	// configuration — Status is authoritative output, notifications are the
	// optional side channel.
	for _, trigger := range triggers {
		switch trigger {
		case dc.TriggerCPUCritical, dc.TriggerMemoryCritical, dc.TriggerInputDelayCritical:
			result.Status = "Alert"
		case dc.TriggerCPUWarning, dc.TriggerMemoryWarning, dc.TriggerInputDelayWarning:
			if result.Status == "Healthy" {
				result.Status = "Warning"
			}
		}
	}

	// Report to dashboard if configured.
	if dashCfg != nil && dashCfg.URL != "" {
		slog.Debug("diag: check=dashboard_report", "url", dashCfg.URL)
		if dashState != nil {
			// Local path: dashboard runs in the same process. We still
			// need to surface any pending force-update command to the
			// agent, but we don't have an HTTP response to read it from
			// — instead we ask the dashboard's ServerState directly via
			// the consumePendingCommand helper exposed through the
			// dashboard package. The helper is exported for this exact
			// use case (the integration test for the force-update
			// registry exercises the same call from a remote path so
			// both stay in lock-step).
			pending := consumeForceUpdatePending(dashState, result.Host)
			var localCompletion *dashboard.ForceUpdateCompletion
			if pending != nil && updaterSub != nil {
				slog.Info("force_update=received",
					"host", pending.Host,
					"command_id", pending.CommandID,
					"reason", pending.Reason)
				outcome, decision, oldVer, newVer := triggerUpdateAndWait(ctx, updaterSub, pending)
				localCompletion = &dashboard.ForceUpdateCompletion{
					CommandID:  pending.CommandID,
					Outcome:    outcome,
					Reason:     decision,
					OldVersion: oldVer,
					NewVersion: newVer,
				}
			}
			if dashState.ReportLocal(result.Host, result, toDashboardCompletion(localCompletion, result.Host)) {
				slog.Debug("dashboard heartbeat (local)", "host", result.Host)
			} else {
				slog.Warn("dashboard heartbeat (local): host not registered", "host", result.Host)
			}
		} else {
			slog.Debug("dashboard heartbeat sending", "url", dashCfg.URL)
			repResult := dashboard.ReportState(ctx, dashCfg.URL, result)
			// Process any pending force-update command the dashboard
			// attached to the report response. The agent runs the
			// updater immediately; the completion is posted back on the
			// NEXT heartbeat (or this same round if the updater
			// completes synchronously below the deadline) via
			// ReportForceUpdateCompletion. The completion path is gated
			// on updaterSub != nil — pre-26.9.24 builds drop the
			// pending command silently because they can't act on it.
			if repResult != nil && repResult.PendingCommand != nil && updaterSub != nil {
				pending := repResult.PendingCommand
				slog.Info("force_update=received",
					"host", pending.Host,
					"command_id", pending.CommandID,
					"reason", pending.Reason)
				outcome, decision, oldVer, newVer := triggerUpdateAndWait(ctx, updaterSub, pending)
				dashboard.ReportForceUpdateCompletion(ctx, dashCfg.URL, pending.Host, dashboard.ForceUpdateCompletion{
					CommandID:  pending.CommandID,
					Outcome:    outcome,
					Reason:     decision,
					OldVersion: oldVer,
					NewVersion: newVer,
				})
			}
		}
	}

	slog.Debug("poll tick", "mode", state.Mode, "status", status, "elapsed", time.Since(checkStart).Truncate(time.Microsecond))
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
	for k := range state.LastPerfNotify {
		if !active[k] {
			delete(state.LastPerfNotify, k)
		}
	}
}

// applyRemoteConfig updates service config from dashboard-sourced settings.
// Values are clamped to their valid ranges so a misconfigured or compromised dashboard
// cannot inject out-of-range values into the service.
//
// evtSpike, when non-nil, receives the remote evtspike overlay: every
// operator-safe field from the dashboard is layered on top of the
// locally-loaded EvtSpikeConfig so the dashboard's ConfigModal controls
// (enabled toggle, sensitivity knobs, channel lists, security-channel gate)
// propagate to every connected agent. baseline_path stays admin-only on the
// agent side so it is never overwritten by the dashboard.
func applyRemoteConfig(
	remote *dashboard.RemoteSettings,
	cfg *dc.ServiceConfig,
	targets *[]dc.NotificationTarget,
	evtSpike *dc.EvtSpikeConfig,
	exclusions ...*[]string,
) {
	*targets = remote.Notifications
	if len(exclusions) > 0 && exclusions[0] != nil {
		*exclusions[0] = slices.Clone(remote.NotificationExclusions)
	}
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
	if remote.PollInterval > 0 {
		pi := remote.PollInterval
		if pi < 10 {
			pi = 10
		}
		if pi > dc.MaxPollInterval {
			pi = dc.MaxPollInterval
		}
		cfg.PollInterval = time.Duration(pi) * time.Second
	}
	// Apply remote performance config unless locally force-disabled.
	if remote.Performance != nil && !cfg.Performance.ForceDisabled {
		remote.Performance.ForceDisabled = cfg.Performance.ForceDisabled // preserve local flag
		cfg.Performance = *remote.Performance
	}
	if evtSpike != nil && remote.EvtSpike != nil {
		overlayEvtSpikeFromRemote(evtSpike, remote.EvtSpike)
	}
}

// overlayEvtSpikeFromRemote layers remote onto dst, clamping every field to
// its documented bounds. baseline_path is intentionally left untouched so
// the agent keeps its locally-configured baseline file location; the
// dashboard never sends baseline_path over the wire.
//
// The runtime live-reload path in service_loop.go calls Subsystem.Reload
// after this returns, which propagates the new scalars (MinCount, Threshold,
// Cooldown, SlotMaturityObservations, HalfLifeBuckets → Rho) to every
// running detector. Channel-set changes (DisabledChannels, AddedChannels,
// SecurityChannelEnabled) trigger a stop+start cycle via Reload's
// channelSetChanged branch — mature channels keep their learned state
// because the baseline is flushed to disk before the restart and hydrated
// back from the same file when subscriptions come back up.
//
// PersistIntervalSeconds is the one knob that is read on Reload but NOT
// applied to the running persistence loop — see internal/evtspike
// Subsystem.Reload's gamma update; the loop's tick source is captured at
// Start. The agent picks the new cadence up at the next start, which is
// the documented behaviour and matches the "hot scalars + restart for the
// loop cadence" contract.
func overlayEvtSpikeFromRemote(dst *dc.EvtSpikeConfig, remote *dashboard.RemoteEvtSpike) {
	if dst == nil || remote == nil {
		return
	}
	dst.Enabled = remote.Enabled

	dst.MinCount = clampRemoteInt(remote.MinCount, dc.MinEvtSpikeMinCount, dc.MaxEvtSpikeMinCount, dst.MinCount)
	dst.Threshold = clampRemoteFloat(remote.Threshold, dc.MinEvtSpikeThreshold, dc.MaxEvtSpikeThreshold, dst.Threshold)
	dst.CooldownMinutes = clampRemoteInt(remote.CooldownMinutes, dc.MinEvtSpikeCooldownMinutes, dc.MaxEvtSpikeCooldownMinutes, dst.CooldownMinutes)
	dst.SlotMaturityObservations = clampRemoteInt(remote.SlotMaturityObservations, dc.MinEvtSpikeSlotMaturityObservations, dc.MaxEvtSpikeSlotMaturityObservations, dst.SlotMaturityObservations)
	dst.PersistIntervalSeconds = clampRemoteInt(remote.PersistIntervalSeconds, dc.MinEvtSpikePersistIntervalSeconds, dc.MaxEvtSpikePersistIntervalSeconds, dst.PersistIntervalSeconds)
	dst.HalfLifeBuckets = clampRemoteInt(remote.HalfLifeBuckets, dc.MinEvtSpikeHalfLifeBuckets, dc.MaxEvtSpikeHalfLifeBuckets, dst.HalfLifeBuckets)
	dst.PriorStrength = clampRemoteFloat(remote.PriorStrength, dc.MinEvtSpikePriorStrength, dc.MaxEvtSpikePriorStrength, dst.PriorStrength)
	dst.MeanPerBucketPrior = clampRemoteFloat(remote.MeanPerBucketPrior, dc.MinEvtSpikeMeanPerBucketPrior, dc.MaxEvtSpikeMeanPerBucketPrior, dst.MeanPerBucketPrior)
	dst.SecurityChannelEnabled = remote.SecurityChannelEnabled

	// Slice fields: always replace (an empty slice on the wire means
	// "drop everything") so the dashboard's "Clear" affordance works.
	if remote.DisabledChannels != nil {
		cp := append([]string(nil), remote.DisabledChannels...)
		dst.DisabledChannels = cp
	}
	if remote.AddedChannels != nil {
		cp := append([]string(nil), remote.AddedChannels...)
		dst.AddedChannels = cp
	}
}

func clampRemoteInt(v, min, max, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func clampRemoteFloat(v, min, max, fallback float64) float64 {
	if v == 0 {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// effectiveUpdateConfig overlays the dashboard-managed self-update block on
// the locally configured fallback. Older dashboards omit Update, in which
// case the agent must preserve its local policy.
func effectiveUpdateConfig(local dc.UpdateConfig, remote *dashboard.RemoteSettings) dc.UpdateConfig {
	if remote != nil && remote.Update != nil {
		return *remote.Update
	}
	return local
}

// triggerUpdateAndWait runs the updater synchronously against the
// dashboard's pending force-update command and translates the result
// into the dashboard's ForceUpdateCompletion outcome vocabulary. The
// returned strings map 1:1 to the documented outcomes:
//   - completed: updater ran; decision is up_to_date | not_modified |
//     no_stable_release | installer_spawned.
//   - failed: updater errored (network, spawn, etc.). decision carries
//     the error context.
//   - refused: updater refused the remote release (signature,
//     replay-fence, manifest, etc.). decision carries the stage.
//   - duplicate: commandID was a replay within the idempotency
//     window; no second execution.
//
// A bounded ctx prevents a network-bound updater from pinning the
// service loop indefinitely; 60s is generous (typical tick finishes in
// <2s) and aligns with the periodic poll's own client timeout of 30s
// plus margin for verify + spawn.
func triggerUpdateAndWait(ctx context.Context, updaterSub *updater.Subsystem, pending *dashboard.ForceUpdatePendingCommand) (outcome, decision, oldVer, newVer string) {
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	tickOutcome := updaterSub.TriggerCheckNow(runCtx, pending.CommandID)
	switch tickOutcome.Decision {
	case "duplicate":
		return dashboard.ForceUpdateOutcomeDuplicate, tickOutcome.Reason, tickOutcome.OldVer, tickOutcome.NewVer
	case "disabled":
		// Operator disabled updater between dashboard's decision and our
		// run. The dashboard said accepted because at enqueue time the
		// host was up-to-date on the enabled flag; surface as
		// "completed:disabled" so the UI can show "updater disabled".
		return dashboard.ForceUpdateOutcomeFailed, "updater_disabled", tickOutcome.OldVer, tickOutcome.NewVer
	case "up_to_date", "not_modified", "no_stable_release", "installer_spawned":
		return dashboard.ForceUpdateOutcomeCompleted, tickOutcome.Decision, tickOutcome.OldVer, tickOutcome.NewVer
	case "refused":
		return dashboard.ForceUpdateOutcomeRefused, tickOutcome.Reason, tickOutcome.OldVer, tickOutcome.NewVer
	case "error":
		return dashboard.ForceUpdateOutcomeFailed, tickOutcome.Reason, tickOutcome.OldVer, tickOutcome.NewVer
	case "cancelled":
		return dashboard.ForceUpdateOutcomeFailed, "cancelled", tickOutcome.OldVer, tickOutcome.NewVer
	default:
		// Unknown decision: surface as failed with the raw value so an
		// operator can correlate against the file log.
		return dashboard.ForceUpdateOutcomeFailed, "unknown:" + tickOutcome.Decision, tickOutcome.OldVer, tickOutcome.NewVer
	}
}

// consumeForceUpdatePending pulls the next queued force-update command
// for host from the dashboard's in-process registry. Wraps the
// ServerState.GetConsumeForceUpdate closure so the svc caller doesn't
// have to nil-check the closure itself. Returns nil when no callback
// is wired (which is the pre-subsystem-Start case in tests, plus any
// code path that uses the dashboard package without going through
// Subsystem.Start).
func consumeForceUpdatePending(dashState *dashboard.ServerState, host string) *dashboard.ForceUpdatePendingCommand {
	if dashState == nil {
		return nil
	}
	f := dashState.GetConsumeForceUpdate()
	if f == nil {
		return nil
	}
	return f(host)
}

// toDashboardCompletion bridges the wire-side ForceUpdateCompletion
// (used by ReportForceUpdateCompletion over HTTP) to the dashboard's
// in-process completion payload (used by ReportLocal's SSE broadcast).
// host is the canonical checked host; local updater completions have no host
// field of their own, but the dashboard needs it to acknowledge the matching
// durable outbox row before broadcasting the terminal event.
// Returns nil for a nil input so callers can pass-through "no completion this
// round" without an extra nil check at the call site.
func toDashboardCompletion(c *dashboard.ForceUpdateCompletion, host string) *dashboard.ForceUpdateCompletionPayload {
	if c == nil {
		return nil
	}
	return &dashboard.ForceUpdateCompletionPayload{
		Host:       host,
		CommandID:  c.CommandID,
		Outcome:    c.Outcome,
		Reason:     c.Reason,
		OldVersion: c.OldVersion,
		NewVersion: c.NewVersion,
	}
}
