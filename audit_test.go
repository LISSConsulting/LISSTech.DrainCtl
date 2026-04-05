//go:build windows

package drainctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// writeTestRecords and ptr are defined in audit_filter_test.go.

// ── scanRecords ───────────────────────────────────────────────────────────────

// TestScanRecords_EarlyStop verifies that scanRecords stops iterating as soon
// as the callback returns false. This exercises the `if !fn(rec) { break }`
// branch which no production caller currently triggers (all callers always
// return true), but is required for correctness of the streaming contract.
func TestScanRecords_EarlyStop(t *testing.T) {
	records := []AuditRecord{
		{Timestamp: time.Now().Add(-2 * time.Second), Host: "srv1"},
		{Timestamp: time.Now().Add(-1 * time.Second), Host: "srv1"},
		{Timestamp: time.Now(), Host: "srv1"},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	var seen int
	err := store.scanRecords(func(_ AuditRecord) bool {
		seen++
		return false // stop after first record
	})
	if err != nil {
		t.Fatalf("scanRecords: %v", err)
	}
	if seen != 1 {
		t.Errorf("seen = %d, want 1 (early stop after first record)", seen)
	}
}

// ── LastObservation ───────────────────────────────────────────────────────────

func TestLastObservation_Empty(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_lo_empty_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	store, err := OpenAuditStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	rec, err := store.LastObservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil for empty store, got %+v", rec)
	}
}

func TestLastObservation_SingleRecord(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	store, cleanup := writeTestRecords(t, []AuditRecord{
		{Timestamp: base, Host: "srv1", DrainMode: AllowAll, DrainLabel: "AllowAll"},
	})
	defer cleanup()

	rec, err := store.LastObservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.Host != "srv1" {
		t.Errorf("Host = %q, want %q", rec.Host, "srv1")
	}
	if !rec.Timestamp.Equal(base) {
		t.Errorf("Timestamp = %v, want %v", rec.Timestamp, base)
	}
}

func TestLastObservation_ReturnsNewest(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, Host: "srv1", DrainMode: AllowAll},
		{Timestamp: base.Add(time.Hour), Host: "srv1", DrainMode: 1},
		{Timestamp: base.Add(2 * time.Hour), Host: "srv1", DrainMode: AllowAll},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	rec, err := store.LastObservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if !rec.Timestamp.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("Timestamp = %v, want %v", rec.Timestamp, base.Add(2*time.Hour))
	}
}

func TestLastObservation_NonexistentFile(t *testing.T) {
	store, err := OpenAuditStore("/nonexistent/path/audit.jsonl")
	if err != nil {
		t.Fatalf("OpenAuditStore: %v", err)
	}

	// scanRecords returns nil on os.IsNotExist, so LastObservation should return nil, nil.
	rec, err := store.LastObservation()
	if err != nil {
		t.Fatalf("unexpected error for nonexistent path: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil for nonexistent file, got %+v", rec)
	}
}

// ── History ───────────────────────────────────────────────────────────────────

func TestHistory_Empty(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_hist_empty_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	store, err := OpenAuditStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	recs, err := store.History(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 records, got %d", len(recs))
	}
}

func TestHistory_NewestFirst(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, Host: "srv1"},
		{Timestamp: base.Add(time.Hour), Host: "srv1"},
		{Timestamp: base.Add(2 * time.Hour), Host: "srv1"},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	// Newest first.
	if !recs[0].Timestamp.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("recs[0] = %v, want %v", recs[0].Timestamp, base.Add(2*time.Hour))
	}
	if !recs[2].Timestamp.Equal(base) {
		t.Errorf("recs[2] = %v, want %v", recs[2].Timestamp, base)
	}
}

