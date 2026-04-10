//go:build windows

package drainctl

import (
	"fmt"
	"log/slog"
	"time"
)

// ClassifyState derives the drain status, human-readable message, and exit
// code from the three fundamental drain inputs.
//
//   - drainActive: drain mode is not AllowAll
//   - stateDur:    how long the current mode has been active (0 if unknown)
//   - gracePeriod: configured grace period
//
// The function is pure and deterministic — it contains no I/O or system calls.
func ClassifyState(drainActive bool, stateDur, gracePeriod time.Duration) (status, message string, exitCode int) {
	if drainActive && stateDur > gracePeriod {
		return "Alert",
			fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.",
				stateDur.Truncate(time.Second), gracePeriod),
			1
	}
	if drainActive {
		remaining := gracePeriod - stateDur
		return "Grace",
			fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second)),
			0
	}
	return "Healthy", "All connections allowed.", 0
}

// CheckOptions configures a drain mode check.
type CheckOptions struct {
	DBPath        string        // audit trail path (empty = skip audit)
	GracePeriod   time.Duration // how long drain mode must persist before alerting
	RetentionDays int           // days to retain audit records
}

// CheckOutput holds the result of a check plus the recommended exit code.
type CheckOutput struct {
	Result   *CheckResult
	ExitCode int
}

