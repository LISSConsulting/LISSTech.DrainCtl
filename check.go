//go:build windows

package drainctl

import (
	"fmt"
	"time"
)

// CheckOptions configures a drain mode check.
type CheckOptions struct {
	DBPath        string        // audit trail path (empty = skip audit)
	GracePeriod   time.Duration // how long drain mode must persist before alerting
	RetentionDays int           // days to retain audit records
	Log           LogFunc       // nil = discard
}

// CheckOutput holds the result of a check plus the recommended exit code.
type CheckOutput struct {
	Result   *CheckResult
	ExitCode int
}

// Check reads the registry, detects transitions, records audit, and evaluates
// the drain mode state against the grace period.
func Check(opts CheckOptions) (*CheckOutput, error) {
	log := opts.Log
	if log == nil {
		log = DiscardLogger()
	}

	res := &CheckResult{
		Version:            Version,
		Timestamp:          time.Now(),
		GracePeriodSeconds: int(opts.GracePeriod.Seconds()),
	}

	log(LvlINF, fmt.Sprintf("drainctl=%s", Version), "cmd=check")

	// ── Read registry ──────────────────────────────────────────────────
	state, err := ReadDrainMode()
	if err != nil {
		LogMsg(log, LvlERR, "registry read failed", fmt.Sprintf("error=%q", err))
		res.Status = "Error"
		res.Message = err.Error()
		res.ExitCode = 2
		return &CheckOutput{Result: res, ExitCode: 2}, nil
	}

	res.Host = state.Host
	res.DrainModeLabel = state.Mode.String()
	res.DrainModeValue = uint32(state.Mode)

	log(LvlINF, fmt.Sprintf("host=%s", state.Host))
	if state.Mode == AllowAll {
		log(LvlINF, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("value=%d", state.Mode))
	} else {
		log(LvlWRN, fmt.Sprintf("drain_mode=%s", state.Mode), fmt.Sprintf("value=%d", state.Mode))
	}

	log(LvlINF, fmt.Sprintf("grace_period=%s", opts.GracePeriod))

	// ── Open audit store ───────────────────────────────────────────────
	var store *AuditStore
	if opts.DBPath != "" {
		store, err = OpenAuditStore(opts.DBPath)
		if err != nil {
			LogMsg(log, LvlWRN, "audit store unavailable", fmt.Sprintf("error=%q", err))
			store = nil
		}
	}
	if store != nil {
		defer func() { _ = store.Close() }()
	}

	// ── Evaluate state ─────────────────────────────────────────────────
	drainActive := state.Mode != AllowAll
	graceExceeded := !state.KeyModified.IsZero() && time.Since(state.KeyModified) > opts.GracePeriod
	connAllowed := !drainActive
	res.ConnectionsAllowed = &connAllowed

	exitCode := 0
	if drainActive && graceExceeded {
		exitCode = 1
	}
	res.ExitCode = exitCode

	// Detect state transitions
	if store != nil {
		last, err := store.LastObservation()
		if err != nil {
			LogMsg(log, LvlWRN, "could not read last observation", fmt.Sprintf("error=%q", err))
		} else if last != nil && last.DrainMode != state.Mode {
			res.Transition = true
			res.TransitionFrom = last.DrainMode.String()

			log(LvlWRN,
				"transition=true",
				fmt.Sprintf("from=%s", last.DrainMode),
				fmt.Sprintf("to=%s", state.Mode),
			)

			lookback := time.Since(last.Timestamp)
			if lookback < 24*time.Hour {
				res.ChangedBy = QueryRegistryChangeUser(last.Timestamp)
			}
			if res.ChangedBy != "" {
				log(LvlINF, fmt.Sprintf("changed_by=%s", res.ChangedBy))
			} else {
				log(LvlINF, "changed_by=unknown (run: drainctl audit-setup)")
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
			log(LvlINF, "state_since=now", "state_duration=0s")
		} else if since, err := store.StateSince(state.Mode); err == nil && since != nil {
			s := since.Local()
			res.StateSince = &s
			dur := time.Since(*since).Seconds()
			res.StateDurationSeconds = &dur
			log(LvlINF,
				fmt.Sprintf("state_since=%s", s.Format(time.RFC3339)),
				fmt.Sprintf("state_duration=%s", (time.Duration(dur)*time.Second).Truncate(time.Second)),
			)
		}
	}

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
			ExitCode:    exitCode,
		}
		if err := store.Record(rec); err != nil {
			LogMsg(log, LvlWRN, "failed to record observation", fmt.Sprintf("error=%q", err))
		}

		retention := time.Duration(opts.RetentionDays) * 24 * time.Hour
		if pruned, err := store.Prune(retention); err != nil {
			LogMsg(log, LvlWRN, "prune failed", fmt.Sprintf("error=%q", err))
		} else if pruned > 0 {
			log(LvlINF, fmt.Sprintf("pruned=%d", pruned), fmt.Sprintf("retention=%dd", opts.RetentionDays))
		}
	}

	// ── Determine final status ─────────────────────────────────────────
	if drainActive && graceExceeded {
		age := time.Since(state.KeyModified).Truncate(time.Second)
		res.Status = "Alert"
		res.Message = fmt.Sprintf("Drain mode active for %s, exceeding grace period of %s. New connections are blocked.", age, opts.GracePeriod)
		log(LvlERR, "status=alert", "connections_allowed=false",
			fmt.Sprintf("drain_age=%s", age),
			fmt.Sprintf("threshold=%s", opts.GracePeriod),
			fmt.Sprintf("exit=%d", exitCode),
		)
		return &CheckOutput{Result: res, ExitCode: 1}, nil
	}

	if drainActive && !graceExceeded {
		remaining := opts.GracePeriod - time.Since(state.KeyModified)
		res.Status = "Grace"
		res.Message = fmt.Sprintf("Drain mode active, within grace period (%s remaining).", remaining.Truncate(time.Second))
		log(LvlWRN, "status=grace", "connections_allowed=false",
			fmt.Sprintf("grace_remaining=%s", remaining.Truncate(time.Second)),
			"exit=0",
		)
		return &CheckOutput{Result: res, ExitCode: 0}, nil
	}

	res.Status = "Healthy"
	res.Message = "All connections allowed."
	log(LvlOK, "status=healthy", "connections_allowed=true", "exit=0")
	return &CheckOutput{Result: res, ExitCode: 0}, nil
}
