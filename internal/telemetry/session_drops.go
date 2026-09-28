//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const sessionDropModelVersion = "gamma_poisson_lower_v1"

// SessionDropObservation is the immutable, typed detector input committed with
// an accepted server report. AcceptedSequence is assigned only by SQLite.
type SessionDropObservation struct {
	AcceptedSequence      int64
	CanonicalHost         string
	ReportEpochMs         int64
	AcceptedAtMs          int64
	LocalOffsetMinutes    int
	LocalDate             string
	SessionPresence       string
	ActiveSessions        *int64
	DisconnectedSessions  *int64
	FreshnessContext      string
	DrainContext          string
	ClassificationContext string
}

// SessionDropBaseline is one host-local slot or all-hours sufficient-statistics row.
type SessionDropBaseline struct {
	ID                    int64
	Host                  string
	Scope                 string
	SlotIndex             *int
	ModelVersion          string
	Alpha                 float64
	Beta                  float64
	ObservationCount      int64
	FirstTrainedAtMs      *int64
	LastNormalTrainedAtMs *int64
	LastUpdatedAtMs       int64
	LocalDates            []string
}

// SessionDropDetectorState is the persisted host-local ordering, confirmation,
// cooldown, and drain-horizon state. Confirmation arrays are oldest first.
type SessionDropDetectorState struct {
	Host                     string
	LastScoredReportEpochMs  int64
	LastReferenceTotal       *int64
	CooldownUntilMs          int64
	PostDrainRemaining       int
	ConfirmationReportEpochs [3]*int64
	ConfirmationCandidates   [3]*bool
	ConfirmationContexts     [3]*string
	ConfirmationAcceptedAtMs [3]*int64
	LastGapReason            string
	StateUpdatedAtMs         int64
}

// SessionDropSource is the durable deterministic source row. Confirmation
// timestamps are central acceptance times and deliberately separate from report epochs.
type SessionDropSource struct {
	ID                               int64
	Host                             string
	ReportEpochMs                    int64
	AcceptedAtMs                     int64
	LocalOffsetMinutes               int
	LocalDate                        string
	DetectedAtMs                     int64
	ConfirmationStartedReportEpochMs int64
	ConfirmationEndedReportEpochMs   int64
	ConfirmationStartedAtMs          int64
	ConfirmationEndedAtMs            int64
	ObservedSessions                 int64
	ReferenceSessions                int64
	ExpectedSessions                 float64
	BaselineModelVersion             string
	BaselineScope                    string
	SlotIndex                        *int
	SlotMatureDays                   *int
	TailProbability                  float64
	AbsoluteLoss                     int64
	RelativeLoss                     float64
	ConfirmationWindowSize           int
	ConfirmationCandidates           [3]*bool
	ConfirmationCount                int
	FreshnessContext                 string
	DrainContext                     string
	Classification                   string
	ProviderEligible                 bool
}

// SessionDropStore owns durable lower-tail detector state and sources.
type SessionDropStore struct{ db *DB }

func NewSessionDropStore(db *DB) *SessionDropStore { return &SessionDropStore{db: db} }

