//go:build windows

package telemetry

import (
	"testing"
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

	if got := queryPragmaInt(t, db, "user_version"); got != 1 {
		t.Errorf("user_version = %d, want 1", got)
	}
}

func TestApplySchema_IdempotentOnExistingV1(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if got := queryPragmaInt(t, db1, "user_version"); got != 1 {
		t.Errorf("after first open: user_version = %d, want 1", got)
	}
	_ = db1.Close()

	// Second open — applySchema must not return an error on a v1 DB.
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open (idempotent): %v", err)
	}
	defer func() { _ = db2.Close() }()

	if got := queryPragmaInt(t, db2, "user_version"); got != 1 {
		t.Errorf("after second open: user_version = %d, want 1", got)
	}
}
