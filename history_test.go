//go:build windows

package drainctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

// seedTelemetryAudit opens a telemetry store in a fresh temp dir, writes the
// supplied records, and returns a path inside that data dir. The returned
// path mimics the legacy audit.jsonl location — GetHistory derives the data
// dir from its parent.
func seedTelemetryAudit(t *testing.T, recs []telemetry.AuditRecord) string {
	t.Helper()
	dataDir := t.TempDir()
	db, err := telemetry.Open(dataDir)
	if err != nil {
		t.Fatalf("telemetry.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	store, err := telemetry.NewAuditStore(ctx, db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for i, r := range recs {
		if err := store.Append(ctx, r); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	return filepath.Join(dataDir, "audit.jsonl")
}

func TestGetHistory_ReturnsAllRecords(t *testing.T) {
	now := time.Now().UTC()
	dbPath := seedTelemetryAudit(t, []telemetry.AuditRecord{
		{Ts: now.Add(-2 * time.Hour), Host: "h", PrevState: 0, NewState: 1},
		{Ts: now.Add(-time.Hour), Host: "h", PrevState: 1, NewState: 0},
		{Ts: now, Host: "h", PrevState: 0, NewState: 1},
	})

	recs, err := GetHistory(HistoryOptions{DBPath: dbPath})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 3 {
		t.Errorf("len = %d, want 3", len(recs))
	}
}

func TestGetHistory_AcceptsFilePath(t *testing.T) {
	now := time.Now().UTC()
	dbPath := seedTelemetryAudit(t, []telemetry.AuditRecord{
		{Ts: now, Host: "h", PrevState: 0, NewState: 1},
	})

	recs, err := GetHistory(HistoryOptions{DBPath: filepath.Join(filepath.Dir(dbPath), "drainctl.db")})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
}

func TestGetHistory_AcceptsDirPath(t *testing.T) {
	now := time.Now().UTC()
	dbPath := seedTelemetryAudit(t, []telemetry.AuditRecord{
		{Ts: now, Host: "h", PrevState: 0, NewState: 1},
	})

	recs, err := GetHistory(HistoryOptions{DBPath: filepath.Dir(dbPath)})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
}

func TestGetHistory_ChangesOnly(t *testing.T) {
	now := time.Now().UTC()
	dbPath := seedTelemetryAudit(t, []telemetry.AuditRecord{
		{Ts: now.Add(-time.Hour), Host: "h", PrevState: 0, NewState: 1},
	})

	recs, err := GetHistory(HistoryOptions{DBPath: dbPath, ChangesOnly: true})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
	if !recs[0].Changed {
		t.Error("expected Changed=true on returned transition")
	}
}

func TestGetHistory_LimitRespected(t *testing.T) {
	base := time.Now().UTC().Add(-10 * time.Minute)
	var seeds []telemetry.AuditRecord
	for i := range 5 {
		seeds = append(seeds, telemetry.AuditRecord{
			Ts:        base.Add(time.Duration(i) * time.Second),
			Host:      "h",
			PrevState: i % 2,
			NewState:  (i + 1) % 2,
		})
	}
	dbPath := seedTelemetryAudit(t, seeds)

	recs, err := GetHistory(HistoryOptions{DBPath: dbPath, Limit: 2})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("len = %d, want 2 (limit applied)", len(recs))
	}
}

// TestGetHistory_UntilBoundaryIsInclusive verifies that a row whose timestamp
// equals Until is returned — the CLI advertises --until as "at or before",
// and the adapter must translate to the exclusive telemetry.QueryFilter.To.
func TestGetHistory_UntilBoundaryIsInclusive(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	dbPath := seedTelemetryAudit(t, []telemetry.AuditRecord{
		{Ts: now, Host: "h", PrevState: 0, NewState: 1},
	})

	until := now
	recs, err := GetHistory(HistoryOptions{DBPath: dbPath, Until: &until})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1 (row at Until boundary must be included)", len(recs))
	}
}

func TestGetHistory_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	badPath := filepath.Join(blockingFile, "audit.jsonl")

	_, err := GetHistory(HistoryOptions{DBPath: badPath})
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
	if !strings.Contains(err.Error(), "open audit store") {
		t.Errorf("error = %q, want 'open audit store' in message", err.Error())
	}
}
