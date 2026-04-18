//go:build windows

package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// openMigrateTestDB opens a telemetry DB on a fresh temp dir and returns both
// so each test can control the audit.jsonl contents alongside the DB.
// Mirrors openTestDB but surfaces the dir, which MigrateJSONL needs as input.
func openMigrateTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

// writeJSONL serialises legacyAuditRecord values to dataDir/audit.jsonl,
// one record per line, matching the pre-007 on-disk format.
func writeJSONL(t *testing.T, dataDir string, records []legacyAuditRecord) string {
	t.Helper()
	path := filepath.Join(dataDir, auditJSONLFilename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create jsonl: %v", err)
	}
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			t.Fatalf("encode: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close jsonl: %v", err)
	}
	return path
}

// writeJSONLRaw writes arbitrary lines verbatim so tests can inject malformed
// JSON without having to go through encoding/json.
func writeJSONLRaw(t *testing.T, dataDir string, lines []string) string {
	t.Helper()
	path := filepath.Join(dataDir, auditJSONLFilename)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func countAuditRowsGlobal(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.reader.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

func TestMigrate_NoJSONLWritesMarker(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	res, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("MigrateJSONL: %v", err)
	}
	if res.JSONLFound {
		t.Errorf("JSONLFound = true, want false")
	}
	if res.Imported != 0 || res.LineCount != 0 || res.BackupPath != "" {
		t.Errorf("unexpected result on absent jsonl: %+v", res)
	}

	migrated, err := readSchemaMeta(ctx, db.writer, "jsonl_migrated")
	if err != nil {
		t.Fatalf("readSchemaMeta: %v", err)
	}
	if migrated != "true" {
		t.Errorf("jsonl_migrated = %q, want \"true\"", migrated)
	}

	var outcome, reason string
	var rowsAffected int64
	if err := db.reader.QueryRow(
		`SELECT outcome, reason, rows_affected FROM maintenance_jobs WHERE name = ?`,
		jsonlMigrationJobName,
	).Scan(&outcome, &reason, &rowsAffected); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "skipped" {
		t.Errorf("outcome = %q, want \"skipped\"", outcome)
	}
	if rowsAffected != 0 {
		t.Errorf("rows_affected = %d, want 0", rowsAffected)
	}
	if !strings.Contains(reason, "no audit.jsonl") {
		t.Errorf("reason = %q, want mention of missing file", reason)
	}
}

func TestMigrate_ValidJSONLAllImported(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Hour)
	km := base.Add(-time.Minute)
	records := []legacyAuditRecord{
		// Host A: baseline observation (Changed=false) then a transition to 1.
		{Timestamp: base, Host: "SRV-A", DrainMode: 0, KeyModified: km},
		{Timestamp: base.Add(10 * time.Minute), Host: "SRV-A", DrainMode: 1,
			Changed: true, ChangedBy: "alice", Reason: "drain", KeyModified: km},
		// Host B: single transition from implicit 0 to 2.
		{Timestamp: base.Add(5 * time.Minute), Host: "SRV-B", DrainMode: 2,
			Changed: true, ChangedBy: "bob", Reason: "node upgrade"},
		// Host A: reconciliation marker.
		{Timestamp: base.Add(20 * time.Minute), Host: "SRV-A", DrainMode: 0,
			Changed: true, Reconciliation: true, Reason: "drift"},
	}
	writeJSONL(t, dir, records)

	res, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("MigrateJSONL: %v", err)
	}
	if !res.JSONLFound {
		t.Error("JSONLFound = false, want true")
	}
	if res.LineCount != 4 {
		t.Errorf("LineCount = %d, want 4", res.LineCount)
	}
	if res.Imported != 3 {
		t.Errorf("Imported = %d, want 3 (only Changed records)", res.Imported)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", res.Skipped)
	}
	if res.BackupPath == "" {
		t.Error("BackupPath empty, want <path>.bak.<ts>")
	}

	if got := countAuditRowsGlobal(t, db); got != 3 {
		t.Fatalf("audit rows = %d, want 3", got)
	}

	store, err := NewAuditStore(ctx, db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	aRecs, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV-A"})
	if err != nil {
		t.Fatalf("QueryRange SRV-A: %v", err)
	}
	if len(aRecs) != 2 {
		t.Fatalf("SRV-A rows = %d, want 2", len(aRecs))
	}
	// Records come back DESC by ts; aRecs[0] is the reconciliation row.
	if !aRecs[0].Reconciliation || aRecs[0].PrevState != 1 || aRecs[0].NewState != 0 {
		t.Errorf("SRV-A recon row wrong: %+v", aRecs[0])
	}
	if aRecs[1].Reconciliation || aRecs[1].PrevState != 0 || aRecs[1].NewState != 1 ||
		aRecs[1].ChangedBy != "alice" || aRecs[1].Reason != "drain" {
		t.Errorf("SRV-A transition row wrong: %+v", aRecs[1])
	}
	if aRecs[1].KeyModifiedTs == nil || !aRecs[1].KeyModifiedTs.Equal(km) {
		t.Errorf("SRV-A transition KeyModifiedTs = %v, want %v", aRecs[1].KeyModifiedTs, km)
	}

	bRecs, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV-B"})
	if err != nil {
		t.Fatalf("QueryRange SRV-B: %v", err)
	}
	if len(bRecs) != 1 || bRecs[0].PrevState != 0 || bRecs[0].NewState != 2 ||
		bRecs[0].ChangedBy != "bob" {
		t.Fatalf("SRV-B rows wrong: %+v", bRecs)
	}

	migrated, err := readSchemaMeta(ctx, db.writer, "jsonl_migrated")
	if err != nil {
		t.Fatalf("readSchemaMeta: %v", err)
	}
	if migrated != "true" {
		t.Errorf("jsonl_migrated = %q, want \"true\"", migrated)
	}
}

