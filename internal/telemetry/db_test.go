//go:build windows

package telemetry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
