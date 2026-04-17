//go:build windows

package drainctl

import (
	"os"
	"testing"
	"time"
)

// writeTestRecords writes a slice of AuditRecords to a temp file and returns
// an open AuditStore backed by that file. The caller is responsible for closing
// the store and removing the file.
func writeTestRecords(t *testing.T, records []AuditRecord) (*AuditStore, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "audit_filter_test_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()

	store, err := OpenAuditStore(path)
	if err != nil {
		_ = os.Remove(path)
		t.Fatalf("open audit store: %v", err)
	}
	for i := range records {
		if err := store.Record(&records[i]); err != nil {
			_ = os.Remove(path)
			t.Fatalf("write record %d: %v", i, err)
		}
	}
	return store, func() { _ = os.Remove(path) }
}

func TestHistoryFiltered(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	t1 := base                    // 10:00
	t2 := base.Add(1 * time.Hour) // 11:00
	t3 := base.Add(2 * time.Hour) // 12:00
	t4 := base.Add(3 * time.Hour) // 13:00
	t5 := base.Add(4 * time.Hour) // 14:00

	records := []AuditRecord{
		{Timestamp: t1, Host: "h1", DrainMode: 0},
		{Timestamp: t2, Host: "h1", DrainMode: 1},
		{Timestamp: t3, Host: "h1", DrainMode: 0},
		{Timestamp: t4, Host: "h1", DrainMode: 1},
		{Timestamp: t5, Host: "h1", DrainMode: 0},
	}

	t.Run("no bounds same as History", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		got, err := store.HistoryFiltered(0, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, err := store.History(0)
		if err != nil {
			t.Fatalf("unexpected error from History: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d records, want %d", len(got), len(want))
		}
		for i := range got {
			if !got[i].Timestamp.Equal(want[i].Timestamp) {
				t.Errorf("record[%d] timestamp mismatch: got %v, want %v", i, got[i].Timestamp, want[i].Timestamp)
			}
		}
	})

	t.Run("since only filters old records", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		since := t3
		got, err := store.HistoryFiltered(0, &since, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should return t3, t4, t5 (newest first: t5, t4, t3)
		if len(got) != 3 {
			t.Fatalf("got %d records, want 3", len(got))
		}
		if !got[0].Timestamp.Equal(t5) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t5)
		}
		if !got[2].Timestamp.Equal(t3) {
			t.Errorf("got[2] = %v, want %v", got[2].Timestamp, t3)
		}
	})

	t.Run("until only filters new records", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		until := t3
		got, err := store.HistoryFiltered(0, nil, &until)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should return t1, t2, t3 (newest first: t3, t2, t1)
		if len(got) != 3 {
			t.Fatalf("got %d records, want 3", len(got))
		}
		if !got[0].Timestamp.Equal(t3) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t3)
		}
		if !got[2].Timestamp.Equal(t1) {
			t.Errorf("got[2] = %v, want %v", got[2].Timestamp, t1)
		}
	})

	t.Run("both since and until returns window", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		since := t2
		until := t4
		got, err := store.HistoryFiltered(0, &since, &until)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should return t2, t3, t4 (newest first: t4, t3, t2)
		if len(got) != 3 {
			t.Fatalf("got %d records, want 3", len(got))
		}
		if !got[0].Timestamp.Equal(t4) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t4)
		}
		if !got[2].Timestamp.Equal(t2) {
			t.Errorf("got[2] = %v, want %v", got[2].Timestamp, t2)
		}
	})

	t.Run("window matches nothing returns empty", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		future := base.Add(10 * time.Hour)
		got, err := store.HistoryFiltered(0, &future, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d records, want 0", len(got))
		}
	})

	t.Run("limit applied after filtering", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		since := t2
		got, err := store.HistoryFiltered(2, &since, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// t2..t5 pass the filter (4 records), limit=2 → newest 2: t5, t4
		if len(got) != 2 {
			t.Fatalf("got %d records, want 2", len(got))
		}
		if !got[0].Timestamp.Equal(t5) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t5)
		}
		if !got[1].Timestamp.Equal(t4) {
			t.Errorf("got[1] = %v, want %v", got[1].Timestamp, t4)
		}
	})
}

