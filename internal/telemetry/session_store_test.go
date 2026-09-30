//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionSnapshotStoreReplacementAndFatalPreservation(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1000) }
	user := "alice"
	domain := "CONTOSO"
	working := sessiondata.DecimalUint64(42)
	snapshot := sessiondata.SessionSnapshot{Schema: sessiondata.SnapshotSchema, Host: "host.example.test", AgentInstanceID: "0195a584-5b25-7a00-91a5-7cbb4dac9201", Sequence: 2, ObservedAtMS: 900, CollectorVersion: "test", LogicalCPUCount: 1, Sessions: []sessiondata.SessionRecord{{SessionID: 1, User: &user, Domain: &domain, State: sessiondata.SessionActive, WorkingSetBytes: &working}}}
	result, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{Identity: sessiondata.VisibilityFull, Client: sessiondata.VisibilityFull, Process: sessiondata.VisibilityFull})
	if err != nil || !result.Accepted {
		t.Fatalf("success result=%+v err=%v", result, err)
	}
	fatal := snapshot
	fatal.Sequence = 3
	fatal.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	fatal.Sessions = nil
	if _, err := store.Apply(context.Background(), fatal, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	var count int
	var errorCode string
	if err := db.reader.QueryRow(`SELECT session_count, latest_attempt_error_code FROM session_snapshots WHERE canonical_host=?`, snapshot.Host).Scan(&count, &errorCode); err != nil {
		t.Fatal(err)
	}
	if count != 1 || errorCode != sessiondata.CollectionErrorWTSEnumerationFailed {
		t.Fatalf("fatal mutated success: count=%d code=%q", count, errorCode)
	}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_latest WHERE canonical_host=?`, snapshot.Host).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stale request replaced rows: %d", count)
	}
}

func TestSessionSnapshotStoreEmptyClearsAndRetentionBounds(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1000) }
	snapshot := sessiondata.SessionSnapshot{Schema: sessiondata.SnapshotSchema, Host: "empty.example.test", AgentInstanceID: "0195a584-5b25-7a00-91a5-7cbb4dac9201", Sequence: 1, ObservedAtMS: 1, CollectorVersion: "test", LogicalCPUCount: 1, Sessions: []sessiondata.SessionRecord{{SessionID: 1, State: sessiondata.SessionActive}}}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence = 2
	snapshot.Sessions = []sessiondata.SessionRecord{}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	var sessions, rows int
	if err := db.reader.QueryRow(`SELECT session_count FROM session_snapshots WHERE canonical_host=?`, snapshot.Host).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_latest WHERE canonical_host=?`, snapshot.Host).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || rows != 0 {
		t.Fatalf("empty snapshot did not clear: sessions=%d rows=%d", sessions, rows)
	}
	if _, err := store.PurgeSnapshots(context.Background(), 0, time.Now()); err == nil {
		t.Fatal("accepted invalid retention")
	}
}

