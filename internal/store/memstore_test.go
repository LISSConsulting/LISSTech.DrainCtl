//go:build windows

package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// newTestStore opens a MemAuditStore at a temp file and returns the store and
// a cleanup function. The caller must invoke cleanup even on test failure.
func newTestStore(t *testing.T) (*MemAuditStore, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("OpenMemAuditStore: %v", err)
	}
	return st, func() { _ = st.Close() }
}

func rec(mode dc.DrainMode, ts time.Time, changed bool) *dc.AuditRecord {
	return &dc.AuditRecord{
		Timestamp:  ts,
		Host:       "test-host",
		DrainMode:  mode,
		DrainLabel: mode.String(),
		Changed:    changed,
	}
}

// ── LastObservation ──────────────────────────────────────────────────────────

func TestLastObservation_Empty(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	if got := st.LastObservation(); got != nil {
		t.Errorf("expected nil on empty store, got %+v", got)
	}
}

func TestLastObservation_AfterAppend(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	t1 := time.Now().Add(-2 * time.Second).Truncate(time.Millisecond)
	t2 := time.Now().Truncate(time.Millisecond)

	st.Append(rec(dc.AllowAll, t1, false))
	st.Append(rec(dc.PreventNewLogon, t2, true))

	got := st.LastObservation()
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.DrainMode != dc.PreventNewLogon {
		t.Errorf("mode = %v, want PreventNewLogon", got.DrainMode)
	}
	if !got.Timestamp.Equal(t2) {
		t.Errorf("timestamp = %v, want %v", got.Timestamp, t2)
	}
}

// ── History ──────────────────────────────────────────────────────────────────

func TestHistory_Empty(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	if got := st.History(0); len(got) != 0 {
		t.Errorf("expected empty slice, got %d records", len(got))
	}
}

func TestHistory_NewestFirst(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), false))
	}

	got := st.History(0)
	if len(got) != 5 {
		t.Fatalf("want 5 records, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.After(got[i-1].Timestamp) {
			t.Errorf("records[%d] is newer than records[%d] — not sorted newest-first", i, i-1)
		}
	}
}

func TestHistory_LimitTruncates(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	for i := 0; i < 10; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), false))
	}

	got := st.History(3)
	if len(got) != 3 {
		t.Errorf("want 3 records with limit=3, got %d", len(got))
	}
}

func TestHistory_LimitZeroReturnsAll(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	for i := 0; i < 7; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), false))
	}

	got := st.History(0)
	if len(got) != 7 {
		t.Errorf("want 7 records with limit=0, got %d", len(got))
	}
}

// ── Changes ──────────────────────────────────────────────────────────────────

func TestChanges_FiltersByChanged(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	st.Append(rec(dc.AllowAll, base, false))
	st.Append(rec(dc.PreventNewLogon, base.Add(time.Second), true))
	st.Append(rec(dc.PreventNewLogon, base.Add(2*time.Second), false))
	st.Append(rec(dc.AllowAll, base.Add(3*time.Second), true))

	got := st.Changes(0)
	if len(got) != 2 {
		t.Fatalf("want 2 change records, got %d", len(got))
	}
	// Newest-first: t+3s, then t+1s.
	if !got[0].Timestamp.Equal(base.Add(3 * time.Second)) {
		t.Errorf("changes[0].Timestamp = %v, want %v", got[0].Timestamp, base.Add(3*time.Second))
	}
}

func TestChanges_LimitRespected(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	for i := 0; i < 10; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), true))
	}

	got := st.Changes(4)
	if len(got) != 4 {
		t.Errorf("want 4, got %d", len(got))
	}
}

func TestChanges_EmptyWhenNoTransitions(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	for i := 0; i < 5; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), false))
	}

	got := st.Changes(0)
	if len(got) != 0 {
		t.Errorf("want 0 change records, got %d", len(got))
	}
}

// ── StateSince ───────────────────────────────────────────────────────────────

func TestStateSince_EmptyStoreNil(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	if got := st.StateSince(dc.AllowAll); got != nil {
		t.Errorf("expected nil on empty store, got %v", got)
	}
}

func TestStateSince_AllSameMode(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		st.Append(rec(dc.AllowAll, base.Add(time.Duration(i)*time.Second), i == 0))
	}

	got := st.StateSince(dc.AllowAll)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if !got.Equal(base) {
		t.Errorf("StateSince = %v, want %v (first record)", got, base)
	}
}

func TestStateSince_AfterTransition(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now().Truncate(time.Millisecond)
	// AllowAll for a while, then PreventNewLogon starts.
	st.Append(rec(dc.AllowAll, base, false))
	st.Append(rec(dc.AllowAll, base.Add(time.Second), false))
	transitionTime := base.Add(2 * time.Second)
	st.Append(rec(dc.PreventNewLogon, transitionTime, true))
	st.Append(rec(dc.PreventNewLogon, base.Add(3*time.Second), false))

	got := st.StateSince(dc.PreventNewLogon)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if !got.Equal(transitionTime) {
		t.Errorf("StateSince(Drain) = %v, want %v", got, transitionTime)
	}
}