func TestHistory_LimitedToN(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	var records []AuditRecord
	for i := 0; i < 10; i++ {
		records = append(records, AuditRecord{Timestamp: base.Add(time.Duration(i) * time.Hour), Host: "srv1"})
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.History(3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	// Should be the 3 newest records, newest first.
	if !recs[0].Timestamp.Equal(base.Add(9 * time.Hour)) {
		t.Errorf("recs[0] = %v, want newest", recs[0].Timestamp)
	}
	if !recs[2].Timestamp.Equal(base.Add(7 * time.Hour)) {
		t.Errorf("recs[2] = %v, want 3rd-newest", recs[2].Timestamp)
	}
}

func TestHistory_LimitZeroReturnsAll(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	var records []AuditRecord
	for i := 0; i < 5; i++ {
		records = append(records, AuditRecord{Timestamp: base.Add(time.Duration(i) * time.Hour)})
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 5 {
		t.Errorf("got %d records, want 5", len(recs))
	}
}

func TestHistory_LimitLargerThanTotal(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, Host: "srv1"},
		{Timestamp: base.Add(time.Hour), Host: "srv1"},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.History(100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2", len(recs))
	}
}

// ── Changes ───────────────────────────────────────────────────────────────────

func TestChanges_Empty(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, Host: "srv1", Changed: false},
		{Timestamp: base.Add(time.Hour), Host: "srv1", Changed: false},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.Changes(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("got %d changes, want 0", len(recs))
	}
}

func TestChanges_OnlyTransitions(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, Host: "srv1", Changed: false},
		{Timestamp: base.Add(time.Hour), Host: "srv1", Changed: true},
		{Timestamp: base.Add(2 * time.Hour), Host: "srv1", Changed: false},
		{Timestamp: base.Add(3 * time.Hour), Host: "srv1", Changed: true},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.Changes(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d changes, want 2", len(recs))
	}
	// Newest first.
	if !recs[0].Timestamp.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("recs[0] = %v, want newest transition", recs[0].Timestamp)
	}
	if !recs[1].Timestamp.Equal(base.Add(time.Hour)) {
		t.Errorf("recs[1] = %v, want oldest transition", recs[1].Timestamp)
	}
}

