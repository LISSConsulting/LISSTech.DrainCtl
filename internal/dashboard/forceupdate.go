//go:build windows

package dashboard

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// forceUpdateOutcome is the synchronously observable status returned to the
// dashboard caller when it issues a force-update command. It answers the
// narrow question "did we accept the command right now?" — the asynchronous
// completion event ("did the agent actually run the updater and what was
// the decision?") is published via the SSE force_update event with its
// own outcome vocabulary.
type forceUpdateOutcome string

const (
	// forceUpdateOutcomeAccepted means the command is queued for delivery
	// on the agent's next /api/v1/report. The dashboard caller should
	// expect an SSE force_update event within the agent's poll_interval.
	forceUpdateOutcomeAccepted forceUpdateOutcome = "accepted"
	// forceUpdateOutcomeDuplicate means the same (host, command_id) pair
	// was already accepted within the idempotency window. No second
	// execution will occur. Returned with HTTP 200 — the caller asked for
	// an idempotent operation, and idempotency was preserved.
	forceUpdateOutcomeDuplicate forceUpdateOutcome = "duplicate"
	// forceUpdateOutcomeOffline means the host is registered but stale
	// (no report within the offline threshold). The command is rejected;
	// it is NOT queued. Caller should treat this as a hard error.
	forceUpdateOutcomeOffline forceUpdateOutcome = "offline"
	// forceUpdateOutcomeUnsupported means the agent's last reported
	// version is below the minimum version that implements force-update.
	// The command is rejected; the agent would not understand the
	// pending-command block in the report response. This is distinct from
	// "offline" because the host IS reachable — it just doesn't speak
	// the new protocol.
	forceUpdateOutcomeUnsupported forceUpdateOutcome = "unsupported"
	// forceUpdateOutcomeFailed means durable persistence failed. It is never
	// returned as accepted because an unpersisted command would disappear on a
	// dashboard restart.
	forceUpdateOutcomeFailed forceUpdateOutcome = "failed"
)

// forceUpdateCommand is the in-memory delivery record for a queued command.
// The agent persists its command IDs before execution, which is the durable
// replay guard across agent restarts. This registry retains a command for the
// idempotency window so repeated operator requests and repeated reports do
// not result in another delivery while this dashboard process is running.
type forceUpdateCommand struct {
	CommandID    string             `json:"command_id"`
	Host         string             `json:"host"`
	Reason       string             `json:"reason,omitempty"`
	AcceptedAt   time.Time          `json:"accepted_at"`
	AgentVersion string             `json:"agent_version"`
	Outcome      forceUpdateOutcome `json:"outcome"`
	Acknowledged bool               `json:"acknowledged"`
}

// forceUpdateState is the dashboard's delivery registry. When outbox is
// present, every accepted command is first committed to SQLite and restored on
// dashboard startup; the in-memory map only accelerates report delivery.
type forceUpdateState struct {
	mu       sync.Mutex
	commands map[string][]*forceUpdateCommand // keyed by lowercase hostname
	outbox   *telemetry.ForceUpdateOutboxStore
}

func newForceUpdateState() *forceUpdateState {
	return &forceUpdateState{commands: make(map[string][]*forceUpdateCommand)}
}

func newPersistentForceUpdateState(ctx context.Context, outbox *telemetry.ForceUpdateOutboxStore) (*forceUpdateState, error) {
	state := newForceUpdateState()
	state.outbox = outbox
	if outbox == nil {
		return state, nil
	}
	entries, err := outbox.Load(ctx, time.Now().Add(-forceUpdateIdempotencyWindow))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		state.commands[entry.Host] = append(state.commands[entry.Host], &forceUpdateCommand{
			CommandID: entry.CommandID, Host: entry.Host, Reason: entry.Reason,
			AcceptedAt: entry.AcceptedAt, AgentVersion: entry.AgentVersion,
			Outcome: forceUpdateOutcomeAccepted,
		})
	}
	return state, nil
}

// forceUpdateIdempotencyWindow is the rolling window during which a
// duplicate (host, command_id) is rejected. 24 hours matches the spec
// acceptance criterion ("repeated delivery does not run the same
// command twice"). Exposed as a var so tests can shrink it.
var forceUpdateIdempotencyWindow = 24 * time.Hour

// forceUpdateMaxReasonLen caps the operator-supplied reason field so a
// misbehaving dashboard UI cannot push megabytes into the per-host log
// stream or the SSE event payload. 256 chars is the documented cap; see
// the BatchOperations integration contract.
const forceUpdateMaxReasonLen = 256

