//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func newRemovalStore(t *testing.T) (*RemovalStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewRemovalStore(db), db
}

func TestExclusions_ExcludeIsIdempotent(t *testing.T) {
	s, _ := newRemovalStore(t)

	if err := s.Exclude(context.Background(), "SRV01", "alice", "decommissioned"); err != nil {
		t.Fatalf("first Exclude: %v", err)
	}
	first, err := s.AllExcluded(context.Background())
	if err != nil || len(first) != 1 {
		t.Fatalf("AllExcluded after first: err=%v len=%d", err, len(first))
	}
	if first[0].Hostname != "SRV01" || first[0].ExcludedBy != "alice" || first[0].Reason != "decommissioned" {
		t.Errorf("first AllExcluded entry = %+v", first[0])
	}
	if first[0].ExcludedAt.IsZero() {
		t.Error("ExcludedAt is zero")
	}

	// Second Exclude refreshes metadata but must not duplicate the row.
	time.Sleep(10 * time.Millisecond)
	if err := s.Exclude(context.Background(), "SRV01", "bob", "re-mark"); err != nil {
		t.Fatalf("second Exclude: %v", err)
	}
	second, err := s.AllExcluded(context.Background())
	if err != nil || len(second) != 1 {
		t.Fatalf("AllExcluded after refresh: err=%v len=%d", err, len(second))
	}
	if second[0].ExcludedBy != "bob" || second[0].Reason != "re-mark" {
		t.Errorf("refresh did not update metadata: %+v", second[0])
	}
	if !second[0].ExcludedAt.After(first[0].ExcludedAt) {
		t.Errorf("refresh did not advance ExcludedAt: first=%v second=%v",
			first[0].ExcludedAt, second[0].ExcludedAt)
	}
}

func TestExclusions_IsExcluded(t *testing.T) {
	s, _ := newRemovalStore(t)

	ok, err := s.IsExcluded(context.Background(), "GHOST")
	if err != nil || ok {
		t.Errorf("IsExcluded ghost = (%v,%v); want (false,nil)", ok, err)
	}
	if err := s.Exclude(context.Background(), "SRV01", "alice", ""); err != nil {
		t.Fatalf("Exclude SRV01: %v", err)
	}
	ok, err = s.IsExcluded(context.Background(), "SRV01")
	if err != nil || !ok {
		t.Errorf("IsExcluded SRV01 = (%v,%v); want (true,nil)", ok, err)
	}
}

func TestExclusions_Restore(t *testing.T) {
	s, _ := newRemovalStore(t)

	found, err := s.Restore(context.Background(), "GHOST")
	if err != nil {
		t.Fatalf("Restore ghost: %v", err)
	}
	if found {
		t.Error("Restore of un-excluded host returned found=true")
	}
	if err := s.Exclude(context.Background(), "SRV01", "alice", ""); err != nil {
		t.Fatalf("Exclude SRV01: %v", err)
	}
	found, err = s.Restore(context.Background(), "SRV01")
	if err != nil || !found {
		t.Errorf("Restore SRV01 = (%v,%v); want (true,nil)", found, err)
	}
	ok, _ := s.IsExcluded(context.Background(), "SRV01")
	if ok {
		t.Error("SRV01 still excluded after Restore")
	}
}