// InsertObservationTx records an accepted observation in an existing acceptance
// transaction. It is idempotent by host/report epoch and SQLite assigns order.
func (s *SessionDropStore) InsertObservationTx(ctx context.Context, tx *sql.Tx, observation SessionDropObservation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_drop_observation_inbox
		(canonical_host, report_epoch_ms, accepted_at_ms, local_offset_minutes, local_date, session_presence,
		 active_sessions, disconnected_sessions, total_sessions, freshness_context, drain_context, classification_context)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? = 'present' THEN ? + ? ELSE NULL END, ?, ?, ?)
		ON CONFLICT(canonical_host, report_epoch_ms) DO NOTHING`,
		CanonicalHostname(observation.CanonicalHost), observation.ReportEpochMs, observation.AcceptedAtMs,
		observation.LocalOffsetMinutes, observation.LocalDate, observation.SessionPresence,
		observation.ActiveSessions, observation.DisconnectedSessions, observation.SessionPresence,
		observation.ActiveSessions, observation.DisconnectedSessions, observation.FreshnessContext,
		observation.DrainContext, observation.ClassificationContext)
	if err != nil {
		return fmt.Errorf("telemetry: insert session-drop observation: %w", err)
	}
	return nil
}

// InsertObservation commits one idempotent inbox row when a caller does not
// already own the report-acceptance transaction.
func (s *SessionDropStore) InsertObservation(ctx context.Context, observation SessionDropObservation) error {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: session-drop observation begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.InsertObservationTx(ctx, tx, observation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: session-drop observation commit: %w", err)
	}
	return nil
}

// PendingObservation returns the next globally ordered detector input without
// consuming it. It exists for startup diagnostics and acceptance tests; normal
// detector work must use ConsumeNext.
func (s *SessionDropStore) PendingObservation(ctx context.Context) (*SessionDropObservation, error) {
	row := s.db.reader.QueryRowContext(ctx, `SELECT accepted_sequence, canonical_host, report_epoch_ms, accepted_at_ms,
		local_offset_minutes, local_date, session_presence, active_sessions, disconnected_sessions,
		freshness_context, drain_context, classification_context
		FROM session_drop_observation_inbox ORDER BY accepted_sequence ASC LIMIT 1`)
	observation, err := scanSessionDropObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: get pending session-drop observation: %w", err)
	}
	return &observation, nil
}

// ConsumeNext atomically applies consume to the globally earliest pending row
// and removes exactly that row. Returning nil,nil means the inbox is empty.
func (s *SessionDropStore) ConsumeNext(ctx context.Context, consume func(context.Context, *sql.Tx, SessionDropObservation) error) (bool, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("telemetry: consume session-drop begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	row := tx.QueryRowContext(ctx, `SELECT accepted_sequence, canonical_host, report_epoch_ms, accepted_at_ms,
		local_offset_minutes, local_date, session_presence, active_sessions, disconnected_sessions,
		freshness_context, drain_context, classification_context
		FROM session_drop_observation_inbox ORDER BY accepted_sequence ASC LIMIT 1`)
	observation, err := scanSessionDropObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("telemetry: consume session-drop select: %w", err)
	}
	if err := consume(ctx, tx, observation); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_drop_observation_inbox WHERE accepted_sequence = ?`, observation.AcceptedSequence); err != nil {
		return false, fmt.Errorf("telemetry: consume session-drop delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("telemetry: consume session-drop commit: %w", err)
	}
	return true, nil
}

func scanSessionDropObservation(row interface{ Scan(...any) error }) (SessionDropObservation, error) {
	var observation SessionDropObservation
	var active, disconnected sql.NullInt64
	err := row.Scan(&observation.AcceptedSequence, &observation.CanonicalHost, &observation.ReportEpochMs, &observation.AcceptedAtMs,
		&observation.LocalOffsetMinutes, &observation.LocalDate, &observation.SessionPresence, &active, &disconnected,
		&observation.FreshnessContext, &observation.DrainContext, &observation.ClassificationContext)
	if err != nil {
		return SessionDropObservation{}, err
	}
	if active.Valid {
		value := active.Int64
		observation.ActiveSessions = &value
	}
	if disconnected.Valid {
		value := disconnected.Int64
		observation.DisconnectedSessions = &value
	}
	return observation, nil
}

