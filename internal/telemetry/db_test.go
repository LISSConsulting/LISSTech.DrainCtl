//go:build windows

package telemetry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func queryPragmaInt(t *testing.T, db *DB, pragma string) int {
	t.Helper()
	var v int
	row := db.reader.QueryRow("PRAGMA " + pragma)
	if err := row.Scan(&v); err != nil {
		t.Fatalf("query PRAGMA %s: %v", pragma, err)
	}
	return v
}

func queryPragmaStr(t *testing.T, db *DB, pragma string) string {
	t.Helper()
	var v string
	row := db.reader.QueryRow("PRAGMA " + pragma)
	if err := row.Scan(&v); err != nil {
		t.Fatalf("query PRAGMA %s: %v", pragma, err)
	}
	return v
}

func TestOpen_AppliesPragmas(t *testing.T) {
	db := openTestDB(t)

	if got := queryPragmaStr(t, db, "journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
	if got := queryPragmaInt(t, db, "synchronous"); got != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", got)
	}
	if got := queryPragmaInt(t, db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d, want 1 (ON)", got)
	}
	if got := queryPragmaInt(t, db, "user_version"); got != 1 {
		t.Errorf("user_version = %d, want 1", got)
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close first DB: %v", err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = db2.Close() }()

	if got := queryPragmaInt(t, db2, "user_version"); got != 1 {
		t.Errorf("second open: user_version = %d, want 1", got)
	}
}

func TestOpen_AppliesACL(t *testing.T) {
	if !isElevated() {
		t.Skip("skipping ACL test: not elevated")
	}

	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	dbPath := filepath.Join(dir, dbFileName)
	out, err := exec.Command("icacls", dbPath).Output()
	if err != nil {
		t.Fatalf("icacls: %v", err)
	}
	acl := string(out)
	if !strings.Contains(acl, "NT AUTHORITY\\SYSTEM") &&
		!strings.Contains(acl, "SYSTEM") {
		t.Errorf("expected SYSTEM in ACL, got:\n%s", acl)
	}
}

func TestOpen_PragmasAppliedToEveryConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Acquire two connections simultaneously so the pool must create both.
	conn1, err := db.reader.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn 1: %v", err)
	}
	defer func() { _ = conn1.Close() }()

	conn2, err := db.reader.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn 2: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	checkPragma := func(connName string, query func(pragma string) int) {
		t.Helper()
		if got := query("foreign_keys"); got != 1 {
			t.Errorf("%s: foreign_keys = %d, want 1", connName, got)
		}
		if got := query("busy_timeout"); got != 5000 {
			t.Errorf("%s: busy_timeout = %d, want 5000", connName, got)
		}
		if got := query("temp_store"); got != 2 {
			t.Errorf("%s: temp_store = %d, want 2 (MEMORY)", connName, got)
		}
	}

	// Test connection-local pragmas (NOT journal_mode which is file-level/persistent).
	scan1 := func(pragma string) int {
		var v int
		if err := conn1.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&v); err != nil {
			t.Errorf("conn1 PRAGMA %s: %v", pragma, err)
		}
		return v
	}
	scan2 := func(pragma string) int {
		var v int
		if err := conn2.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&v); err != nil {
			t.Errorf("conn2 PRAGMA %s: %v", pragma, err)
		}
		return v
	}
	checkPragma("conn1", scan1)
	checkPragma("conn2", scan2)
}

func TestOpen_RefusesNetworkShare(t *testing.T) {
	// GetDriveType on an unreachable UNC root returns DRIVE_NO_ROOT_DIR (1),
	// which is not DRIVE_FIXED (3) — the gate must fire before any file I/O.
	_, err := Open(`\\bogus-drainctl-test-server\share\data`)
	if err == nil {
		t.Fatal("Open on UNC path returned nil error, want error")
	}
	if !strings.Contains(err.Error(), "FR-004") {
		t.Errorf("error does not mention FR-004: %v", err)
	}
	if !strings.Contains(err.Error(), "non-local") {
		t.Errorf("error does not mention 'non-local': %v", err)
	}
}