func TestChanges_LimitedToN(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	var records []AuditRecord
	for i := 0; i < 6; i++ {
		records = append(records, AuditRecord{
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			Changed:   i%2 == 1, // transitions at hours 1, 3, 5
		})
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	recs, err := store.Changes(2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	// Newest 2 transitions are at hours 5 and 3.
	if !recs[0].Timestamp.Equal(base.Add(5 * time.Hour)) {
		t.Errorf("recs[0] = %v, want hour 5", recs[0].Timestamp)
	}
	if !recs[1].Timestamp.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("recs[1] = %v, want hour 3", recs[1].Timestamp)
	}
}

// ── StateSince ────────────────────────────────────────────────────────────────

func TestStateSince_EmptyStore(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_ss_empty_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	store, err := OpenAuditStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	since, err := store.StateSince(AllowAll)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if since != nil {
		t.Errorf("expected nil for empty store, got %v", since)
	}
}

func TestStateSince_AllSameMode(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	records := []AuditRecord{
		{Timestamp: base, DrainMode: AllowAll},
		{Timestamp: base.Add(time.Hour), DrainMode: AllowAll},
		{Timestamp: base.Add(2 * time.Hour), DrainMode: AllowAll},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	since, err := store.StateSince(AllowAll)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if since == nil {
		t.Fatal("expected non-nil since, got nil")
	}
	// All records have the same mode → oldest record's timestamp.
	if !since.Equal(base) {
		t.Errorf("StateSince = %v, want %v (oldest record)", since, base)
	}
}

func TestStateSince_AfterTransition(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(time.Hour)
	t3 := base.Add(2 * time.Hour)
	t4 := base.Add(3 * time.Hour)

	// Modes: AllowAll, BlockAll, BlockAll, BlockAll
	// Current mode is BlockAll; it started at t2.
	records := []AuditRecord{
		{Timestamp: t1, DrainMode: AllowAll},
		{Timestamp: t2, DrainMode: 1}, // transition into BlockAll
		{Timestamp: t3, DrainMode: 1},
		{Timestamp: t4, DrainMode: 1},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	since, err := store.StateSince(DrainMode(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if since == nil {
		t.Fatal("expected non-nil since, got nil")
	}
	if !since.Equal(t2) {
		t.Errorf("StateSince(BlockAll) = %v, want %v", since, t2)
	}
}

func TestStateSince_ModeNotCurrentlyActive(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	// All records in mode AllowAll; asking for mode 1 (never present).
	records := []AuditRecord{
		{Timestamp: base, DrainMode: AllowAll},
		{Timestamp: base.Add(time.Hour), DrainMode: AllowAll},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	since, err := store.StateSince(DrainMode(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The requested mode has never been active — no record with a different
	// mode precedes a record with mode 1, so StateSince returns nil.
	if since != nil {
		t.Errorf("StateSince for absent mode = %v, want nil", since)
	}
}

func TestStateSince_ReturnsToCurrent(t *testing.T) {
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(time.Hour)
	t3 := base.Add(2 * time.Hour)
	t4 := base.Add(3 * time.Hour)
	t5 := base.Add(4 * time.Hour)

	// AllowAll → BlockAll → AllowAll → BlockAll → AllowAll
	// Current mode (AllowAll) started at t5.
	records := []AuditRecord{
		{Timestamp: t1, DrainMode: AllowAll},
		{Timestamp: t2, DrainMode: 1},
		{Timestamp: t3, DrainMode: AllowAll},
		{Timestamp: t4, DrainMode: 1},
		{Timestamp: t5, DrainMode: AllowAll},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	since, err := store.StateSince(AllowAll)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if since == nil {
		t.Fatal("expected non-nil since, got nil")
	}
	if !since.Equal(t5) {
		t.Errorf("StateSince(AllowAll) = %v, want %v (last transition into AllowAll)", since, t5)
	}
}

// ── Prune ─────────────────────────────────────────────────────────────────────

func TestPrune_RemovesOldRecords(t *testing.T) {
	now := time.Now().UTC()
	retention := 7 * 24 * time.Hour

	records := []AuditRecord{
		{Timestamp: now.Add(-14 * 24 * time.Hour), Host: "srv1"}, // 14 days old — pruned
		{Timestamp: now.Add(-10 * 24 * time.Hour), Host: "srv1"}, // 10 days old — pruned
		{Timestamp: now.Add(-3 * 24 * time.Hour), Host: "srv1"},  // 3 days old — kept
		{Timestamp: now.Add(-1 * 24 * time.Hour), Host: "srv1"},  // 1 day old — kept
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	pruned, err := store.Prune(retention)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pruned != 2 {
		t.Errorf("Prune returned %d, want 2", pruned)
	}

	// Verify only 2 records remain.
	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error reading after prune: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("after Prune: %d records remain, want 2", len(recs))
	}
}

func TestPrune_KeepsAllRecentRecords(t *testing.T) {
	now := time.Now().UTC()
	retention := 7 * 24 * time.Hour

	records := []AuditRecord{
		{Timestamp: now.Add(-3 * 24 * time.Hour), Host: "srv1"},
		{Timestamp: now.Add(-1 * 24 * time.Hour), Host: "srv1"},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	pruned, err := store.Prune(retention)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pruned != 0 {
		t.Errorf("Prune returned %d, want 0 (nothing to prune)", pruned)
	}

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error reading after prune: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("after Prune: %d records remain, want 2", len(recs))
	}
}

func TestPrune_EmptyStore(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_prune_empty_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	store, err := OpenAuditStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	pruned, err := store.Prune(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pruned != 0 {
		t.Errorf("Prune on empty store = %d, want 0", pruned)
	}
}

func TestPrune_AllRecordsPruned(t *testing.T) {
	now := time.Now().UTC()
	records := []AuditRecord{
		{Timestamp: now.Add(-30 * 24 * time.Hour), Host: "srv1"},
		{Timestamp: now.Add(-20 * 24 * time.Hour), Host: "srv1"},
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	pruned, err := store.Prune(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pruned != 2 {
		t.Errorf("Prune returned %d, want 2", pruned)
	}

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error reading after full prune: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("after full Prune: %d records remain, want 0", len(recs))
	}
}

// ── scanRecords — malformed line handling ─────────────────────────────────────

// TestAuditStore_Close verifies that Close returns nil and can be called
// on any open store (it is a no-op satisfying the interface contract).
func TestAuditStore_Close(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAuditStore(dir + `\audit.jsonl`)
	if err != nil {
		t.Fatalf("OpenAuditStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestScanRecords_SkipsMalformedLines(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_malformed_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	// Write a mix of valid JSONL lines and garbage.
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	good := AuditRecord{Timestamp: base, Host: "srv1", DrainMode: AllowAll}
	good2 := AuditRecord{Timestamp: base.Add(time.Hour), Host: "srv1", DrainMode: 1}

	store := &AuditStore{path: path}
	_ = store.Record(&good)
	// Inject a malformed line directly.
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = fh.WriteString("THIS IS NOT JSON\n")
	_ = fh.Close()
	_ = store.Record(&good2)

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the 2 valid records should be returned.
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2 (malformed line should be skipped)", len(recs))
	}
}

// ── OpenAuditStore error path ─────────────────────────────────────────────────

// TestOpenAuditStore_MkdirAllError verifies that OpenAuditStore returns an error
// when the parent directory cannot be created (here: a regular file exists at the
// directory path, blocking os.MkdirAll).
func TestOpenAuditStore_MkdirAllError(t *testing.T) {
	parent := t.TempDir()
	// Create a regular file where the audit directory should be.
	blocker := filepath.Join(parent, "datadir")
	if err := os.WriteFile(blocker, []byte{}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// The audit path is inside "blocker" (a file, not a dir) — MkdirAll should fail.
	_, err := OpenAuditStore(filepath.Join(blocker, "audit.jsonl"))
	if err == nil {
		t.Fatal("expected error from OpenAuditStore when parent cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "create audit directory") {
		t.Errorf("error = %q, want 'create audit directory' in message", err)
	}
}

// ── Record error path ─────────────────────────────────────────────────────────

// TestRecord_OpenFileError verifies that Record returns an error when the audit
// file path points to a directory (os.OpenFile for writing fails on a directory).
func TestRecord_OpenFileError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the audit file path so os.OpenFile fails.
	auditPath := filepath.Join(dir, "audit.jsonl")
	if err := os.Mkdir(auditPath, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	store := &AuditStore{path: auditPath}
	rec := &AuditRecord{Timestamp: time.Now(), Host: "srv1"}
	err := store.Record(rec)
	if err == nil {
		t.Fatal("expected error from Record when path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "open audit file") {
		t.Errorf("error = %q, want 'open audit file' in message", err)
	}
}

// ── scanRecords empty-line handling ──────────────────────────────────────────

// TestScanRecords_EmptyLineSkipped verifies that scanRecords skips blank lines
// (len(line)==0) without treating them as malformed records.
func TestScanRecords_EmptyLineSkipped(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_emptyline_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	_ = f.Close()

	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	good := AuditRecord{Timestamp: base, Host: "srv1", DrainMode: AllowAll}
	good2 := AuditRecord{Timestamp: base.Add(time.Hour), Host: "srv1", DrainMode: 1}

	store := &AuditStore{path: path}
	_ = store.Record(&good)
	// Inject a blank line between the two valid records.
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = fh.WriteString("\n")
	_ = fh.Close()
	_ = store.Record(&good2)

	recs, err := store.History(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2 (blank line should be skipped)", len(recs))
	}
}

// ── scanRecords / History error propagation ───────────────────────────────────

// TestHistory_ScanError verifies that History propagates a scanner error when a
// line in the JSONL file exceeds the 64 KiB scanner buffer limit.
func TestHistory_ScanError(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_longerr_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	store := &AuditStore{path: path}
	_ = store.Record(&AuditRecord{Timestamp: base, Host: "srv1"})
	// Append a line longer than the 64 KiB scanner buffer — bufio.ErrTooLong.
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = fh.WriteString(strings.Repeat("x", 64*1024+1) + "\n")
	_ = fh.Close()

	_, err = store.History(10)
	if err == nil {
		t.Fatal("expected scanner error from History on oversized line, got nil")
	}
}

// ── Prune error paths ─────────────────────────────────────────────────────────

// TestPrune_ScanError verifies that Prune propagates a scanner error (here:
// an oversized line in the JSONL file) and returns (0, err).
func TestPrune_ScanError(t *testing.T) {
	f, err := os.CreateTemp("", "audit_test_prunescane_*.jsonl")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	// Write one old record, then a line that overflows the scanner buffer.
	store := &AuditStore{path: path}
	old := AuditRecord{Timestamp: time.Now().Add(-48 * time.Hour), Host: "srv1"}
	_ = store.Record(&old)
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = fh.WriteString(strings.Repeat("y", 64*1024+1) + "\n")
	_ = fh.Close()

	pruned, err := store.Prune(24 * time.Hour)
	if err == nil {
		t.Fatal("expected scanner error from Prune on oversized line, got nil")
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 on scan error", pruned)
	}
}

// TestPrune_CreateTempError verifies that Prune returns an error when the
// temporary file cannot be created (here: a directory already exists at the
// tmp path blocking os.Create).
func TestPrune_CreateTempError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	store := &AuditStore{path: path}
	// Write two records with timestamps more than 24 h apart.
	old := AuditRecord{Timestamp: time.Now().Add(-48 * time.Hour), Host: "srv1"}
	recent := AuditRecord{Timestamp: time.Now(), Host: "srv1"}
	_ = store.Record(&old)
	_ = store.Record(&recent)

	// Create a directory at the tmp path so os.Create fails.
	tmpPath := path + ".tmp"
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	pruned, err := store.Prune(24 * time.Hour)
	if err == nil {
		t.Fatal("expected error from Prune when tmp file cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "create temp file") {
		t.Errorf("error = %q, want 'create temp file' in message", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 on error", pruned)
	}
}

// ── HistoryFiltered / ChangesFiltered / Changes scan-error paths ──────────────

// overSizedLine writes a line longer than the scanner's 64 KiB buffer to
// trigger bufio.ErrTooLong from scanRecords, producing a non-nil error return.
func writeOverSizedLine(t *testing.T, path string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("writeOverSizedLine: open %s: %v", path, err)
	}
	_, _ = fh.WriteString(strings.Repeat("z", 64*1024+1) + "\n")
	_ = fh.Close()
}

// TestHistoryFiltered_ScanError verifies that HistoryFiltered propagates a
// scanner error and returns (nil, err).
func TestHistoryFiltered_ScanError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	store := &AuditStore{path: path}

	rec := AuditRecord{Timestamp: time.Now(), Host: "srv1"}
	_ = store.Record(&rec)
	writeOverSizedLine(t, path)

	recs, err := store.HistoryFiltered(10, nil, nil)
	if err == nil {
		t.Fatal("expected scanner error from HistoryFiltered, got nil")
	}
	if recs != nil {
		t.Errorf("records = %v, want nil on error", recs)
	}
}

// TestChangesFiltered_ScanError verifies that ChangesFiltered propagates a
// scanner error and returns (nil, err).
func TestChangesFiltered_ScanError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	store := &AuditStore{path: path}

	rec := AuditRecord{Timestamp: time.Now(), Host: "srv1", Changed: true}
	_ = store.Record(&rec)
	writeOverSizedLine(t, path)

	recs, err := store.ChangesFiltered(10, nil, nil)
	if err == nil {
		t.Fatal("expected scanner error from ChangesFiltered, got nil")
	}
	if recs != nil {
		t.Errorf("records = %v, want nil on error", recs)
	}
}

// TestChanges_ScanError verifies that Changes propagates a scanner error and
// returns (nil, err).
func TestChanges_ScanError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	store := &AuditStore{path: path}

	rec := AuditRecord{Timestamp: time.Now(), Host: "srv1", Changed: true}
	_ = store.Record(&rec)
	writeOverSizedLine(t, path)

	recs, err := store.Changes(10)
	if err == nil {
		t.Fatal("expected scanner error from Changes, got nil")
	}
	if recs != nil {
		t.Errorf("records = %v, want nil on error", recs)
	}
}

// TestScanRecords_OpenError verifies that scanRecords returns a wrapped error
// (not nil) when os.Open fails with a non-NotExist error (here: a null byte in
// the path produces syscall.EINVAL which is not os.IsNotExist).
func TestScanRecords_OpenError(t *testing.T) {
	store := &AuditStore{path: "invalid\x00path"}

	recs, err := store.History(1)
	if err == nil {
		t.Fatal("expected error from History with invalid path, got nil")
	}
	if !strings.Contains(err.Error(), "open audit file") {
		t.Errorf("error = %q, want 'open audit file' in message", err)
	}
	if recs != nil {
		t.Errorf("records = %v, want nil on error", recs)
	}
}

// ── GetHistory ────────────────────────────────────────────────────────────────

// TestGetHistory_ReturnsAllRecords verifies the basic happy path: records written
// to the JSONL file are returned in newest-first order.
func TestGetHistory_ReturnsAllRecords(t *testing.T) {
	store, cleanup := writeTestRecords(t, []AuditRecord{
		ptr(time.Now().Add(-2*time.Hour), false),
		ptr(time.Now().Add(-time.Hour), false),
		ptr(time.Now(), true),
	})
	defer cleanup()

	recs, err := GetHistory(HistoryOptions{DBPath: store.path})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 3 {
		t.Errorf("len = %d, want 3", len(recs))
	}
}

// TestGetHistory_ChangesOnly verifies that setting ChangesOnly=true returns only
// records where Changed is true.
func TestGetHistory_ChangesOnly(t *testing.T) {
	store, cleanup := writeTestRecords(t, []AuditRecord{
		ptr(time.Now().Add(-2*time.Hour), false),
		ptr(time.Now().Add(-time.Hour), true),
		ptr(time.Now(), false),
	})
	defer cleanup()

	recs, err := GetHistory(HistoryOptions{DBPath: store.path, ChangesOnly: true})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("len = %d, want 1 (only changed records)", len(recs))
	}
	if !recs[0].Changed {
		t.Error("expected Changed=true on returned record")
	}
}

// TestGetHistory_LimitRespected verifies that the Limit field caps results.
func TestGetHistory_LimitRespected(t *testing.T) {
	var raws []AuditRecord
	base := time.Now()
	for i := range 5 {
		raws = append(raws, ptr(base.Add(time.Duration(i)*time.Second), false))
	}
	store, cleanup := writeTestRecords(t, raws)
	defer cleanup()

	recs, err := GetHistory(HistoryOptions{DBPath: store.path, Limit: 2})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("len = %d, want 2 (limit applied)", len(recs))
	}
}

// TestGetHistory_InvalidPath verifies that GetHistory returns an error when the
// audit file directory cannot be created (a file already exists at the would-be
// directory path), exercising the "open audit store" error return.
func TestGetHistory_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	// Place a file where the directory would need to exist so MkdirAll fails.
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

// TestPrune_RenameError verifies that Prune returns an error containing
// "rename temp file" when os.Rename fails. This is forced by holding an
// exclusive (no-share-delete) Windows handle on the destination file so that
// MoveFileEx cannot replace it.
func TestPrune_RenameError(t *testing.T) {
	now := time.Now().UTC()
	records := []AuditRecord{
		{Timestamp: now.Add(-48 * time.Hour), Host: "srv1"}, // old — would be pruned
		{Timestamp: now, Host: "srv1"},                      // recent — kept
	}
	store, cleanup := writeTestRecords(t, records)
	defer cleanup()

	// Open the audit file without FILE_SHARE_DELETE so that os.Rename to it
	// fails with a sharing violation.
	pathPtr, err := windows.UTF16PtrFromString(store.path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ, // intentionally omit FILE_SHARE_DELETE
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile exclusive: %v", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	pruned, err := store.Prune(24 * time.Hour)
	if err == nil {
		t.Fatal("expected error from Prune when rename fails, got nil")
	}
	if !strings.Contains(err.Error(), "rename temp file") {
		t.Errorf("error = %q, want 'rename temp file' in message", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 on error", pruned)
	}
}
