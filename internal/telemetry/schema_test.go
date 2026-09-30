//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplySchema_FreshDB(t *testing.T) {
	db := openTestDB(t)

	wantTables := []string{
		"schema_meta",
		"audit",
		"metrics_raw",
		"metrics_5min",
		"metrics_hourly",
		"maintenance_jobs",
		"event_spikes",
		"servers",
		"server_exclusions",
		"host_freshness",
		"force_update_outbox",
		"session_snapshots",
		"session_generation_fence",
		"session_action_outbox",
		"session_action_audit",
		"session_action_ledger",
		"investigation_attempts",
		"investigation_evidence",
		"investigation_evidence_facts",
		"investigation_results",
		"investigation_result_summary_facts",
		"investigation_hypotheses",
		"investigation_hypothesis_facts",
		"investigation_missing_evidence",
		"investigation_missing_evidence_facts",
		"investigation_recommended_diagnostic_checks",
		"investigation_recommended_diagnostic_check_facts",
		"investigation_recommended_diagnostic_check_hypotheses",
		"investigation_provenance",
		"investigation_privacy_acknowledgements",
		"session_drop_anomalies",
		"session_drop_baselines",
		"session_drop_baseline_days",
		"session_drop_detector_state",
		"session_drop_observation_inbox",
	}
	for _, tbl := range wantTables {
		var name string
		err := db.writer.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after schema apply: %v", tbl, err)
		}
	}

	if got := queryPragmaInt(t, db, "user_version"); got != schemaVersion {
		t.Errorf("user_version = %d, want %d", got, schemaVersion)
	}
}

func TestApplySchema_V4TriggerBodiesAndForeignKeys(t *testing.T) {
	db := openTestDB(t)

	if got := queryPragmaInt(t, db, "foreign_keys"); got != 1 {
		t.Fatalf("foreign_keys = %d, want 1", got)
	}
	const (
		sourceTime = 1_000_000_000
		snapshotAt = sourceTime + 1
	)
	if _, err := db.writer.Exec(`
		INSERT INTO investigation_attempts (
			source_kind, source_id, attempt_no, initiation, state, created_at_ms,
			queued_at_ms, evidence_hash
		) VALUES ('event_spike', 1, 1, 'manual', 'queued', ?, ?, zeroblob(32))`,
		snapshotAt, snapshotAt,
	); err != nil {
		t.Fatalf("insert queued attempt: %v", err)
	}
	var attemptID int64
	if err := db.writer.QueryRow(`SELECT id FROM investigation_attempts`).Scan(&attemptID); err != nil {
		t.Fatalf("read attempt id: %v", err)
	}
	if _, err := db.writer.Exec(`
		INSERT INTO investigation_evidence (
			attempt_id, dto_version, snapshot_kind, source_time_ms, snapshot_at_ms,
			from_ms, to_ms, canonical_json
		) VALUES (?, 1, 'unavailable', ?, ?, ?, ?, '{}')`,
		attemptID, sourceTime, snapshotAt, sourceTime-1_800_000, snapshotAt,
	); err != nil {
		t.Fatalf("insert unavailable evidence: %v", err)
	}
	if _, err := db.writer.Exec(
		`INSERT INTO investigation_evidence_facts(attempt_id, fact_id, ordinal) VALUES (?, 'F001', 1)`,
		attemptID,
	); err == nil {
		t.Fatal("unavailable evidence accepted a fact")
	}
	if _, err := db.writer.Exec(`
		UPDATE investigation_attempts
		SET state = 'insufficient_evidence', started_at_ms = ?, completed_at_ms = ?,
		    terminal_reason = 'evidence_unavailable'
		WHERE id = ?`,
		snapshotAt, snapshotAt, attemptID,
	); err != nil {
		t.Fatalf("complete unavailable attempt: %v", err)
	}
	if _, err := db.writer.Exec(
		`UPDATE investigation_attempts SET completed_at_ms = completed_at_ms + 1 WHERE id = ?`,
		attemptID,
	); err == nil {
		t.Fatal("terminal attempt was mutable")
	}
	if _, err := db.writer.Exec(`
		INSERT INTO investigation_privacy_acknowledgements (
			acknowledgement_version, actor, accepted_at_ms, clause_set_hash, clause_set_marker
		) VALUES (
			'openai_responses_privacy_v1', 'operator', ?, ?,
			'openai_responses_privacy_v1_complete_clauses'
		)`, snapshotAt, PrivacyAcknowledgementClauseSetHash(),
	); err != nil {
		t.Fatalf("insert privacy acknowledgement: %v", err)
	}
	if _, err := db.writer.Exec(
		`UPDATE investigation_privacy_acknowledgements SET actor = 'other' WHERE id = 1`,
	); err == nil {
		t.Fatal("privacy acknowledgement was mutable")
	}

	if _, err := db.writer.Exec(`
		INSERT INTO investigation_privacy_acknowledgements (
			acknowledgement_version, actor, accepted_at_ms, clause_set_hash, clause_set_marker
		) VALUES (
			'openai_responses_privacy_v1', 'operator', ?, zeroblob(32),
			'openai_responses_privacy_v1_complete_clauses'
		)`, snapshotAt,
	); err == nil {
		t.Fatal("privacy acknowledgement accepted a noncanonical clause hash")
	}
	if _, err := db.writer.Exec(`DELETE FROM investigation_attempts WHERE id = ?`, attemptID); err != nil {
		t.Fatalf("delete attempt: %v", err)
	}
	var evidenceCount int
	if err := db.writer.QueryRow(`SELECT count(*) FROM investigation_evidence WHERE attempt_id = ?`, attemptID).Scan(&evidenceCount); err != nil {
		t.Fatalf("count cascaded evidence: %v", err)
	}
	if evidenceCount != 0 {
		t.Errorf("cascaded evidence rows = %d, want 0", evidenceCount)
	}
}

