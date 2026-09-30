//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionActionLedgerClaimRestartAndTerminal(t *testing.T) {
	db := openTestDB(t)
	ledger := NewSessionActionLedgerStore(db)
	now := time.UnixMilli(4000000)
	expiry := now.Add(5 * time.Minute)
	claimed, entry, err := ledger.Claim(context.Background(), "action", expiry, now)
	if err != nil || !claimed || entry.State != sessiondata.SessionActionLedgerClaimed {
		t.Fatalf("first claim: %v %+v %v", claimed, entry, err)
	}
	claimed, entry, err = ledger.Claim(context.Background(), "action", expiry, now.Add(time.Second))
	if err != nil || claimed || entry.State != sessiondata.SessionActionLedgerClaimed {
		t.Fatalf("restart claim: %v %+v %v", claimed, entry, err)
	}
	if err := ledger.Complete(context.Background(), "action", sessiondata.SessionActionOutcomeFailed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Complete(context.Background(), "action", sessiondata.SessionActionOutcomeCompleted, now.Add(2*time.Second)); err != nil {
		t.Fatalf("terminal retry: %v", err)
	}
	entry, err = ledger.Entry(context.Background(), "action")
	if err != nil || entry.State != sessiondata.SessionActionLedgerTerminal || entry.Outcome == nil || *entry.Outcome != sessiondata.SessionActionOutcomeFailed {
		t.Fatalf("terminal entry: %+v %v", entry, err)
	}
	if err := ledger.Acknowledge(context.Background(), "action"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Entry(context.Background(), "action"); err != sql.ErrNoRows {
		t.Fatalf("acknowledged entry error=%v", err)
	}
}

func TestSessionActionLedgerCleanupIsBounded(t *testing.T) {
	db := openTestDB(t)
	ledger := NewSessionActionLedgerStore(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := ledger.Claim(context.Background(), id, now.Add(-time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := ledger.Cleanup(context.Background(), now, time.Minute, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("first cleanup=%d %v", deleted, err)
	}
	var remaining int
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM session_action_ledger`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining=%d %v", remaining, err)
	}
	deleted, err = ledger.Cleanup(context.Background(), now, time.Minute, 2)
	if err != nil || deleted != 1 {
		t.Fatalf("second cleanup=%d %v", deleted, err)
	}
}