func TestMigrate_MalformedLineSkipped(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	good, err := json.Marshal(legacyAuditRecord{
		Timestamp: ts, Host: "SRV01", DrainMode: 1, Changed: true, ChangedBy: "alice",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeJSONLRaw(t, dir, []string{
		string(good),
		"",                                   // empty line — skipped silently by the length guard
		"this is not json at all",            // malformed — counted as Skipped
		`{"host":"SRV02","ts":"not-a-time"}`, // malformed per time.Time parser
	})

	res, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("MigrateJSONL: %v", err)
	}
	if res.LineCount != 4 {
		t.Errorf("LineCount = %d, want 4", res.LineCount)
	}
	if res.Imported != 1 {
		t.Errorf("Imported = %d, want 1", res.Imported)
	}
	if res.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (empty line doesn't count)", res.Skipped)
	}
	if got := countAuditRowsGlobal(t, db); got != 1 {
		t.Errorf("audit rows = %d, want 1", got)
	}

	// schema_meta[jsonl_migrate_skipped] must survive for cross-invocation totalling.
	stored, err := readSchemaMetaInt(ctx, db.writer, "jsonl_migrate_skipped")
	if err != nil {
		t.Fatalf("readSchemaMetaInt: %v", err)
	}
	if stored != 2 {
		t.Errorf("jsonl_migrate_skipped = %d, want 2", stored)
	}

	var reason string
	if err := db.reader.QueryRow(
		`SELECT reason FROM maintenance_jobs WHERE name = ?`, jsonlMigrationJobName,
	).Scan(&reason); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if !strings.Contains(reason, "skipped 2") {
		t.Errorf("reason = %q, want mention of skipped=2", reason)
	}
}

func TestMigrate_DuplicateRecordsIgnored(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	rec := legacyAuditRecord{
		Timestamp: ts, Host: "SRV01", DrainMode: 1,
		Changed: true, ChangedBy: "alice", Reason: "first",
	}
	dup := rec
	dup.ChangedBy = "bob"
	dup.Reason = "second"
	writeJSONL(t, dir, []legacyAuditRecord{rec, dup})

	res, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("MigrateJSONL: %v", err)
	}
	// Both rows submitted to INSERT; one is dropped by ON CONFLICT DO NOTHING.
	if res.Imported != 2 {
		t.Errorf("Imported = %d, want 2 (submission count, not insert count)", res.Imported)
	}
	if got := countAuditRowsGlobal(t, db); got != 1 {
		t.Errorf("audit rows = %d, want 1 (PK collision dropped)", got)
	}

	store, err := NewAuditStore(ctx, db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	records, _, err := store.QueryRange(ctx, QueryFilter{Host: "SRV01"})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 1 || records[0].ChangedBy != "alice" || records[0].Reason != "first" {
		t.Errorf("conflict overwrote first row: %+v", records)
	}
}

func TestMigrate_RerunIsNoop(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	writeJSONL(t, dir, []legacyAuditRecord{
		{Timestamp: ts, Host: "SRV01", DrainMode: 1, Changed: true, ChangedBy: "alice"},
	})

	firstRes, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("first MigrateJSONL: %v", err)
	}
	if firstRes.Imported != 1 || firstRes.BackupPath == "" {
		t.Fatalf("first run result wrong: %+v", firstRes)
	}
	rowsBefore := countAuditRowsGlobal(t, db)

	var firstStarted, firstFinished int64
	if err := db.reader.QueryRow(
		`SELECT started_ts, finished_ts FROM maintenance_jobs WHERE name = ?`,
		jsonlMigrationJobName,
	).Scan(&firstStarted, &firstFinished); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}

	secondRes, err := MigrateJSONL(ctx, db, dir)
	if err != nil {
		t.Fatalf("second MigrateJSONL: %v", err)
	}
	if secondRes.JSONLFound || secondRes.Imported != 0 || secondRes.LineCount != 0 ||
		secondRes.BackupPath != "" {
		t.Errorf("rerun result non-zero: %+v", secondRes)
	}

	if got := countAuditRowsGlobal(t, db); got != rowsBefore {
		t.Errorf("rerun changed audit rows: %d -> %d", rowsBefore, got)
	}

	// Existing maintenance_jobs row must be preserved verbatim — the early-exit
	// branch short-circuits before recording a new run.
	var started2, finished2 int64
	if err := db.reader.QueryRow(
		`SELECT started_ts, finished_ts FROM maintenance_jobs WHERE name = ?`,
		jsonlMigrationJobName,
	).Scan(&started2, &finished2); err != nil {
		t.Fatalf("read maintenance_jobs after rerun: %v", err)
	}
	if started2 != firstStarted || finished2 != firstFinished {
		t.Errorf("rerun rewrote maintenance_jobs row: started %d->%d finished %d->%d",
			firstStarted, started2, firstFinished, finished2)
	}
}

