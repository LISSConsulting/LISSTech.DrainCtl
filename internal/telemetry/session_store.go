//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

var errSessionStoreReadOnly = errors.New("telemetry: session store requires a writable database")

// SessionSnapshotStore keeps only the newest complete successful snapshot for a host.
type SessionSnapshotStore struct {
	db  *DB
	now func() time.Time
}

type SnapshotApplyResult struct {
	Accepted     bool
	ReceivedAtMS int64
	Fatal        bool
	Aggregates   *sessiondata.SessionAggregates
}

func NewSessionSnapshotStore(db *DB) (*SessionSnapshotStore, error) {
	if db == nil || db.writer == nil {
		return nil, errSessionStoreReadOnly
	}
	return &SessionSnapshotStore{db: db, now: time.Now}, nil
}

// Apply validates the already decoded snapshot, calculates privacy-safe
// aggregates from its original rows, projects it before persistence, and
// atomically records either a new successful current view or a fatal attempt.
func (s *SessionSnapshotStore) Apply(ctx context.Context, snapshot sessiondata.SessionSnapshot, privacy sessiondata.PrivacyPolicy) (SnapshotApplyResult, error) {
	if err := validateSnapshotForStore(snapshot); err != nil {
		return SnapshotApplyResult{}, err
	}
	instanceID, err := sessiondata.ParseUUIDv7(snapshot.AgentInstanceID)
	if err != nil {
		return SnapshotApplyResult{}, fmt.Errorf("telemetry: invalid session generation: %w", err)
	}
	snapshot.AgentInstanceID = instanceID.String()
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return SnapshotApplyResult{}, fmt.Errorf("telemetry: session snapshot begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var fence []byte
	err = tx.QueryRowContext(ctx, `SELECT max_instance_id FROM session_generation_fence WHERE canonical_host=?`, snapshot.Host).Scan(&fence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SnapshotApplyResult{}, fmt.Errorf("telemetry: session snapshot read generation fence: %w", err)
	}
	advanceFence := errors.Is(err, sql.ErrNoRows)
	if err == nil {
		if len(fence) != len(instanceID) {
			return SnapshotApplyResult{}, fmt.Errorf("telemetry: invalid stored session generation fence")
		}
		var maximum sessiondata.UUIDv7
		copy(maximum[:], fence)
		if _, err := sessiondata.ParseUUIDv7(maximum.String()); err != nil {
			return SnapshotApplyResult{}, fmt.Errorf("telemetry: invalid stored session generation fence")
		}
		switch sessiondata.CompareUUIDv7(instanceID, maximum) {
		case -1:
			if err := tx.Commit(); err != nil {
				return SnapshotApplyResult{}, fmt.Errorf("telemetry: session snapshot stale commit: %w", err)
			}
			return SnapshotApplyResult{Accepted: false, ReceivedAtMS: s.now().UTC().UnixMilli()}, nil
		case 1:
			advanceFence = true
		}
	}

	var instance string
	var sequence []byte
	err = tx.QueryRowContext(ctx, `SELECT latest_attempt_instance_id, latest_attempt_sequence FROM session_snapshots WHERE canonical_host=?`, snapshot.Host).Scan(&instance, &sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SnapshotApplyResult{}, fmt.Errorf("telemetry: session snapshot read generation: %w", err)
	}
	if !advanceFence && err == nil && instance == snapshot.AgentInstanceID && binary.BigEndian.Uint64(sequence) >= snapshot.Sequence.Uint64() {
		if err := tx.Commit(); err != nil {
			return SnapshotApplyResult{}, fmt.Errorf("telemetry: session snapshot stale commit: %w", err)
		}
		return SnapshotApplyResult{Accepted: false, ReceivedAtMS: s.now().UTC().UnixMilli()}, nil
	}
	if advanceFence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_generation_fence(canonical_host,max_instance_id) VALUES(?,?) ON CONFLICT(canonical_host) DO UPDATE SET max_instance_id=excluded.max_instance_id`, snapshot.Host, instanceID[:]); err != nil {
			return SnapshotApplyResult{}, fmt.Errorf("telemetry: advance session generation fence: %w", err)
		}
	}

	received := s.now().UTC().UnixMilli()
	sequence = sequenceBytes(snapshot.Sequence.Uint64())
	if snapshot.CollectionError != nil {
		if err := s.applyFatal(ctx, tx, snapshot, sequence, received); err != nil {
			return SnapshotApplyResult{}, err
		}
		if err := upsertSessionWorkload(ctx, tx, snapshot, received); err != nil {
			return SnapshotApplyResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return SnapshotApplyResult{}, fmt.Errorf("telemetry: session fatal commit: %w", err)
		}
		return SnapshotApplyResult{Accepted: true, ReceivedAtMS: received, Fatal: true}, nil
	}
	aggregates := sessiondata.CalculateAggregatesForIdentity(snapshot.Sessions, privacy.Identity)
	projected := sessiondata.ProjectSnapshot(snapshot, privacy)

	if err := s.applySuccess(ctx, tx, projected, sequence, received, aggregates); err != nil {
		return SnapshotApplyResult{}, err
	}
	if err := upsertSessionWorkload(ctx, tx, snapshot, received); err != nil {
		return SnapshotApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SnapshotApplyResult{}, fmt.Errorf("telemetry: session success commit: %w", err)
	}
	return SnapshotApplyResult{Accepted: true, ReceivedAtMS: received, Aggregates: &aggregates}, nil
}

func (s *SessionSnapshotStore) applyFatal(ctx context.Context, tx *sql.Tx, snapshot sessiondata.SessionSnapshot, sequence []byte, received int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_snapshots (
		canonical_host, latest_attempt_instance_id, latest_attempt_sequence, latest_attempt_observed_at_ms, latest_attempt_received_at_ms,
		last_success_instance_id, last_success_sequence, last_success_observed_at_ms, last_success_received_at_ms,
		session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms,
		collector_version, logical_cpu_count, capability_actions, capability_processes, capability_input_delay, capability_remotefx, latest_attempt_error_code
	) VALUES (?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?)
	ON CONFLICT(canonical_host) DO UPDATE SET
		latest_attempt_instance_id=excluded.latest_attempt_instance_id, latest_attempt_sequence=excluded.latest_attempt_sequence,
		latest_attempt_observed_at_ms=excluded.latest_attempt_observed_at_ms, latest_attempt_received_at_ms=excluded.latest_attempt_received_at_ms,
		latest_attempt_error_code=excluded.latest_attempt_error_code`,
		snapshot.Host, snapshot.AgentInstanceID, sequence, snapshot.ObservedAtMS, received, snapshot.CollectionError.Code)
	if err != nil {
		return fmt.Errorf("telemetry: update fatal session attempt: %w", err)
	}
	return nil
}