func (s *SessionDropStore) UpsertBaselineTx(ctx context.Context, tx *sql.Tx, baseline SessionDropBaseline) error {
	if baseline.ModelVersion == "" {
		baseline.ModelVersion = sessionDropModelVersion
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO session_drop_baselines
		(host, scope, slot_index, model_version, alpha, beta, observation_count, first_trained_at_ms, last_normal_trained_at_ms, last_updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO UPDATE SET alpha=excluded.alpha, beta=excluded.beta, observation_count=excluded.observation_count,
		first_trained_at_ms=excluded.first_trained_at_ms, last_normal_trained_at_ms=excluded.last_normal_trained_at_ms,
		last_updated_at_ms=excluded.last_updated_at_ms
		RETURNING id`, CanonicalHostname(baseline.Host), baseline.Scope, baseline.SlotIndex, baseline.ModelVersion,
		baseline.Alpha, baseline.Beta, baseline.ObservationCount, baseline.FirstTrainedAtMs, baseline.LastNormalTrainedAtMs,
		baseline.LastUpdatedAtMs).Scan(&id)
	if err != nil {
		return fmt.Errorf("telemetry: upsert session-drop baseline: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_drop_baseline_days WHERE baseline_id = ?`, id); err != nil {
		return fmt.Errorf("telemetry: clear session-drop baseline dates: %w", err)
	}
	for _, date := range baseline.LocalDates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_drop_baseline_days (baseline_id, local_date) VALUES (?, ?)`, id, date); err != nil {
			return fmt.Errorf("telemetry: insert session-drop baseline date: %w", err)
		}
	}
	return nil
}

func (s *SessionDropStore) UpsertBaseline(ctx context.Context, baseline SessionDropBaseline) error {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: upsert session-drop baseline begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.UpsertBaselineTx(ctx, tx, baseline); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: upsert session-drop baseline commit: %w", err)
	}
	return nil
}

type sessionDropQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *SessionDropStore) BaselineTx(ctx context.Context, tx *sql.Tx, host, scope string, slotIndex *int) (*SessionDropBaseline, error) {
	return s.baseline(ctx, tx, host, scope, slotIndex)
}

func (s *SessionDropStore) Baseline(ctx context.Context, host, scope string, slotIndex *int) (*SessionDropBaseline, error) {
	return s.baseline(ctx, s.db.reader, host, scope, slotIndex)
}

func (s *SessionDropStore) baseline(ctx context.Context, queryer sessionDropQueryer, host, scope string, slotIndex *int) (*SessionDropBaseline, error) {
	row := queryer.QueryRowContext(ctx, `SELECT id, host, scope, slot_index, model_version, alpha, beta, observation_count,
		first_trained_at_ms, last_normal_trained_at_ms, last_updated_at_ms FROM session_drop_baselines
		WHERE host = ? COLLATE NOCASE AND scope = ? AND slot_index IS ?`, CanonicalHostname(host), scope, slotIndex)
	var baseline SessionDropBaseline
	var slot sql.NullInt64
	var first, last sql.NullInt64
	if err := row.Scan(&baseline.ID, &baseline.Host, &baseline.Scope, &slot, &baseline.ModelVersion, &baseline.Alpha, &baseline.Beta,
		&baseline.ObservationCount, &first, &last, &baseline.LastUpdatedAtMs); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("telemetry: get session-drop baseline: %w", err)
	}
	if slot.Valid {
		value := int(slot.Int64)
		baseline.SlotIndex = &value
	}
	if first.Valid {
		value := first.Int64
		baseline.FirstTrainedAtMs = &value
	}
	if last.Valid {
		value := last.Int64
		baseline.LastNormalTrainedAtMs = &value
	}
	rows, err := queryer.QueryContext(ctx, `SELECT local_date FROM session_drop_baseline_days WHERE baseline_id = ? ORDER BY local_date`, baseline.ID)
	if err != nil {
		return nil, fmt.Errorf("telemetry: get session-drop baseline dates: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, fmt.Errorf("telemetry: scan session-drop baseline date: %w", err)
		}
		baseline.LocalDates = append(baseline.LocalDates, date)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("telemetry: read session-drop baseline dates: %w", err)
	}
	return &baseline, nil
}

func (s *SessionDropStore) SaveDetectorStateTx(ctx context.Context, tx *sql.Tx, state SessionDropDetectorState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_drop_detector_state (host, last_scored_report_epoch_ms, last_reference_total,
		cooldown_until_ms, post_drain_remaining, confirmation_1_report_epoch_ms, confirmation_1_candidate, confirmation_1_context, confirmation_1_accepted_at_ms,
		confirmation_2_report_epoch_ms, confirmation_2_candidate, confirmation_2_context, confirmation_2_accepted_at_ms,
		confirmation_3_report_epoch_ms, confirmation_3_candidate, confirmation_3_context, confirmation_3_accepted_at_ms, last_gap_reason, state_updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host) DO UPDATE SET last_scored_report_epoch_ms=excluded.last_scored_report_epoch_ms,
		last_reference_total=excluded.last_reference_total, cooldown_until_ms=excluded.cooldown_until_ms,
		post_drain_remaining=excluded.post_drain_remaining, confirmation_1_report_epoch_ms=excluded.confirmation_1_report_epoch_ms,
		confirmation_1_candidate=excluded.confirmation_1_candidate, confirmation_1_context=excluded.confirmation_1_context,
		confirmation_1_accepted_at_ms=excluded.confirmation_1_accepted_at_ms,
		confirmation_2_report_epoch_ms=excluded.confirmation_2_report_epoch_ms, confirmation_2_candidate=excluded.confirmation_2_candidate,
		confirmation_2_context=excluded.confirmation_2_context, confirmation_2_accepted_at_ms=excluded.confirmation_2_accepted_at_ms,
		confirmation_3_report_epoch_ms=excluded.confirmation_3_report_epoch_ms, confirmation_3_candidate=excluded.confirmation_3_candidate,
		confirmation_3_context=excluded.confirmation_3_context, confirmation_3_accepted_at_ms=excluded.confirmation_3_accepted_at_ms,
		last_gap_reason=excluded.last_gap_reason, state_updated_at_ms=excluded.state_updated_at_ms`,
		CanonicalHostname(state.Host), state.LastScoredReportEpochMs, state.LastReferenceTotal, state.CooldownUntilMs, state.PostDrainRemaining,
		state.ConfirmationReportEpochs[0], state.ConfirmationCandidates[0], state.ConfirmationContexts[0], state.ConfirmationAcceptedAtMs[0],
		state.ConfirmationReportEpochs[1], state.ConfirmationCandidates[1], state.ConfirmationContexts[1], state.ConfirmationAcceptedAtMs[1],
		state.ConfirmationReportEpochs[2], state.ConfirmationCandidates[2], state.ConfirmationContexts[2], state.ConfirmationAcceptedAtMs[2],
		state.LastGapReason, state.StateUpdatedAtMs)
	if err != nil {
		return fmt.Errorf("telemetry: save session-drop detector state: %w", err)
	}
	return nil
}