func TestMigrate_PartialFailureLeavesJSONL(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	jsonlPath := writeJSONL(t, dir, []legacyAuditRecord{
		{Timestamp: ts, Host: "SRV01", DrainMode: 1, Changed: true, ChangedBy: "alice"},
	})

	// Hold audit.jsonl with dwShareMode=0 so MigrateJSONL's os.Open hits
	// ERROR_SHARING_VIOLATION — same observable failure mode as a permission
	// error during import, exercising the "error before rename" path.
	jp, err := windows.UTF16PtrFromString(jsonlPath)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(
		jp, windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0,
	)
	if err != nil {
		t.Fatalf("CreateFile exclusive on jsonl: %v", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	_, err = MigrateJSONL(ctx, db, dir)
	if err == nil {
		t.Fatal("MigrateJSONL succeeded with exclusive handle; want error")
	}

	if _, statErr := os.Stat(jsonlPath); statErr != nil {
		t.Errorf("audit.jsonl removed on failure: %v", statErr)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, auditJSONLFilename+".bak.*"))
	if len(matches) != 0 {
		t.Errorf("backup file created despite failure: %v", matches)
	}

	migrated, err := readSchemaMeta(ctx, db.writer, "jsonl_migrated")
	if err != nil {
		t.Fatalf("readSchemaMeta: %v", err)
	}
	if migrated == "true" {
		t.Error("jsonl_migrated = \"true\" after failure")
	}

	// maintenance_jobs row carries the failure so operators can see it.
	var outcome string
	if err := db.reader.QueryRow(
		`SELECT outcome FROM maintenance_jobs WHERE name = ?`, jsonlMigrationJobName,
	).Scan(&outcome); err != nil {
		t.Fatalf("read maintenance_jobs: %v", err)
	}
	if outcome != "failure" {
		t.Errorf("outcome = %q, want \"failure\"", outcome)
	}
}

func TestMigrate_RenameIncludesTimestamp(t *testing.T) {
	db, dir := openMigrateTestDB(t)
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	jsonlPath := writeJSONL(t, dir, []legacyAuditRecord{
		{Timestamp: ts, Host: "SRV01", DrainMode: 1, Changed: true, ChangedBy: "alice"},
	})

	before := time.Now().UTC()
	res, err := MigrateJSONL(ctx, db, dir)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("MigrateJSONL: %v", err)
	}

	if _, statErr := os.Stat(jsonlPath); !os.IsNotExist(statErr) {
		t.Errorf("audit.jsonl still present after success: %v", statErr)
	}
	if res.BackupPath == "" {
		t.Fatal("BackupPath empty")
	}
	if _, statErr := os.Stat(res.BackupPath); statErr != nil {
		t.Fatalf("backup file missing: %v", statErr)
	}

	want := jsonlPath + ".bak."
	if !strings.HasPrefix(res.BackupPath, want) {
		t.Fatalf("BackupPath = %q, want prefix %q", res.BackupPath, want)
	}
	stamp := strings.TrimPrefix(res.BackupPath, want)
	// UTC timestamp format is YYYYMMDDTHHMMSSZ — 16 chars, matches the layout
	// MigrateJSONL formats via time.Format("20060102T150405Z").
	if !regexp.MustCompile(`^\d{8}T\d{6}Z$`).MatchString(stamp) {
		t.Errorf("backup stamp %q does not match YYYYMMDDTHHMMSSZ", stamp)
	}
	parsed, parseErr := time.Parse("20060102T150405Z", stamp)
	if parseErr != nil {
		t.Fatalf("parse stamp %q: %v", stamp, parseErr)
	}
	// Windows filesystem timestamps and the stamp are both second-granularity;
	// allow a one-second slop on either side.
	lo := before.Truncate(time.Second).Add(-time.Second)
	hi := after.Add(time.Second)
	if parsed.Before(lo) || parsed.After(hi) {
		t.Errorf("stamp %v outside call window [%v, %v]", parsed, lo, hi)
	}
}