func TestOpen_AllowsLocalFixedDrive(t *testing.T) {
	// t.TempDir() is always on the system DRIVE_FIXED volume; the drive-type
	// gate must not block a valid local path.
	db, err := Open(t.TempDir())
	if err != nil {
		if strings.Contains(err.Error(), "FR-004") {
			t.Fatalf("Open incorrectly rejected local fixed drive: %v", err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
}

// seedOneAudit inserts a single audit row on a writer DB. Used by the
// OpenReadOnly tests so the read-only roundtrip has something to return.
func seedOneAudit(t *testing.T, db *DB, host string) time.Time {
	t.Helper()
	ctx := context.Background()
	store, err := NewAuditStore(ctx, db)
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	ts := time.Now().UTC().Truncate(time.Millisecond)
	err = store.Append(ctx, AuditRecord{
		Ts:        ts,
		Host:      host,
		PrevState: 0,
		NewState:  1,
		ChangedBy: "test",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return ts
}

func TestCLIReadOnly_SidecarsPresent(t *testing.T) {
	dir := t.TempDir()

	// Leave the writer open during the CLI read — this is the live case
	// covered by research.md §13(a): service running, CLI reader joins via
	// WAL. SQLite deletes -wal/-shm on last-connection-close, so we can't
	// produce this state any other way from a test.
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	wantHost := "host-a"
	seedOneAudit(t, db, wantHost)

	dbPath := filepath.Join(dir, dbFileName)
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("WAL sidecar missing with active writer: %v", err)
	}
	if _, err := os.Stat(shmPath); err != nil {
		t.Fatalf("SHM sidecar missing with active writer: %v", err)
	}

	roDB, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = roDB.Close() }()

	if roDB.writer != nil || roDB.auditDB != nil || roDB.checkpointDB != nil {
		t.Errorf("read-only DB must not populate write pools (writer=%v audit=%v checkpoint=%v)",
			roDB.writer != nil, roDB.auditDB != nil, roDB.checkpointDB != nil)
	}

	store := NewReadOnlyAuditStore(roDB)
	defer func() { _ = store.Close() }()
	records, _, err := store.QueryRange(context.Background(), QueryFilter{})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 1 || records[0].Host != wantHost {
		t.Fatalf("expected 1 audit record for host=%s, got %+v", wantHost, records)
	}

	// Append on a read-only store is blocked rather than silently no-op.
	if err := store.Append(context.Background(), AuditRecord{Host: "x", NewState: 1}); err == nil {
		t.Error("Append on read-only audit store returned nil; want error")
	}
}

func TestCLIReadOnly_SidecarsMissing(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	wantHost := "host-b"
	seedOneAudit(t, db, wantHost)
	// TRUNCATE checkpoint before close so data lands in the main file; the
	// test then deletes the sidecars to simulate "service cleanly shut down
	// and checkpointed" (research.md §13 case b).
	if _, err := db.writer.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(dir, dbFileName)
	if err := os.Remove(dbPath + "-wal"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove WAL: %v", err)
	}
	if err := os.Remove(dbPath + "-shm"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove SHM: %v", err)
	}

	roDB, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("OpenReadOnly without sidecars: %v", err)
	}
	defer func() { _ = roDB.Close() }()

	store := NewReadOnlyAuditStore(roDB)
	defer func() { _ = store.Close() }()
	records, _, err := store.QueryRange(context.Background(), QueryFilter{})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(records) != 1 || records[0].Host != wantHost {
		t.Fatalf("expected 1 audit record for host=%s, got %+v", wantHost, records)
	}
}

func TestCLIReadOnly_SidecarUnreadableAclError(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedOneAudit(t, db, "host-c")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(dir, dbFileName)
	walPath := dbPath + "-wal"
	// SQLite deletes sidecars on clean close. Recreate a placeholder -wal so
	// the test exercises validateReadOnlySidecars' read-probe branch against
	// a real file; the content is irrelevant because the exclusive handle
	// below blocks all other opens before the bytes matter.
	if err := os.WriteFile(walPath, []byte{0}, 0o600); err != nil {
		t.Fatalf("seed placeholder WAL: %v", err)
	}

	// Hold -wal with dwShareMode=0 so any subsequent CreateFile for the file
	// (including os.Open inside the validator) fails with ERROR_SHARING_VIOLATION.
	// Models the "CLI process cannot read the sidecar" case — same observable
	// failure mode as an ACL-denied open.
	walPtr, err := windows.UTF16PtrFromString(walPath)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(
		walPtr,
		windows.GENERIC_READ,
		0, // no sharing
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile exclusive on WAL: %v", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	_, openErr := OpenReadOnly(dir)
	if openErr == nil {
		t.Fatal("OpenReadOnly succeeded with exclusively-locked WAL; want error")
	}
	if !strings.Contains(openErr.Error(), walPath) {
		t.Errorf("error does not cite WAL sidecar path %q: %v", walPath, openErr)
	}
	if !strings.Contains(openErr.Error(), "cannot read") {
		t.Errorf("error does not describe read failure: %v", openErr)
	}
	if !strings.Contains(openErr.Error(), "read permission") {
		t.Errorf("error does not mention required permission: %v", openErr)
	}
}

func TestCLIReadOnly_SidecarCorruptError(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedOneAudit(t, db, "host-d")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(dir, dbFileName)
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	// Clean close deletes sidecars. Plant a -wal placeholder with no matching
	// -shm to simulate a partially-closed writer (research.md §13 case d):
	// WAL on disk but shared memory gone.
	if err := os.WriteFile(walPath, []byte{0}, 0o600); err != nil {
		t.Fatalf("seed placeholder WAL: %v", err)
	}
	if _, err := os.Stat(shmPath); err == nil {
		t.Fatalf("SHM sidecar unexpectedly present after clean close")
	}

	_, openErr := OpenReadOnly(dir)
	if openErr == nil {
		t.Fatal("OpenReadOnly succeeded with WAL-but-no-SHM; want error")
	}
	if !strings.Contains(openErr.Error(), walPath) {
		t.Errorf("error does not cite WAL path %q: %v", walPath, openErr)
	}
	if !strings.Contains(openErr.Error(), shmPath) {
		t.Errorf("error does not cite SHM path %q: %v", shmPath, openErr)
	}
	if !strings.Contains(openErr.Error(), "partially-closed") {
		t.Errorf("error does not describe partially-closed writer: %v", openErr)
	}
}

func TestOpen_CorruptFileReturnsClearError(t *testing.T) {
	dir := t.TempDir()

	// Create a valid DB with data and force a checkpoint so the main file
	// contains actual page content (not just the WAL).
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.writer.Exec(
		`INSERT OR IGNORE INTO schema_meta(key,value) VALUES('test_key','test_value')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Force WAL to flush into the main file so corruption is detectable.
	if _, err := db.writer.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Corrupt page 2 of the main file (bytes 4096..8191) with zeroes.
	dbPath := filepath.Join(dir, dbFileName)
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	if _, err := f.WriteAt(make([]byte, 512), 4096); err != nil {
		_ = f.Close()
		t.Fatalf("write corruption: %v", err)
	}
	_ = f.Close()

	_, openErr := Open(dir)
	if openErr == nil {
		t.Fatal("Open on corrupt DB returned nil error, want error")
	}
	if !strings.Contains(openErr.Error(), dbPath) {
		t.Errorf("error does not contain DB path %q: %v", dbPath, openErr)
	}
	if !strings.Contains(openErr.Error(), "integrity") {
		t.Errorf("error does not contain 'integrity': %v", openErr)
	}
}