// forceUpdateCommandIDRE matches the accepted command_id alphabet and
// length. Loosely UUID-shaped so UUIDv4 round-trips, but does not reject
// other deterministic operator keys (timestamp suffixes, build IDs, etc.)
// that a custom dashboard might use. Rejects whitespace, control chars,
// and overlong inputs that would explode log lines or SSE payloads.
var forceUpdateCommandIDRE = regexp.MustCompile(`^[A-Za-z0-9_\-]{8,128}$`)

// Enqueue accepts a new force-update command for the named host.
// Returns the synchronously-observable outcome and, when the outcome is
// Accepted, the command itself so the caller can serialise it into the
// response body. agentVersion is the version reported on the host's most
// recent /api/v1/report (empty string if the host has never reported).
//
// Idempotency: if a command with the same CommandID is already pending
// (and not yet expired) for this host, Enqueue returns Duplicate and
// does NOT enqueue a second entry. A separate, distinct CommandID is
// treated as a brand-new request.
//
// Concurrency: safe for concurrent callers. The lock is held only across
// the small map lookup + append; the lazy garbage collect is also
// performed under the lock so a parallel caller sees a consistent
// snapshot.
func (s *forceUpdateState) Enqueue(host, commandID, reason, agentVersion string) (forceUpdateOutcome, *forceUpdateCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	s.gcLocked(host, now)

	for _, existing := range s.commands[host] {
		if existing.CommandID == commandID {
			return forceUpdateOutcomeDuplicate, existing
		}
	}

	cmd := &forceUpdateCommand{
		CommandID: commandID, Host: host, Reason: reason, AcceptedAt: now,
		AgentVersion: agentVersion, Outcome: forceUpdateOutcomeAccepted,
	}
	if s.outbox != nil {
		if err := s.outbox.Enqueue(context.Background(), telemetry.ForceUpdateOutboxEntry{
			CommandID: commandID, Host: host, Reason: reason, AcceptedAt: now, AgentVersion: agentVersion,
		}, now.Add(-forceUpdateIdempotencyWindow)); err != nil {
			slog.Error("dashboard=force_update_persist_failed", "host", host, "command_id", commandID, "error", err)
			return forceUpdateOutcomeFailed, nil
		}
	}
	s.commands[host] = append(s.commands[host], cmd)
	return forceUpdateOutcomeAccepted, cmd
}

// Consume returns the oldest unacknowledged command. Delivery remains
// retryable until an authenticated completion acknowledges the command, so a
// lost report response cannot strand an accepted request.
func (s *forceUpdateState) Consume(host string) *forceUpdateCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(host, time.Now())
	for _, cmd := range s.commands[host] {
		if !cmd.Acknowledged {
			clone := *cmd
			return &clone
		}
	}
	return nil
}

// Pending returns a snapshot of unacknowledged commands for host.
func (s *forceUpdateState) Pending(host string) []*forceUpdateCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(host, time.Now())
	out := make([]*forceUpdateCommand, 0, len(s.commands[host]))
	for _, cmd := range s.commands[host] {
		if !cmd.Acknowledged {
			clone := *cmd
			out = append(out, &clone)
		}
	}
	return out
}

// Acknowledge removes a command only after the persistent delete succeeds.
// A transient SQLite error deliberately leaves it retryable.
func (s *forceUpdateState) Acknowledge(host, commandID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outbox != nil {
		if err := s.outbox.Acknowledge(context.Background(), host, commandID); err != nil {
			slog.Warn("dashboard=force_update_ack_persist_failed", "host", host, "command_id", commandID, "error", err)
			return
		}
	}
	for _, cmd := range s.commands[host] {
		if cmd.CommandID == commandID {
			cmd.Acknowledged = true
			return
		}
	}
}

// gcLocked evicts expired entries for host. Must be called with s.mu
// held. An entry is expired when its AcceptedAt is older than the
// idempotency window; the entry may be in delivered or undelivered
// state — eviction is the same either way.
func (s *forceUpdateState) gcLocked(host string, now time.Time) {
	cutoff := now.Add(-forceUpdateIdempotencyWindow)
	cmds := s.commands[host]
	kept := cmds[:0]
	for _, cmd := range cmds {
		if cmd.AcceptedAt.After(cutoff) {
			kept = append(kept, cmd)
		}
	}
	if len(kept) == 0 {
		delete(s.commands, host)
		return
	}
	// Zero out the tail we're about to drop so a long-lived reference
	// held by a concurrent goroutine (e.g. an HTTP response mid-write)
	// can't accidentally observe stale data.
	for i := len(kept); i < len(cmds); i++ {
		cmds[i] = nil
	}
	s.commands[host] = kept
}