// Check reads the registry, detects transitions, records audit, and evaluates
// the drain mode state against the grace period.
func Check(opts CheckOptions) (*CheckOutput, error) {
	log := slog.Default()

	res := &CheckResult{
		Version:            Version,
		Timestamp:          time.Now(),
		GracePeriodSeconds: int(opts.GracePeriod.Seconds()),
	}

	log.Info("", "drainctl", Version, "cmd", "check")

	// ── Read registry ──────────────────────────────────────────────────
	state, err := ReadDrainMode()
	if err != nil {
		log.Error("registry read failed", "error", err)
		res.Status = "Error"
		res.Message = err.Error()
		res.ExitCode = 2
		return &CheckOutput{Result: res, ExitCode: 2}, nil
	}

	res.Host = state.Host
	res.DrainModeLabel = state.Mode.String()
	res.DrainModeValue = uint32(state.Mode)

	log.Info("", "host", state.Host)
	if state.Mode == AllowAll {
		log.Info("", "drain_mode", state.Mode.String(), "value", uint32(state.Mode))
	} else {
		log.Warn("", "drain_mode", state.Mode.String(), "value", uint32(state.Mode))
	}

	log.Info("", "grace_period", opts.GracePeriod.String())

	// ── Session tracking ───────────────────────────────────────────────
	if sess := GetSessionSummary(); sess != nil {
		res.Sessions = sess
		if sess.MaxSessions > 0 {
			log.Info("",
				"sessions", fmt.Sprintf("%d/%d", sess.TotalSessions, sess.MaxSessions),
				"utilization", fmt.Sprintf("%d%%", sess.UtilizationPct),
			)
		} else {
			sessStr := fmt.Sprintf("active=%d", sess.ActiveSessions)
			if sess.DisconnectedSessions > 0 {
				sessStr += fmt.Sprintf(" disconnected=%d", sess.DisconnectedSessions)
			}
			log.Info("sessions=" + sessStr)
		}
	}

	// ── Open audit store ───────────────────────────────────────────────
	var store *AuditStore
	if opts.DBPath != "" {
		store, err = OpenAuditStore(opts.DBPath)
		if err != nil {
			log.Warn("audit store unavailable", "error", err)
			store = nil
		}
	}
	if store != nil {
		defer func() { _ = store.Close() }()
	}

	// ── Evaluate state ─────────────────────────────────────────────────
	drainActive := state.Mode != AllowAll
	connAllowed := !drainActive
	res.ConnectionsAllowed = &connAllowed

	// Compute state duration from the registry key's last-write timestamp.
	// If KeyModified is zero (timestamp unavailable), treat stateDur as 0 so
	// ClassifyState conservatively returns Grace rather than Alert.
	stateDur := time.Duration(0)
	if !state.KeyModified.IsZero() {
		stateDur = time.Since(state.KeyModified)
	}

	// Detect state transitions
	if store != nil {
		last, err := store.LastObservation()
		if err != nil {
			log.Warn("could not read last observation", "error", err)
		} else if last != nil && last.DrainMode != state.Mode {
			res.Transition = true
			res.TransitionFrom = last.DrainMode.String()

			log.Warn("",
				"transition", "true",
				"from", last.DrainMode.String(),
				"to", state.Mode.String(),
			)

			lookback := time.Since(last.Timestamp)
			if lookback < 24*time.Hour {
				res.ChangedBy = QueryRegistryChangeUser(last.Timestamp)
			}
			if res.ChangedBy != "" {
				log.Info("", "changed_by", res.ChangedBy)
			} else {
				log.Info("changed_by=unknown (run: drainctl audit-setup)")
			}
		}
	}

	// Compute state duration from audit trail
	if store != nil {
		if res.Transition {
			// Just transitioned — state duration is zero (this is the first observation)
			zero := 0.0
			now := time.Now()
			res.StateSince = &now
			res.StateDurationSeconds = &zero
			log.Info("", "state_since", "now", "state_duration", "0s")
		} else if since, err := store.StateSince(state.Mode); err == nil && since != nil {
			s := since.Local()
			res.StateSince = &s
			dur := time.Since(*since).Seconds()
			res.StateDurationSeconds = &dur
			log.Info("",
				"state_since", s.Format(time.RFC3339),
				"state_duration", (time.Duration(dur) * time.Second).Truncate(time.Second).String(),
			)
		}
	}

	// Determine final status using the audit store's duration when available
	// (more accurate than the registry timestamp), falling back to stateDur.
	effectiveDur := stateDur
	if res.StateDurationSeconds != nil {
		effectiveDur = time.Duration(*res.StateDurationSeconds) * time.Second
	}
	res.Status, res.Message, res.ExitCode = ClassifyState(drainActive, effectiveDur, opts.GracePeriod)

	// Record this observation
	if store != nil {
		rec := &AuditRecord{
			Timestamp:   time.Now(),
			Host:        state.Host,
			DrainMode:   state.Mode,
			DrainLabel:  state.Mode.String(),
			KeyModified: state.KeyModified,
			Changed:     res.Transition,
			ChangedBy:   res.ChangedBy,
			ExitCode:    res.ExitCode,
		}
		if res.Sessions != nil {
			rec.ActiveSessions = res.Sessions.ActiveSessions
			rec.DisconnectedSessions = res.Sessions.DisconnectedSessions
			rec.TotalSessions = res.Sessions.TotalSessions
			rec.MaxSessions = res.Sessions.MaxSessions
		}
		if err := store.Record(rec); err != nil {
			log.Warn("failed to record observation", "error", err)
		}

		retention := time.Duration(opts.RetentionDays) * 24 * time.Hour
		if pruned, err := store.Prune(retention); err != nil {
			log.Warn("prune failed", "error", err)
		} else if pruned > 0 {
			log.Info("", "pruned", pruned, "retention_days", opts.RetentionDays)
		}
	}

	// ── Log final status ───────────────────────────────────────────────
	switch res.Status {
	case "Alert":
		log.Error("",
			"status", "alert",
			"connections_allowed", false,
			"drain_age", effectiveDur.Truncate(time.Second).String(),
			"threshold", opts.GracePeriod.String(),
			"exit", res.ExitCode,
		)
	case "Grace":
		remaining := opts.GracePeriod - effectiveDur
		log.Warn("",
			"status", "grace",
			"connections_allowed", false,
			"grace_remaining", remaining.Truncate(time.Second).String(),
			"exit", 0,
		)
	default:
		log.Info("status=healthy connections_allowed=true exit=0")
	}
	return &CheckOutput{Result: res, ExitCode: res.ExitCode}, nil
}