func TestExclusions_AllSortedByExcludedAt(t *testing.T) {
	s, _ := newRemovalStore(t)

	// Insert in non-sorted order, advancing the clock via successive Exclude calls.
	for _, name := range []string{"BRAVO", "ALPHA", "CHARLIE"} {
		if err := s.Exclude(context.Background(), name, "alice", ""); err != nil {
			t.Fatalf("Exclude %s: %v", name, err)
		}
		// Give the recorded ms timestamps a chance to differ — the schema
		// stores UnixMilli so two adjacent-ms records could collide on
		// very fast hardware. Sleep a millisecond to disambiguate the
		// primary ordering.
		time.Sleep(2 * time.Millisecond)
	}

	list, err := s.AllExcluded(context.Background())
	if err != nil {
		t.Fatalf("AllExcluded: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	// Newest first: CHARLIE (last excluded), then ALPHA, then BRAVO.
	want := []string{"CHARLIE", "ALPHA", "BRAVO"}
	for i := range want {
		if list[i].Hostname != want[i] {
			t.Errorf("list[%d].Hostname = %q, want %q", i, list[i].Hostname, want[i])
		}
	}
}

// TestExclusions_RestoreThenReExcludeSucceeds asserts the loop that
// BatchOperations' UI uses — restore, then exclude, then restore — does
// not leave dangling state.
func TestExclusions_RestoreThenReExcludeSucceeds(t *testing.T) {
	s, _ := newRemovalStore(t)

	ctx := context.Background()
	if err := s.Exclude(ctx, "SRV01", "alice", "first"); err != nil {
		t.Fatalf("first Exclude: %v", err)
	}
	if ok, _ := s.IsExcluded(ctx, "SRV01"); !ok {
		t.Fatal("SRV01 not excluded after first Exclude")
	}
	if _, err := s.Restore(ctx, "SRV01"); err != nil {
		t.Fatalf("first Restore: %v", err)
	}
	if ok, _ := s.IsExcluded(ctx, "SRV01"); ok {
		t.Fatal("SRV01 still excluded after Restore")
	}
	if err := s.Exclude(ctx, "SRV01", "bob", "second"); err != nil {
		t.Fatalf("second Exclude: %v", err)
	}
	list, _ := s.AllExcluded(ctx)
	if len(list) != 1 || list[0].ExcludedBy != "bob" {
		t.Errorf("after second Exclude, expected one entry with bob, got %+v", list)
	}
}

func TestExclusions_PermanentRemoveRollsBackOnTombstoneFailure(t *testing.T) {
	removals, db := newRemovalStore(t)
	servers := NewServerStore(db)
	ctx := context.Background()
	if err := servers.Register(ctx, "SRV01"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := db.writer.Exec(`
		CREATE TRIGGER fail_permanent_remove_tombstone
		BEFORE INSERT ON server_exclusions
		WHEN NEW.hostname = 'SRV01'
		BEGIN
			SELECT RAISE(FAIL, 'injected tombstone failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if err := removals.PermanentRemove(ctx, "srv01", "alice", "test"); err == nil {
		t.Fatal("PermanentRemove succeeded despite injected tombstone failure")
	}
	if registered, err := servers.IsRegistered(ctx, "SRV01"); err != nil || !registered {
		t.Fatalf("live row lost after rollback: registered=%v err=%v", registered, err)
	}
	if excluded, err := removals.IsExcluded(ctx, "SRV01"); err != nil || excluded {
		t.Fatalf("tombstone persisted after rollback: excluded=%v err=%v", excluded, err)
	}
}

func TestExclusions_RegisterLosingPermanentRemoveRaceIsNotAccepted(t *testing.T) {
	removals, db := newRemovalStore(t)
	servers := NewServerStore(db)
	ctx := context.Background()
	if err := servers.Register(ctx, "SRV01"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The production writer has one connection, which serializes write
	// transactions. A second SQLite writer lets this test force the other
	// valid interleaving: permanent removal commits after the registration
	// predicate but before its insert obtains a write lock.
	otherWriter, err := sql.Open("sqlite", db.path)
	if err != nil {
		t.Fatalf("open competing writer: %v", err)
	}
	defer func() { _ = otherWriter.Close() }()
	otherWriter.SetMaxOpenConns(1)
	if _, err := otherWriter.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("set competing writer busy timeout: %v", err)
	}
	otherRemovals := &RemovalStore{db: &DB{writer: otherWriter, reader: db.reader}}
	removals.beforeRegisterInsert = func() {
		if err := otherRemovals.PermanentRemove(ctx, "srv01", "alice", "race"); err != nil {
			t.Fatalf("PermanentRemove during register: %v", err)
		}
	}

	accepted, excluded, err := removals.RegisterIfNotExcluded(ctx, "SRV01")
	if accepted || excluded || err == nil {
		t.Fatalf("RegisterIfNotExcluded = accepted:%v excluded:%v err:%v, want failed registration", accepted, excluded, err)
	}
	if registered, err := servers.IsRegistered(ctx, "SRV01"); err != nil || registered {
		t.Fatalf("live row survived winning permanent remove: registered=%v err=%v", registered, err)
	}
	if excluded, err := removals.IsExcluded(ctx, "SRV01"); err != nil || !excluded {
		t.Fatalf("tombstone missing after winning permanent remove: excluded=%v err=%v", excluded, err)
	}
}

func TestExclusions_PermanentRemoveClearsOnlyHostDetectorStateAndInbox(t *testing.T) {
	removals, db := newRemovalStore(t)
	servers := NewServerStore(db)
	drops := NewSessionDropStore(db)
	ctx := context.Background()
	for _, host := range []string{"srv-a", "srv-b"} {
		if err := servers.Register(ctx, host); err != nil {
			t.Fatalf("register %s: %v", host, err)
		}
		if err := drops.UpsertBaseline(ctx, SessionDropBaseline{
			Host: host, Scope: "all_hours", ModelVersion: sessionDropModelVersion,
			Alpha: 2, Beta: 2, ObservationCount: 1, FirstTrainedAtMs: int64Pointer(10),
			LastNormalTrainedAtMs: int64Pointer(10), LastUpdatedAtMs: 10,
		}); err != nil {
			t.Fatalf("baseline %s: %v", host, err)
		}
		if err := drops.SaveDetectorState(ctx, SessionDropDetectorState{Host: host, StateUpdatedAtMs: 10}); err != nil {
			t.Fatalf("detector state %s: %v", host, err)
		}
		if err := drops.InsertObservation(ctx, SessionDropObservation{
			CanonicalHost: host, ReportEpochMs: 100, AcceptedAtMs: 100, LocalDate: "2026-09-27",
			SessionPresence: "nil", FreshnessContext: "unknown", DrainContext: "unknown", ClassificationContext: "not_scored",
		}); err != nil {
			t.Fatalf("inbox %s: %v", host, err)
		}
	}
	history, inserted, err := drops.InsertSource(ctx, testSessionDropSource("srv-a", 100))
	if err != nil || !inserted {
		t.Fatalf("insert retained source history: %#v, %t, %v", history, inserted, err)
	}
	if _, err := db.writer.ExecContext(ctx, `INSERT INTO investigation_attempts
		(source_kind, source_id, attempt_no, initiation, state, created_at_ms, queued_at_ms, evidence_hash)
		VALUES ('session_drop', ?, 1, 'manual', 'queued', 100, 100, zeroblob(32))`, history.ID); err != nil {
		t.Fatalf("insert retained investigation history: %v", err)
	}
	if err := removals.PermanentRemove(ctx, "srv-a", "alice", "decommissioned"); err != nil {
		t.Fatalf("permanent remove: %v", err)
	}
	if baseline, err := drops.Baseline(ctx, "srv-a", "all_hours", nil); err != nil || baseline != nil {
		t.Fatalf("removed host baseline = %#v, %v", baseline, err)
	}
	if state, err := drops.DetectorState(ctx, "srv-a"); err != nil || state != nil {
		t.Fatalf("removed host state = %#v, %v", state, err)
	}
	var pending int
	if err := db.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_drop_observation_inbox WHERE canonical_host = 'SRV-A'`).Scan(&pending); err != nil {
		t.Fatalf("count removed host inbox: %v", err)
	}
	if pending != 0 {
		t.Fatalf("removed host pending inbox rows = %d, want 0", pending)
	}
	if retained, err := drops.Source(ctx, history.ID); err != nil || retained == nil || retained.Host != "SRV-A" {
		t.Fatalf("retained source history = %#v, %v", retained, err)
	}
	var attempts int
	if err := db.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM investigation_attempts WHERE source_kind = 'session_drop' AND source_id = ?`, history.ID).Scan(&attempts); err != nil {
		t.Fatalf("count retained investigation history: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("retained investigation attempts = %d, want 1", attempts)
	}
	if baseline, err := drops.Baseline(ctx, "srv-b", "all_hours", nil); err != nil || baseline == nil {
		t.Fatalf("other host baseline = %#v, %v", baseline, err)
	}
	if state, err := drops.DetectorState(ctx, "srv-b"); err != nil || state == nil {
		t.Fatalf("other host state = %#v, %v", state, err)
	}
	pendingObservation, err := drops.PendingObservation(ctx)
	if err != nil || pendingObservation == nil || pendingObservation.CanonicalHost != "SRV-B" {
		t.Fatalf("other host pending inbox = %#v, %v", pendingObservation, err)
	}
}