func TestSessionSnapshotStorePrivacySafeUserCounts(t *testing.T) {
	alice, bob, domain := "alice", "bob", "CONTOSO"
	sessions := []sessiondata.SessionRecord{
		{SessionID: 1, User: &alice, Domain: &domain, State: sessiondata.SessionActive},
		{SessionID: 2, User: &bob, Domain: &domain, State: sessiondata.SessionIdle},
		{SessionID: 3, User: &alice, Domain: &domain, State: sessiondata.SessionDisconnected},
	}

	for _, test := range []struct {
		name      string
		identity  sessiondata.Visibility
		wantUsers int
	}{
		{name: "full", identity: sessiondata.VisibilityFull, wantUsers: 2},
		{name: "masked", identity: sessiondata.VisibilityMasked, wantUsers: 2},
		{name: "hidden", identity: sessiondata.VisibilityHidden, wantUsers: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openTestDB(t)
			store, err := NewSessionSnapshotStore(db)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := sessiondata.SessionSnapshot{
				Schema:           sessiondata.SnapshotSchema,
				Host:             test.name + ".example.test",
				AgentInstanceID:  "0195a584-5b25-7a00-91a5-7cbb4dac9201",
				Sequence:         1,
				ObservedAtMS:     1,
				CollectorVersion: "test",
				LogicalCPUCount:  1,
				Sessions:         sessions,
			}
			result, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{Identity: test.identity})
			if err != nil {
				t.Fatal(err)
			}
			if result.Aggregates == nil || result.Aggregates.UserCount != test.wantUsers {
				t.Fatalf("returned UserCount=%v, want %d", result.Aggregates, test.wantUsers)
			}

			var storedUsers int
			if err := db.reader.QueryRow(`SELECT user_count FROM session_snapshots WHERE canonical_host=?`, snapshot.Host).Scan(&storedUsers); err != nil {
				t.Fatal(err)
			}
			if storedUsers != test.wantUsers {
				t.Fatalf("stored UserCount=%d, want %d", storedUsers, test.wantUsers)
			}

			rows, err := db.reader.Query(`SELECT user_name, domain_name FROM session_latest WHERE canonical_host=? ORDER BY session_id`, snapshot.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := rows.Close(); err != nil {
					t.Errorf("close session rows: %v", err)
				}
			}()
			rowCount := 0
			for rows.Next() {
				rowCount++
				var user, storedDomain sql.NullString
				if err := rows.Scan(&user, &storedDomain); err != nil {
					t.Fatal(err)
				}
				switch test.identity {
				case sessiondata.VisibilityFull:
					if !user.Valid || !storedDomain.Valid || (user.String != alice && user.String != bob) || storedDomain.String != domain {
						t.Fatalf("full identity projection user=%#v domain=%#v", user, storedDomain)
					}
				case sessiondata.VisibilityMasked:
					if !user.Valid || !storedDomain.Valid || user.String != "***" || storedDomain.String != "***" {
						t.Fatalf("masked identity projection user=%#v domain=%#v", user, storedDomain)
					}
				case sessiondata.VisibilityHidden:
					if user.Valid || storedDomain.Valid {
						t.Fatalf("hidden identity projection user=%#v domain=%#v", user, storedDomain)
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if rowCount != len(sessions) {
				t.Fatalf("session rows=%d, want %d", rowCount, len(sessions))
			}
		})
	}
}

func TestSessionSnapshotStoreGenerationFenceRejectsDelayedGeneration(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	old := sessionSnapshotForGeneration("fence.example.test", "0195a584-5b25-7000-8000-000000000001", 99, 1)
	if result, err := store.Apply(context.Background(), old, sessiondata.PrivacyPolicy{}); err != nil || !result.Accepted {
		t.Fatalf("apply old result=%+v err=%v", result, err)
	}
	newer := sessionSnapshotForGeneration(old.Host, "0195a584-5b26-7000-8000-000000000001", 1, 2)
	if result, err := store.Apply(context.Background(), newer, sessiondata.PrivacyPolicy{}); err != nil || !result.Accepted {
		t.Fatalf("apply newer result=%+v err=%v", result, err)
	}
	old.CollectionError = &sessiondata.CollectionError{Code: sessiondata.CollectionErrorWTSEnumerationFailed}
	old.Sessions = nil
	result, err := store.Apply(context.Background(), old, sessiondata.PrivacyPolicy{})
	if err != nil || result.Accepted || result.Fatal {
		t.Fatalf("delayed old generation result=%+v err=%v, want stale no-SSE result", result, err)
	}
	var instance string
	var sessionID int
	if err := db.reader.QueryRow(`SELECT latest_attempt_instance_id FROM session_snapshots WHERE canonical_host=?`, old.Host).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	if err := db.reader.QueryRow(`SELECT session_id FROM session_latest WHERE canonical_host=?`, old.Host).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if instance != newer.AgentInstanceID || sessionID != 2 {
		t.Fatalf("delayed generation mutated current data: instance=%q session=%d", instance, sessionID)
	}
}

func TestSessionGenerationFenceRetainedAcrossPurgeAndRollback(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first := sessionSnapshotForGeneration("retained-fence.example.test", "0195a584-5b25-7000-8000-000000000001", 1, 1)
	if result, err := store.Apply(context.Background(), first, sessiondata.PrivacyPolicy{}); err != nil || !result.Accepted {
		t.Fatalf("apply first result=%+v err=%v", result, err)
	}
	if err := store.PurgeForPrivacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, err = NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := db.reader.QueryRow(`SELECT max_instance_id FROM session_generation_fence WHERE canonical_host=?`, first.Host).Scan(&before); err != nil {
		t.Fatalf("privacy purge or reopen removed fence: %v", err)
	}
	if _, err := db.writer.Exec(`CREATE TRIGGER fail_generation_snapshot BEFORE INSERT ON session_snapshots WHEN NEW.canonical_host = 'retained-fence.example.test' BEGIN SELECT RAISE(FAIL, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	newer := sessionSnapshotForGeneration(first.Host, "0195a584-5b26-7000-8000-000000000001", 1, 2)
	if _, err := store.Apply(context.Background(), newer, sessiondata.PrivacyPolicy{}); err == nil {
		t.Fatal("apply succeeded despite injected failure")
	}
	var after []byte
	if err := db.reader.QueryRow(`SELECT max_instance_id FROM session_generation_fence WHERE canonical_host=?`, first.Host).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed apply advanced fence: before=%x after=%x", before, after)
	}
}

func TestReserveSessionInstanceIDPersistsAcrossRestartAndClockRegression(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := ReserveSessionInstanceID(ctx, db, "reserved.example.test", time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	second, err := ReserveSessionInstanceID(ctx, db, "reserved.example.test", time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	third, err := ReserveSessionInstanceID(ctx, db, "reserved.example.test", time.UnixMilli(1999))
	if err != nil {
		t.Fatal(err)
	}
	firstID, _ := sessiondata.ParseUUIDv7(first)
	secondID, _ := sessiondata.ParseUUIDv7(second)
	thirdID, _ := sessiondata.ParseUUIDv7(third)
	if sessiondata.CompareUUIDv7(firstID, secondID) >= 0 || sessiondata.CompareUUIDv7(secondID, thirdID) >= 0 {
		t.Fatalf("reservations are not strictly increasing: %s, %s, %s", first, second, third)
	}
}

func sessionSnapshotForGeneration(host, instanceID string, sequence uint64, sessionID uint32) sessiondata.SessionSnapshot {
	return sessiondata.SessionSnapshot{Schema: sessiondata.SnapshotSchema, Host: host, AgentInstanceID: instanceID, Sequence: sessiondata.DecimalUint64(sequence), ObservedAtMS: 1, CollectorVersion: "test", LogicalCPUCount: 1, Sessions: []sessiondata.SessionRecord{{SessionID: sessionID, State: sessiondata.SessionActive}}}
}