func TestStateSince_CurrentModeNotPresent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	st.Append(rec(dc.AllowAll, base, false))

	// Asking for a mode that hasn't appeared yet returns nil.
	got := st.StateSince(dc.PreventNewLogon)
	if got != nil {
		t.Errorf("expected nil for absent mode, got %v", got)
	}
}

func TestStateSince_ReturnsToCurrent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// Pattern: AllowAll → Drain → AllowAll again.
	// StateSince(AllowAll) should point to the second AllowAll run.
	base := time.Now().Truncate(time.Millisecond)
	st.Append(rec(dc.AllowAll, base, false))
	st.Append(rec(dc.PreventNewLogon, base.Add(time.Second), true))
	resumeTime := base.Add(2 * time.Second)
	st.Append(rec(dc.AllowAll, resumeTime, true))
	st.Append(rec(dc.AllowAll, base.Add(3*time.Second), false))

	got := st.StateSince(dc.AllowAll)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if !got.Equal(resumeTime) {
		t.Errorf("StateSince(AllowAll) = %v, want %v (second run)", got, resumeTime)
	}
}

// ── Flush / Prune / Persistence ──────────────────────────────────────────────

func TestFlush_PersistsRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	base := time.Now().Truncate(time.Millisecond)
	st.Append(rec(dc.AllowAll, base, false))
	st.Append(rec(dc.PreventNewLogon, base.Add(time.Second), true))

	if err := st.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_ = st.Close()

	// Reopen and confirm records loaded.
	st2, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st2.Close() }()

	got := st2.History(0)
	if len(got) != 2 {
		t.Errorf("after reload: want 2 records, got %d", len(got))
	}
}

func TestClose_FlushesBeforeClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	base := time.Now().Truncate(time.Millisecond)
	st.Append(rec(dc.AllowAll, base, false))
	// Close without explicit Flush — Close should flush.
	_ = st.Close()

	// File should contain 1 record.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("file is empty after Close — records were not flushed")
	}
}

func TestPrune_RemovesOldRecords(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	base := time.Now()
	// Two old records (2 and 1 day ago).
	st.Append(rec(dc.AllowAll, base.Add(-48*time.Hour), false))
	st.Append(rec(dc.AllowAll, base.Add(-24*time.Hour), false))
	// One fresh record.
	st.Append(rec(dc.AllowAll, base, false))

	pruned, err := st.Prune(12 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Errorf("want 2 pruned, got %d", pruned)
	}

	got := st.History(0)
	if len(got) != 1 {
		t.Errorf("want 1 record after prune, got %d", len(got))
	}
}

func TestPrune_NothingToRemove(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	st.Append(rec(dc.AllowAll, time.Now(), false))

	pruned, err := st.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 0 {
		t.Errorf("want 0 pruned, got %d", pruned)
	}
}

func TestFlushIfDirty_SkipsWhenClean(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// No appends — dirty count is zero, FlushIfDirty should be a no-op.
	if err := st.FlushIfDirty(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestFlushIfDirty_FlushesWhenDirty verifies the dirty>0 path: FlushIfDirty
// must call Flush and persist the record when there are unflushed appends.
func TestFlushIfDirty_FlushesWhenDirty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	st.Append(rec(dc.AllowAll, time.Now(), false))

	if err := st.FlushIfDirty(); err != nil {
		t.Fatalf("FlushIfDirty: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("file is empty after FlushIfDirty — dirty record was not flushed")
	}
}

// TestClose_IdempotentOnSecondCall verifies the file==nil guard in Close:
// calling Close a second time must be a no-op and must not panic.
func TestClose_IdempotentOnSecondCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// Second close — exercises the `if m.file == nil { return nil }` path.
	if err := st.Close(); err != nil {
		t.Errorf("second close returned error: %v", err)
	}
}

// TestOpenMemAuditStore_NilLog verifies that passing nil as the log function
// does not panic — the nil guard replaces it with a discard logger.
func TestOpenMemAuditStore_NilLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	st, err := OpenMemAuditStore(path, nil)
	if err != nil {
		t.Fatalf("OpenMemAuditStore with nil log: %v", err)
	}
	_ = st.Close()
}

// TestOpenMemAuditStore_MkdirAllError verifies that OpenMemAuditStore returns
// an error when the directory cannot be created because a plain file already
// exists at the would-be directory path.
func TestOpenMemAuditStore_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Place a regular file where the subdirectory should be created.
	blockingFile := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	path := filepath.Join(blockingFile, "audit.jsonl") // blocked\audit.jsonl

	_, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err == nil {
		t.Fatal("expected error when directory cannot be created, got nil")
	}
}