func (s *SessionDropStore) SaveDetectorState(ctx context.Context, state SessionDropDetectorState) error {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry: save detector state begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.SaveDetectorStateTx(ctx, tx, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry: save detector state commit: %w", err)
	}
	return nil
}

func (s *SessionDropStore) DetectorState(ctx context.Context, host string) (*SessionDropDetectorState, error) {
	row := s.db.reader.QueryRowContext(ctx, `SELECT host, last_scored_report_epoch_ms, last_reference_total, cooldown_until_ms, post_drain_remaining,
		confirmation_1_report_epoch_ms, confirmation_1_candidate, confirmation_1_context, confirmation_1_accepted_at_ms,
		confirmation_2_report_epoch_ms, confirmation_2_candidate, confirmation_2_context, confirmation_2_accepted_at_ms,
		confirmation_3_report_epoch_ms, confirmation_3_candidate, confirmation_3_context, confirmation_3_accepted_at_ms,
		last_gap_reason, state_updated_at_ms FROM session_drop_detector_state WHERE host = ? COLLATE NOCASE`, CanonicalHostname(host))
	state, err := scanDetectorState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: get session-drop detector state: %w", err)
	}
	return &state, nil
}

func (s *SessionDropStore) DetectorStateTx(ctx context.Context, tx *sql.Tx, host string) (*SessionDropDetectorState, error) {
	row := tx.QueryRowContext(ctx, `SELECT host, last_scored_report_epoch_ms, last_reference_total, cooldown_until_ms, post_drain_remaining,
		confirmation_1_report_epoch_ms, confirmation_1_candidate, confirmation_1_context, confirmation_1_accepted_at_ms,
		confirmation_2_report_epoch_ms, confirmation_2_candidate, confirmation_2_context, confirmation_2_accepted_at_ms,
		confirmation_3_report_epoch_ms, confirmation_3_candidate, confirmation_3_context, confirmation_3_accepted_at_ms,
		last_gap_reason, state_updated_at_ms FROM session_drop_detector_state WHERE host = ? COLLATE NOCASE`, CanonicalHostname(host))
	state, err := scanDetectorState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: get session-drop detector state: %w", err)
	}
	return &state, nil
}

