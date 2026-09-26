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
		"server_exclusions",
		"host_freshness",
		"force_update_outbox",
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

func TestApplySchema_IdempotentOnExistingV3(t *testing.T) {
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