// TestOpenMemAuditStore_InvalidPathError verifies that OpenMemAuditStore returns
// an "invalid path" error when the path contains a null byte, which causes
// windows.UTF16PtrFromString to fail with EINVAL.
func TestOpenMemAuditStore_InvalidPathError(t *testing.T) {
	dir := t.TempDir()
	// Embed a null byte — UTF16PtrFromString rejects paths containing \x00.
	path := filepath.Join(dir, "audit\x00.jsonl")

	_, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err == nil {
		t.Fatal("expected error for null-byte path, got nil")
	}
	if !strings.Contains(err.Error(), "invalid path") {
		t.Errorf("error = %q, want message containing 'invalid path'", err.Error())
	}
}

// TestOpenMemAuditStore_CreateFileError verifies that OpenMemAuditStore returns
// an "open audit file" error when windows.CreateFile fails because a directory
// already exists at the file path — OPEN_ALWAYS with GENERIC_WRITE on a
// directory returns ERROR_ACCESS_DENIED on Windows.
func TestOpenMemAuditStore_CreateFileError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory at the exact audit-file path so CreateFile fails.
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err == nil {
		t.Fatal("expected error when CreateFile targets a directory, got nil")
	}
	if !strings.Contains(err.Error(), "open audit file") {
		t.Errorf("error = %q, want message containing 'open audit file'", err.Error())
	}
}

// TestOpenMemAuditStore_LoadError_OversizedLine verifies that OpenMemAuditStore
// returns an error (wrapping the load error) when the audit file contains a
// line that exceeds the 64 KiB scanner buffer limit.
func TestOpenMemAuditStore_LoadError_OversizedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	// Write a single line that exceeds the 64 KiB scanner buffer.
	oversized := make([]byte, 65*1024+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	oversized[len(oversized)-1] = '\n'
	if err := os.WriteFile(path, oversized, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err == nil {
		t.Fatal("expected error when audit file has oversized line, got nil")
	}
}

// ── Concurrent access ────────────────────────────────────────────────────────

func TestConcurrentAppendAndRead(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	const goroutines = 8
	const recordsEach = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < recordsEach; i++ {
				st.Append(rec(dc.AllowAll, time.Now(), false))
				_ = st.LastObservation()
				_ = st.History(10)
				_ = st.Changes(10)
			}
		}()
	}
	wg.Wait()

	got := st.History(0)
	if len(got) != goroutines*recordsEach {
		t.Errorf("want %d records after concurrent appends, got %d", goroutines*recordsEach, len(got))
	}
}

// ── load empty-line skip ──────────────────────────────────────────────────────

// TestLoad_EmptyLineSkipped verifies that load skips blank lines in the JSONL
// file and still returns both surrounding valid records.
// This exercises the `if len(line) == 0 { continue }` branch in load.
func TestLoad_EmptyLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	now := time.Now().Truncate(time.Millisecond)
	r1 := dc.AuditRecord{Timestamp: now, Host: "h1", DrainLabel: "AllowAll"}
	r2 := dc.AuditRecord{Timestamp: now.Add(time.Second), Host: "h1", DrainLabel: "AllowAll"}
	data1, _ := json.Marshal(r1)
	data2, _ := json.Marshal(r2)

	// Embed a bare blank line between two valid JSONL records.
	content := string(data1) + "\n\n" + string(data2) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("OpenMemAuditStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	got := st.History(0)
	if len(got) != 2 {
		t.Errorf("want 2 records (empty line skipped), got %d", len(got))
	}
}

// ── flushLocked seek error ────────────────────────────────────────────────────

// TestFlushLocked_SeekError verifies that flushLocked (called via Flush) returns
// an error when the underlying file handle is closed — exercising the
// `m.file.Seek(0, 2)` error-return path in flushLocked.
func TestFlushLocked_SeekError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup() // cleanup ignores errors from Close — safe even after file close

	// Mark the store dirty so flushLocked proceeds past the dirty==0 guard.
	st.Append(rec(dc.AllowAll, time.Now(), false))

	// Force a file-level error by closing the OS file without going through Close().
	// flushLocked will attempt Seek on the now-closed handle and must return an error.
	_ = st.file.Close()

	if err := st.Flush(); err == nil {
		t.Fatal("expected error from Flush after file close, got nil")
	}
}

// ── load seek error ───────────────────────────────────────────────────────────

// TestLoad_SeekError verifies that load returns an error when the underlying
// file handle is closed — exercising the `m.file.Seek(0, 0)` error-return path
// at the top of load.
func TestLoad_SeekError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// Close the OS file handle so the Seek call inside load fails.
	_ = st.file.Close()

	if err := st.load(); err == nil {
		t.Fatal("expected error from load after file close, got nil")
	}
}