func scanDetectorState(row interface{ Scan(...any) error }) (SessionDropDetectorState, error) {
	var state SessionDropDetectorState
	var reference sql.NullInt64
	var epochs [3]sql.NullInt64
	var candidates [3]sql.NullBool
	var contexts [3]sql.NullString
	var acceptedAtMs [3]sql.NullInt64
	err := row.Scan(&state.Host, &state.LastScoredReportEpochMs, &reference, &state.CooldownUntilMs, &state.PostDrainRemaining,
		&epochs[0], &candidates[0], &contexts[0], &acceptedAtMs[0],
		&epochs[1], &candidates[1], &contexts[1], &acceptedAtMs[1],
		&epochs[2], &candidates[2], &contexts[2], &acceptedAtMs[2],
		&state.LastGapReason, &state.StateUpdatedAtMs)
	if err != nil {
		return SessionDropDetectorState{}, err
	}
	if reference.Valid {
		value := reference.Int64
		state.LastReferenceTotal = &value
	}
	for i := range epochs {
		if epochs[i].Valid {
			value := epochs[i].Int64
			state.ConfirmationReportEpochs[i] = &value
		}
		if candidates[i].Valid {
			value := candidates[i].Bool
			state.ConfirmationCandidates[i] = &value
		}
		if contexts[i].Valid {
			value := contexts[i].String
			state.ConfirmationContexts[i] = &value
		}
		if acceptedAtMs[i].Valid {
			value := acceptedAtMs[i].Int64
			state.ConfirmationAcceptedAtMs[i] = &value
		}
	}
	return state, nil
}