func TestPrivacyAcknowledgementStoreRequiresExactImmutableReference(t *testing.T) {
	db := openTestDB(t)
	store := NewPrivacyAcknowledgementStore(db)
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := store.Append(context.Background(), "operator", now, make([]byte, 32)); !errors.Is(err, ErrInvalidPrivacyAcknowledgement) {
		t.Fatalf("Append with noncanonical clauses error = %v, want ErrInvalidPrivacyAcknowledgement", err)
	}

	if _, err := store.Append(context.Background(), " \t", now, PrivacyAcknowledgementClauseSetHash()); !errors.Is(err, ErrInvalidPrivacyAcknowledgement) {
		t.Fatalf("Append with blank actor error = %v, want ErrInvalidPrivacyAcknowledgement", err)
	}
	ack, err := store.Append(context.Background(), "operator", now, PrivacyAcknowledgementClauseSetHash())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if ack.ID <= 0 || ack.Version != PrivacyAcknowledgementVersion || ack.Actor != "operator" || !ack.AcceptedAt.Equal(now) {
		t.Fatalf("stored acknowledgement = %#v", ack)
	}
	if current, err := store.IsCurrentReference(context.Background(), ack.ID, PrivacyAcknowledgementVersion, PrivacyAcknowledgementClauseSetHash()); err != nil || !current {
		t.Fatalf("exact current reference = %v, %v; want true, nil", current, err)
	}
	if current, err := store.IsCurrentReference(context.Background(), ack.ID, "stale", PrivacyAcknowledgementClauseSetHash()); err != nil || current {
		t.Fatalf("stale version reference = %v, %v; want false, nil", current, err)
	}
	if current, err := store.IsCurrentReference(context.Background(), ack.ID, PrivacyAcknowledgementVersion, make([]byte, 32)); err != nil || current {
		t.Fatalf("mismatched hash reference = %v, %v; want false, nil", current, err)
	}
	if _, err := db.writer.Exec(`DELETE FROM investigation_privacy_acknowledgements WHERE id = ?`, ack.ID); err != nil {
		t.Fatalf("delete unreferenced acknowledgement: %v", err)
	}
	if current, err := store.IsCurrentReference(context.Background(), ack.ID, PrivacyAcknowledgementVersion, PrivacyAcknowledgementClauseSetHash()); err != nil || current {
		t.Fatalf("missing reference = %v, %v; want false, nil", current, err)
	}
}

func TestApplySchema_UpgradesV3Fixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join(
		"..", "..", "specs", "013-ai-anomaly-investigation", "fixtures", "schema-v3.sql",
	))
	if err != nil {
		t.Fatalf("read v3 fixture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, dbFileName)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v3 fixture database: %v", err)
	}
	if _, err := raw.Exec(string(fixture)); err != nil {
		_ = raw.Close()
		t.Fatalf("apply v3 fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close v3 fixture database: %v", err)
	}

	upgraded, err := Open(dir)
	if err != nil {
		t.Fatalf("upgrade v3 fixture: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if got := queryPragmaInt(t, upgraded, "user_version"); got != schemaVersion {
		t.Errorf("upgraded user_version = %d, want %d", got, schemaVersion)
	}
	var host string
	if err := upgraded.reader.QueryRow(`SELECT host FROM event_spikes WHERE id = 1`).Scan(&host); err != nil {
		t.Fatalf("read preserved v3 spike: %v", err)
	}
	if host != "registered-alpha" {
		t.Errorf("preserved v3 host = %q, want registered-alpha", host)
	}
}

func TestApplySchema_IdempotentOnExistingV4(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if got := queryPragmaInt(t, db1, "user_version"); got != schemaVersion {
		t.Errorf("after first open: user_version = %d, want %d", got, schemaVersion)
	}
	_ = db1.Close()

	// Second open — applySchema must not return an error on a current DB.
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	defer func() { _ = db2.Close() }()

	if got := queryPragmaInt(t, db2, "user_version"); got != schemaVersion {
		t.Errorf("after second open: user_version = %d, want %d", got, schemaVersion)
	}
}