// ── Prune seek error ──────────────────────────────────────────────────────────

// TestPrune_SeekError verifies that Prune returns (0, error) when the
// underlying file cannot be seeked — exercising the `m.file.Seek(0, 0)`
// error-return path in Prune.
func TestPrune_SeekError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// Add an old record so Prune finds something to remove and reaches the
	// rewrite stage (Seek + Truncate).
	st.Append(rec(dc.AllowAll, time.Now().Add(-48*time.Hour), false))

	// Close the file to force I/O errors on the rewrite seek.
	_ = st.file.Close()

	_, err := st.Prune(12 * time.Hour)
	if err == nil {
		t.Fatal("expected error from Prune after file close, got nil")
	}
}

// TestPrune_EmptyStoreReturnsZero verifies the len(m.records)==0 early-return
// path in Prune: calling Prune on a store that has never had records appended
// must return (0, nil) without touching the file.
func TestPrune_EmptyStoreReturnsZero(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	pruned, err := st.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune on empty store: %v", err)
	}
	if pruned != 0 {
		t.Errorf("want 0 pruned, got %d", pruned)
	}
}

// ── load — malformed JSON skip ────────────────────────────────────────────────

// TestLoad_MalformedJSONSkipped verifies that load silently skips lines that
// cannot be parsed as AuditRecord JSON, continuing to load valid records that
// follow the corrupted line.
func TestLoad_MalformedJSONSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	now := time.Now().Truncate(time.Millisecond)
	r1 := dc.AuditRecord{Timestamp: now, Host: "h1", DrainLabel: "AllowAll"}
	r2 := dc.AuditRecord{Timestamp: now.Add(time.Second), Host: "h1", DrainLabel: "AllowAll"}
	data1, _ := json.Marshal(r1)
	data2, _ := json.Marshal(r2)

	// Embed a malformed JSON line between two valid JSONL records.
	content := string(data1) + "\n" + "{not valid json!!}" + "\n" + string(data2) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st, err := OpenMemAuditStore(path, dc.DiscardLogger())
	if err != nil {
		t.Fatalf("OpenMemAuditStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	got := st.History(0)
	if len(got) != 2 {
		t.Errorf("want 2 records (malformed line skipped), got %d", len(got))
	}
}

// ── flushLocked — flush write error ──────────────────────────────────────────

// TestFlushLocked_FlushError verifies that flushLocked returns a "flush: ..."
// error when the underlying file write fails. This is exercised by replacing
// the store's file handle with a read-only handle: Seek(0,2) still succeeds
// (seeking does not require write access), but bufio.Writer.Flush fails when
// it attempts to write the buffered bytes to the read-only handle.
func TestFlushLocked_FlushError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// Mark the store dirty so flushLocked proceeds past the dirty==0 guard.
	st.Append(rec(dc.AllowAll, time.Now(), false))

	path := st.file.Name()

	// Close the read-write handle (releases the exclusive write lock).
	if err := st.file.Close(); err != nil {
		t.Fatalf("close original: %v", err)
	}

	// Reopen the same file in read-only mode and inject it as the store's handle.
	// Seek(0,2) will succeed; any Write through bufio.Flush will fail.
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	st.file = readOnly

	flushErr := st.Flush()
	if flushErr == nil {
		t.Fatal("expected error from Flush on read-only file, got nil")
	}
	if !strings.Contains(flushErr.Error(), "flush") {
		t.Errorf("error = %q, want 'flush' in message", flushErr.Error())
	}
}

// ── Prune — truncate error ────────────────────────────────────────────────────

// TestPrune_TruncateError verifies that Prune returns a "truncate: ..." error
// when file.Truncate(0) fails. The same read-only handle trick used in
// TestFlushLocked_FlushError is applied: after close and reopen as read-only,
// Seek(0,0) succeeds but Truncate fails (SetEndOfFile requires write access).
func TestPrune_TruncateError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	// Add a record old enough to be pruned so Prune reaches the rewrite stage.
	st.Append(rec(dc.AllowAll, time.Now().Add(-48*time.Hour), false))

	path := st.file.Name()

	// Flush so the record is on disk, then close the read-write handle.
	if err := st.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := st.file.Close(); err != nil {
		t.Fatalf("close original: %v", err)
	}

	// Reopen read-only: Seek(0,0) succeeds, Truncate(0) fails.
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	st.file = readOnly

	_, pruneErr := st.Prune(12 * time.Hour)
	if pruneErr == nil {
		t.Fatal("expected error from Prune with read-only file, got nil")
	}
	if !strings.Contains(pruneErr.Error(), "truncate") {
		t.Errorf("error = %q, want 'truncate' in message", pruneErr.Error())
	}
}