func (s *SessionSnapshotStore) applySuccess(ctx context.Context, tx *sql.Tx, snapshot sessiondata.SessionSnapshot, sequence []byte, received int64, aggregates sessiondata.SessionAggregates) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_snapshots (
		canonical_host, latest_attempt_instance_id, latest_attempt_sequence, latest_attempt_observed_at_ms, latest_attempt_received_at_ms,
		last_success_instance_id, last_success_sequence, last_success_observed_at_ms, last_success_received_at_ms,
		session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms,
		collector_version, logical_cpu_count, capability_actions, capability_processes, capability_input_delay, capability_remotefx, latest_attempt_error_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	ON CONFLICT(canonical_host) DO UPDATE SET
		latest_attempt_instance_id=excluded.latest_attempt_instance_id, latest_attempt_sequence=excluded.latest_attempt_sequence,
		latest_attempt_observed_at_ms=excluded.latest_attempt_observed_at_ms, latest_attempt_received_at_ms=excluded.latest_attempt_received_at_ms,
		last_success_instance_id=excluded.last_success_instance_id, last_success_sequence=excluded.last_success_sequence,
		last_success_observed_at_ms=excluded.last_success_observed_at_ms, last_success_received_at_ms=excluded.last_success_received_at_ms,
		session_count=excluded.session_count, active_count=excluded.active_count, idle_count=excluded.idle_count,
		disconnected_count=excluded.disconnected_count, user_count=excluded.user_count, last_activity_at_ms=excluded.last_activity_at_ms,
		collector_version=excluded.collector_version, logical_cpu_count=excluded.logical_cpu_count,
		capability_actions=excluded.capability_actions, capability_processes=excluded.capability_processes,
		capability_input_delay=excluded.capability_input_delay, capability_remotefx=excluded.capability_remotefx,
		latest_attempt_error_code=NULL`,
		snapshot.Host, snapshot.AgentInstanceID, sequence, snapshot.ObservedAtMS, received,
		snapshot.AgentInstanceID, sequence, snapshot.ObservedAtMS, received,
		aggregates.SessionCount, aggregates.ActiveCount, aggregates.IdleCount, aggregates.DisconnectedCount, aggregates.UserCount, aggregates.LastActivityAtMS,
		snapshot.CollectorVersion, snapshot.LogicalCPUCount, boolInt(snapshot.Capabilities.SessionActions), boolInt(snapshot.Capabilities.Processes), boolInt(snapshot.Capabilities.InputDelay), boolInt(snapshot.Capabilities.RemoteFX))
	if err != nil {
		return fmt.Errorf("telemetry: upsert session snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_latest WHERE canonical_host=?`, snapshot.Host); err != nil {
		return fmt.Errorf("telemetry: replace session rows: %w", err)
	}
	for _, session := range snapshot.Sessions {
		if err := insertSessionRow(ctx, tx, snapshot.Host, session); err != nil {
			return err
		}
	}
	return nil
}

