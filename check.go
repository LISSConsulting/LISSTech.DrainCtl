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
	GracePeriod time.Duration
}

// CheckOutput holds the result of a check plus the recommended exit code.
type CheckOutput struct {
	Result   *CheckResult
	ExitCode int
}

// Check reads the registry and evaluates drain mode against the grace period.
//
// This is the CLI-direct fallback path used when the service pipe is
// unavailable (the primary path returns pipe.CheckViaPipe results that include
// full audit-backed transition data). The service owns the SQLite telemetry
// store exclusively, so this fallback derives state duration from the
// registry key's last-write timestamp rather than from an audit trail.
func Check(opts CheckOptions) (*CheckOutput, error) {
	log := slog.Default()

	res := &CheckResult{
		Version:            Version,
		Timestamp:          time.Now(),
		GracePeriodSeconds: int(opts.GracePeriod.Seconds()),
	}

	log.Info("", "drainctl", Version, "cmd", "check")

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

	drainActive := state.Mode != AllowAll
	connAllowed := !drainActive
	res.ConnectionsAllowed = &connAllowed

	stateDur := time.Duration(0)
	if !state.KeyModified.IsZero() {
		stateDur = time.Since(state.KeyModified)
		s := state.KeyModified.Local()
		res.StateSince = &s
		dur := stateDur.Seconds()
		res.StateDurationSeconds = &dur
		log.Info("",
			"state_since", s.Format(time.RFC3339),
			"state_duration", stateDur.Truncate(time.Second).String(),
		)
	}

	res.Status, res.Message, res.ExitCode = ClassifyState(drainActive, stateDur, opts.GracePeriod)

	switch res.Status {
	case "Alert":
		log.Error("",
			"status", "alert",
			"connections_allowed", false,
			"drain_age", stateDur.Truncate(time.Second).String(),
			"threshold", opts.GracePeriod.String(),
			"exit", res.ExitCode,
		)
	case "Grace":
		remaining := opts.GracePeriod - stateDur
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