func TestChangesFiltered(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(1 * time.Hour)
	t3 := base.Add(2 * time.Hour)
	t4 := base.Add(3 * time.Hour)
	t5 := base.Add(4 * time.Hour)

	records := []AuditRecord{
		{Timestamp: t1, Host: "h1", DrainMode: 0, Changed: false},
		{Timestamp: t2, Host: "h1", DrainMode: 1, Changed: true},
		{Timestamp: t3, Host: "h1", DrainMode: 1, Changed: false},
		{Timestamp: t4, Host: "h1", DrainMode: 0, Changed: true},
		{Timestamp: t5, Host: "h1", DrainMode: 0, Changed: false},
	}

	t.Run("only transition records returned", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		got, err := store.ChangesFiltered(0, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Only t2 and t4 have Changed=true (newest first: t4, t2)
		if len(got) != 2 {
			t.Fatalf("got %d records, want 2", len(got))
		}
		if !got[0].Timestamp.Equal(t4) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t4)
		}
		if !got[1].Timestamp.Equal(t2) {
			t.Errorf("got[1] = %v, want %v", got[1].Timestamp, t2)
		}
	})

	t.Run("no bounds same as Changes", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		got, err := store.ChangesFiltered(0, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, err := store.Changes(0)
		if err != nil {
			t.Fatalf("unexpected error from Changes: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d records, want %d", len(got), len(want))
		}
		for i := range got {
			if !got[i].Timestamp.Equal(want[i].Timestamp) {
				t.Errorf("record[%d] timestamp mismatch: got %v, want %v", i, got[i].Timestamp, want[i].Timestamp)
			}
		}
	})

	t.Run("since filters out early transitions", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		since := t3
		got, err := store.ChangesFiltered(0, &since, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// t2 is before since; t4 passes. Result: [t4]
		if len(got) != 1 {
			t.Fatalf("got %d records, want 1", len(got))
		}
		if !got[0].Timestamp.Equal(t4) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t4)
		}
	})

	t.Run("until filters out late transitions", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		until := t3
		got, err := store.ChangesFiltered(0, nil, &until)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// t4 is after until; t2 passes. Result: [t2]
		if len(got) != 1 {
			t.Fatalf("got %d records, want 1", len(got))
		}
		if !got[0].Timestamp.Equal(t2) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t2)
		}
	})

	t.Run("window excludes all transitions", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		// Window t3..t3 — only t3 but it's not a change record.
		got, err := store.ChangesFiltered(0, &t3, &t3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d records, want 0", len(got))
		}
	})

	t.Run("limit applied returns newest n transitions", func(t *testing.T) {
		store, cleanup := writeTestRecords(t, records)
		defer cleanup()

		// 2 total transitions (t2, t4); limit=1 → newest: t4
		got, err := store.ChangesFiltered(1, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d records, want 1", len(got))
		}
		if !got[0].Timestamp.Equal(t4) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t4)
		}
	})

	t.Run("limit with time filter", func(t *testing.T) {
		// Add extra change records to exercise ring eviction.
		extra := []AuditRecord{
			{Timestamp: t1, Host: "h1", DrainMode: 0, Changed: false},
			{Timestamp: t2, Host: "h1", DrainMode: 1, Changed: true},
			{Timestamp: t3, Host: "h1", DrainMode: 0, Changed: true},
			{Timestamp: t4, Host: "h1", DrainMode: 1, Changed: true},
			{Timestamp: t5, Host: "h1", DrainMode: 0, Changed: true},
		}
		store, cleanup := writeTestRecords(t, extra)
		defer cleanup()

		// 4 transitions (t2–t5 all Changed); limit=2, since=t2 → newest 2: t5, t4
		since := t2
		got, err := store.ChangesFiltered(2, &since, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d records, want 2", len(got))
		}
		if !got[0].Timestamp.Equal(t5) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t5)
		}
		if !got[1].Timestamp.Equal(t4) {
			t.Errorf("got[1] = %v, want %v", got[1].Timestamp, t4)
		}
	})

	t.Run("ring buffer evicts oldest when over limit", func(t *testing.T) {
		// 3 transitions but limit=2 — only the newest 2 should survive.
		extra := []AuditRecord{
			{Timestamp: t2, Host: "h1", DrainMode: 1, Changed: true},
			{Timestamp: t3, Host: "h1", DrainMode: 0, Changed: true},
			{Timestamp: t4, Host: "h1", DrainMode: 1, Changed: true},
		}
		store, cleanup := writeTestRecords(t, extra)
		defer cleanup()

		got, err := store.ChangesFiltered(2, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d records, want 2", len(got))
		}
		// Newest first: t4, t3
		if !got[0].Timestamp.Equal(t4) {
			t.Errorf("got[0] = %v, want %v", got[0].Timestamp, t4)
		}
		if !got[1].Timestamp.Equal(t3) {
			t.Errorf("got[1] = %v, want %v", got[1].Timestamp, t3)
		}
	})
}