func insertSessionRow(ctx context.Context, tx *sql.Tx, host string, session sessiondata.SessionRecord) error {
	processes, err := json.Marshal(session.Processes)
	if err != nil {
		return fmt.Errorf("telemetry: encode session processes: %w", err)
	}
	var rfx sessiondata.RemoteFXMetrics
	if session.RemoteFX != nil {
		rfx = *session.RemoteFX
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_latest (
		canonical_host, session_id, logon_at_ms, user_name, domain_name, state, station, client_name, client_address,
		connect_at_ms, disconnect_at_ms, idle_since_ms, cpu_percent, working_set_bytes, input_delay_ms,
		remotefx_fps, remotefx_quality_pct, remotefx_encode_ms, remotefx_rtt_ms, remotefx_loss_pct, remotefx_server_skip, remotefx_network_skip, processes_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		host, session.SessionID, session.LogonAtMS, session.User, session.Domain, session.State, session.Station, session.ClientName, session.ClientAddress,
		session.ConnectAtMS, session.DisconnectAtMS, session.IdleSinceMS, session.CPUPercent, decimalPtrInt64(session.WorkingSetBytes), session.InputDelayMS,
		rfx.FPS, rfx.QualityPercent, rfx.EncodeTimeMS, rfx.RTTMS, rfx.LossPercent, rfx.ServerSkippedFPS, rfx.NetworkSkippedFPS, string(processes))
	if err != nil {
		return fmt.Errorf("telemetry: insert session row: %w", err)
	}
	return nil
}

// PurgeSnapshots deletes expired current snapshots. A no-success fatal-only attempt
// expires by its latest attempt receipt; all successful snapshots expire by success receipt.
func (s *SessionSnapshotStore) PurgeSnapshots(ctx context.Context, retentionHours int, now time.Time) (int64, error) {
	if retentionHours < 1 || retentionHours > 168 {
		return 0, fmt.Errorf("telemetry: session retention must be 1..168 hours")
	}
	cutoff := now.UTC().Add(-time.Duration(retentionHours) * time.Hour).UnixMilli()
	result, err := s.db.writer.ExecContext(ctx, `DELETE FROM session_snapshots WHERE COALESCE(last_success_received_at_ms, latest_attempt_received_at_ms) < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("telemetry: purge session snapshots: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("telemetry: session purge rows affected: %w", err)
	}
	return count, nil
}

// PurgeForPrivacy irreversibly discards all retained projected data after any privacy policy change.
func (s *SessionSnapshotStore) PurgeForPrivacy(ctx context.Context) error {
	if _, err := s.db.writer.ExecContext(ctx, `DELETE FROM session_snapshots`); err != nil {
		return fmt.Errorf("telemetry: purge session privacy data: %w", err)
	}
	return nil
}

func sequenceBytes(value uint64) []byte {
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, value)
	return result
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func decimalPtrInt64(value *sessiondata.DecimalUint64) any {
	if value == nil {
		return nil
	}
	return int64(value.Uint64())
}

func validateSnapshotForStore(snapshot sessiondata.SessionSnapshot) error {
	if snapshot.Schema != sessiondata.SnapshotSchema {
		return fmt.Errorf("telemetry: invalid session schema")
	}
	if err := sessiondata.ValidateCanonicalHost(snapshot.Host); err != nil {
		return fmt.Errorf("telemetry: invalid session host: %w", err)
	}
	if _, err := sessiondata.ParseUUIDv7(snapshot.AgentInstanceID); err != nil {
		return fmt.Errorf("telemetry: invalid session generation: %w", err)
	}
	if snapshot.ObservedAtMS < 0 || snapshot.ObservedAtMS > sessiondata.MaxUnixMillis || len(snapshot.Sessions) > sessiondata.MaxSessions {
		return fmt.Errorf("telemetry: invalid session snapshot bounds")
	}
	if snapshot.CollectionError != nil {
		if len(snapshot.Sessions) != 0 || snapshot.Capabilities.SessionActions || snapshot.Capabilities.Processes || snapshot.Capabilities.InputDelay || snapshot.Capabilities.RemoteFX {
			return fmt.Errorf("telemetry: invalid fatal session snapshot")
		}
		return nil
	}
	for _, session := range snapshot.Sessions {
		if !session.State.Valid() || len(session.Processes) > sessiondata.MaxProcesses {
			return fmt.Errorf("telemetry: invalid session row")
		}
		if session.WorkingSetBytes != nil && session.WorkingSetBytes.Uint64() > sessiondata.MaxStoredBytes {
			return fmt.Errorf("telemetry: session working set exceeds SQLite range")
		}
		for _, process := range session.Processes {
			if process.PID == 0 || process.WorkingSetBytes.Uint64() > sessiondata.MaxStoredBytes {
				return fmt.Errorf("telemetry: invalid session process")
			}
		}
	}
	return nil
}