func (s *SessionDropStore) InsertSourceTx(ctx context.Context, tx *sql.Tx, source SessionDropSource) (SessionDropSource, bool, error) {
	if source.BaselineModelVersion == "" {
		source.BaselineModelVersion = sessionDropModelVersion
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO session_drop_anomalies (host, report_epoch_ms, accepted_at_ms, local_offset_minutes, local_date,
		detected_at_ms, confirmation_started_report_epoch_ms, confirmation_ended_report_epoch_ms, confirmation_started_at_ms,
		confirmation_ended_at_ms, observed_sessions, reference_sessions, expected_sessions, baseline_model_version, baseline_scope,
		slot_index, slot_mature_days, tail_probability, absolute_loss, relative_loss, confirmation_window_size, confirmation_1_candidate,
		confirmation_2_candidate, confirmation_3_candidate, confirmation_count, freshness_context, drain_context, classification, provider_eligible)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host, confirmation_ended_report_epoch_ms) DO NOTHING`, CanonicalHostname(source.Host), source.ReportEpochMs, source.AcceptedAtMs,
		source.LocalOffsetMinutes, source.LocalDate, source.DetectedAtMs, source.ConfirmationStartedReportEpochMs, source.ConfirmationEndedReportEpochMs,
		source.ConfirmationStartedAtMs, source.ConfirmationEndedAtMs, source.ObservedSessions, source.ReferenceSessions, source.ExpectedSessions,
		source.BaselineModelVersion, source.BaselineScope, source.SlotIndex, source.SlotMatureDays, source.TailProbability, source.AbsoluteLoss,
		source.RelativeLoss, source.ConfirmationWindowSize, source.ConfirmationCandidates[0], source.ConfirmationCandidates[1], source.ConfirmationCandidates[2],
		source.ConfirmationCount, source.FreshnessContext, source.DrainContext, source.Classification, source.ProviderEligible)
	if err != nil {
		return SessionDropSource{}, false, fmt.Errorf("telemetry: insert session-drop source: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return SessionDropSource{}, false, fmt.Errorf("telemetry: insert session-drop source rows affected: %w", err)
	}
	if inserted == 0 {
		existing, err := lookupSessionDropSource(ctx, tx, source.Host, source.ConfirmationEndedReportEpochMs)
		return existing, false, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SessionDropSource{}, false, fmt.Errorf("telemetry: insert session-drop source id: %w", err)
	}
	source.ID = id
	return source, true, nil
}

func (s *SessionDropStore) InsertSource(ctx context.Context, source SessionDropSource) (SessionDropSource, bool, error) {
	tx, err := s.db.writer.BeginTx(ctx, nil)
	if err != nil {
		return SessionDropSource{}, false, fmt.Errorf("telemetry: insert session-drop source begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	inserted, created, err := s.InsertSourceTx(ctx, tx, source)
	if err != nil {
		return SessionDropSource{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SessionDropSource{}, false, fmt.Errorf("telemetry: insert session-drop source commit: %w", err)
	}
	return inserted, created, nil
}

func (s *SessionDropStore) Source(ctx context.Context, id int64) (*SessionDropSource, error) {
	row := s.db.reader.QueryRowContext(ctx, sessionDropSourceSelect+` WHERE id = ?`, id)
	source, err := scanSessionDropSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: get session-drop source: %w", err)
	}
	return &source, nil
}

const sessionDropSourceSelect = `SELECT id, host, report_epoch_ms, accepted_at_ms, local_offset_minutes, local_date, detected_at_ms,
	confirmation_started_report_epoch_ms, confirmation_ended_report_epoch_ms, confirmation_started_at_ms, confirmation_ended_at_ms,
	observed_sessions, reference_sessions, expected_sessions, baseline_model_version, baseline_scope, slot_index, slot_mature_days,
	tail_probability, absolute_loss, relative_loss, confirmation_window_size, confirmation_1_candidate, confirmation_2_candidate,
	confirmation_3_candidate, confirmation_count, freshness_context, drain_context, classification, provider_eligible FROM session_drop_anomalies`

func lookupSessionDropSource(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, host string, endingEpoch int64) (SessionDropSource, error) {
	row := query.QueryRowContext(ctx, sessionDropSourceSelect+` WHERE host = ? COLLATE NOCASE AND confirmation_ended_report_epoch_ms = ?`, CanonicalHostname(host), endingEpoch)
	return scanSessionDropSource(row)
}

func scanSessionDropSource(row interface{ Scan(...any) error }) (SessionDropSource, error) {
	var source SessionDropSource
	var slot, mature sql.NullInt64
	var c3 sql.NullBool
	var eligible int
	err := row.Scan(&source.ID, &source.Host, &source.ReportEpochMs, &source.AcceptedAtMs, &source.LocalOffsetMinutes, &source.LocalDate, &source.DetectedAtMs, &source.ConfirmationStartedReportEpochMs, &source.ConfirmationEndedReportEpochMs, &source.ConfirmationStartedAtMs, &source.ConfirmationEndedAtMs, &source.ObservedSessions, &source.ReferenceSessions, &source.ExpectedSessions, &source.BaselineModelVersion, &source.BaselineScope, &slot, &mature, &source.TailProbability, &source.AbsoluteLoss, &source.RelativeLoss, &source.ConfirmationWindowSize, &source.ConfirmationCandidates[0], &source.ConfirmationCandidates[1], &c3, &source.ConfirmationCount, &source.FreshnessContext, &source.DrainContext, &source.Classification, &eligible)
	if err != nil {
		return SessionDropSource{}, err
	}
	if slot.Valid {
		value := int(slot.Int64)
		source.SlotIndex = &value
	}
	if mature.Valid {
		value := int(mature.Int64)
		source.SlotMatureDays = &value
	}
	if c3.Valid {
		value := c3.Bool
		source.ConfirmationCandidates[2] = &value
	}
	source.ProviderEligible = eligible != 0
	return source, nil
}

func (s *SessionDropStore) ListSources(ctx context.Context, limit int, beforeID int64) ([]SessionDropSource, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("telemetry: session-drop list limit must be positive")
	}
	query := sessionDropSourceSelect + ` WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`
	rows, err := s.db.reader.QueryContext(ctx, query, beforeID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("telemetry: list session-drop sources: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	sources := make([]SessionDropSource, 0, limit)
	for rows.Next() {
		source, err := scanSessionDropSource(rows)
		if err != nil {
			return nil, fmt.Errorf("telemetry: scan session-drop source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("telemetry: list session-drop sources rows: %w", err)
	}
	return sources, nil
}

func (s *SessionDropStore) removeHostStateTx(ctx context.Context, tx *sql.Tx, hostname string) error {
	hostname = CanonicalHostname(hostname)
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_drop_observation_inbox WHERE canonical_host = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: remove session-drop inbox: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_drop_detector_state WHERE host = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: remove session-drop detector state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_drop_baselines WHERE host = ? COLLATE NOCASE`, hostname); err != nil {
		return fmt.Errorf("telemetry: remove session-drop baselines: %w", err)
	}
	return nil
}

// SessionDropSourceTime converts a persisted millisecond timestamp for callers
// that need the exact UTC REST projection.
func SessionDropSourceTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
