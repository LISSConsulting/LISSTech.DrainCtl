//go:build windows

// Package telemetry manages the SQLite telemetry store (drainctl.db).
// The named mutex that serialises config.json writes is separate from
// SQLite's own WAL locking — do not conflate them (FR-027).
package telemetry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/winacl"
	"golang.org/x/sys/windows"
	sqlite "modernc.org/sqlite"
)

const (
	dbFileName = "drainctl.db"

	walSizeEscalateBytes = 16 * 1024 * 1024 // 16 MB — escalate to TRUNCATE above this
	walSizeWarnBytes     = 64 * 1024 * 1024 // 64 MB — log WARN above this
	walTruncateHoldoff   = time.Hour        // minimum gap between TRUNCATE escalations
	readerMaxLifetime    = 60 * time.Second // max read-transaction lifetime (WAL pin guard)
	checkpointDeadline   = 5 * time.Second  // per-checkpoint call budget
	// Keep burst concurrency but retain one idle reader. Each modernc SQLite
	// connection owns native state and a page cache; holding four idle readers
	// wastes memory on agents between rare CLI/dashboard queries.
	readerMaxOpenConns = 4
	readerMaxIdleConns = 1
)

// DB holds the writer, reader, audit, and checkpoint sql.DB pools for drainctl.db.
type DB struct {
	writer         *sql.DB
	reader         *sql.DB
	auditDB        *sql.DB // separate single-conn pool, synchronous=FULL, used by AuditStore (FR-001b)
	checkpointDB   *sql.DB // separate pool, busy_timeout=0, used only for WAL checkpoints
	path           string
	lastTruncateAt time.Time // when the last TRUNCATE escalation ran
}

// connectionPragmas is issued on every new pooled connection — both writer and
// reader pools share the same pragmaConnector so both receive this block.
// journal_mode and auto_vacuum are file-level/persistent but safe to re-issue;
// the remaining pragmas are connection-local and must be applied per-connection.
var connectionPragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 5000",
	"PRAGMA wal_autocheckpoint = 1000",
	"PRAGMA cache_size = -2048",
	"PRAGMA temp_store = FILE",
	"PRAGMA mmap_size = 0",
	"PRAGMA auto_vacuum = INCREMENTAL",
}

// checkpointPragmas is the same as connectionPragmas except busy_timeout=0 so
// the checkpoint connection never blocks writers when the WAL is contended.
var checkpointPragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 0",
	"PRAGMA temp_store = FILE",
}

// auditPragmas upgrades the audit write path to synchronous=FULL so that
// commits are durable against OS crash / power loss (research.md §2).
// Metrics writes stay at NORMAL; audit volume is low enough to absorb the
// extra fsync cost.
var auditPragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = FULL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 5000",
	"PRAGMA temp_store = FILE",
}

// readOnlyPragmas is applied on CLI fallback reads (tasks.md T028a,
// research.md §13). Only connection-local pragmas that are legal on a
// read-only connection — journal_mode, synchronous, wal_autocheckpoint,
// and auto_vacuum cannot be modified without write access so we inherit
// whatever the file has persisted.
var readOnlyPragmas = []string{
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 5000",
	"PRAGMA temp_store = FILE",
	"PRAGMA cache_size = -2048",
	"PRAGMA mmap_size = 0",
}

// pragmaConnector implements driver.Connector so that every connection obtained
// from either pool receives the specified pragma block before use.
// modernc.org/sqlite's Driver implements driver.Driver but not DriverContext,
// so we call drv.Open(dsn) directly and apply pragmas on the resulting conn.
type pragmaConnector struct {
	dsn     string
	drv     driver.Driver
	pragmas []string
}

func (c *pragmaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	pragmas := c.pragmas
	if pragmas == nil {
		pragmas = connectionPragmas
	}
	for _, p := range pragmas {
		if err := execDriverConn(ctx, conn, p); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("telemetry: connection pragma %s: %w", p, err)
		}
	}
	return conn, nil
}

func (c *pragmaConnector) Driver() driver.Driver { return c.drv }

// execDriverConn issues a single no-arg statement on a raw driver.Conn.
// modernc.org/sqlite always implements ExecerContext; if a future driver does
// not, Open returns a clear error rather than degrading silently.
func execDriverConn(ctx context.Context, conn driver.Conn, query string) error {
	ec, ok := conn.(driver.ExecerContext)
	if !ok {
		return fmt.Errorf("telemetry: driver conn does not implement ExecerContext")
	}
	_, err := ec.ExecContext(ctx, query, nil)
	return err
}

// checkDriveType returns an error if dataDir does not reside on a DRIVE_FIXED
// volume. Network shares, RAM disks, removable media, and unknown volumes are
// rejected per FR-004 — SQLite WAL mode is unsafe on non-local file systems.
func checkDriveType(dataDir string) error {
	// GetDriveTypeW needs the volume root (e.g. "C:\"), not the full path.
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("telemetry: resolve data dir %s: %w", dataDir, err)
	}
	root := filepath.VolumeName(abs) + `\`
	rootPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return fmt.Errorf("telemetry: encode volume root %s: %w", root, err)
	}
	dt := windows.GetDriveType(rootPtr)
	if dt != windows.DRIVE_FIXED {
		return fmt.Errorf(
			"telemetry: data dir %s is on a non-local volume (drive type %d); "+
				"drainctl.db requires a local fixed drive (FR-004)", dataDir, dt,
		)
	}
	return nil
}

// Open opens or creates drainctl.db in dataDir, applies the schema and pragmas,
// seeds initial schema_meta rows, and restricts file ACLs.
func Open(dataDir string) (*DB, error) {
	if err := checkDriveType(dataDir); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, dbFileName)
	drv := &sqlite.Driver{}

	connector := &pragmaConnector{dsn: path, drv: drv}
	writer := sql.OpenDB(connector)
	writer.SetMaxOpenConns(1)

	reader := sql.OpenDB(connector)
	reader.SetConnMaxLifetime(readerMaxLifetime)
	reader.SetMaxOpenConns(readerMaxOpenConns)
	reader.SetMaxIdleConns(readerMaxIdleConns)

	auditConnector := &pragmaConnector{dsn: path, drv: drv, pragmas: auditPragmas}
	auditDB := sql.OpenDB(auditConnector)
	auditDB.SetMaxOpenConns(1)

	checkpointConnector := &pragmaConnector{dsn: path, drv: drv, pragmas: checkpointPragmas}
	checkpointDB := sql.OpenDB(checkpointConnector)
	checkpointDB.SetMaxOpenConns(1)

	closeAll := func() {
		_ = writer.Close()
		_ = reader.Close()
		_ = auditDB.Close()
		_ = checkpointDB.Close()
	}

	if err := applySchema(writer); err != nil {
		closeAll()
		return nil, fmt.Errorf("telemetry: apply schema %s: %w", path, err)
	}

	// Integrity check before accepting ingest — catches silent storage corruption.
	// quick_check is faster than integrity_check and catches structural issues.
	if err := runQuickCheck(writer, path); err != nil {
		closeAll()
		return nil, err
	}

	if err := seedMeta(writer); err != nil {
		closeAll()
		return nil, err
	}

	db := &DB{writer: writer, reader: reader, auditDB: auditDB, checkpointDB: checkpointDB, path: path}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := restrictFileACL(path + suffix); err != nil {
			closeAll()
			return nil, fmt.Errorf("telemetry: restrict ACL %s: %w", path+suffix, err)
		}
	}
	return db, nil
}

// ReaderStats returns a snapshot of the read pool's database/sql stats.
// Test-facing accessor so reader-pool bounds can be asserted without
// exporting the unexported reader field.
func (db *DB) ReaderStats() sql.DBStats {
	return db.reader.Stats()
}

// Close releases all database connections. OpenReadOnly populates only the
// reader pool, so Close tolerates nil fields for the write/audit/checkpoint
// pools rather than panicking.
func (db *DB) Close() error {
	var cerr, aerr, werr, rerr error
	if db.checkpointDB != nil {
		cerr = db.checkpointDB.Close()
	}
	if db.auditDB != nil {
		aerr = db.auditDB.Close()
	}
	if db.writer != nil {
		werr = db.writer.Close()
	}
	if db.reader != nil {
		rerr = db.reader.Close()
	}
	if cerr != nil {
		return cerr
	}
	if aerr != nil {
		return aerr
	}
	if werr != nil {
		return werr
	}
	return rerr
}

// OpenReadOnly opens drainctl.db in read-only mode for CLI fallback queries
// (tasks.md T028a, research.md §13). The returned *DB has only the reader
// pool populated; writer, auditDB, and checkpointDB are nil. Use
// NewReadOnlyAuditStore to query through this DB — calling the write-path
// constructors against a read-only DB will nil-panic.
//
// WAL sidecar cases:
//
//	(a) -wal and -shm both present and readable → normal WAL read open
//	(b) both sidecars missing → rollback-journal read mode; live writer
//	    data may be ≤1 commit stale (logged at INFO)
//	(c) -wal present but the CLI process cannot open it (ACL, exclusive
//	    lock, etc.) → explicit error naming the sidecar path; no silent
//	    fallback
//	(d) -wal present but -shm missing / unreadable / corrupt → explicit
//	    error; no silent fallback
//
// The DSN is a SQLite URI with `mode=ro&_txlock=deferred`; `_journal_mode`
// is deliberately NOT forced so SQLite inherits the file's persisted mode.
func OpenReadOnly(dataDir string) (*DB, error) {
	if err := checkDriveType(dataDir); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, dbFileName)
	if err := validateReadOnlySidecars(path); err != nil {
		return nil, err
	}

	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_txlock=deferred"
	drv := &sqlite.Driver{}
	connector := &pragmaConnector{dsn: dsn, drv: drv, pragmas: readOnlyPragmas}

	reader := sql.OpenDB(connector)
	reader.SetConnMaxLifetime(readerMaxLifetime)
	reader.SetMaxOpenConns(readerMaxOpenConns)
	reader.SetMaxIdleConns(readerMaxIdleConns)

	// Force an actual connection so pragma failures and SQLite-level errors
	// (e.g. a malformed -shm header that slipped past the stat check) surface
	// here with the DB path in the wrapping error, rather than from the first
	// query at the call site.
	pingCtx, cancel := context.WithTimeout(context.Background(), checkpointDeadline)
	defer cancel()
	if err := reader.PingContext(pingCtx); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf(
			"telemetry: read-only open of %s failed (possible sidecar corruption): %w",
			path, err,
		)
	}

	return &DB{reader: reader, path: path}, nil
}

// validateReadOnlySidecars enforces the (b), (c), (d) cases in OpenReadOnly's
// contract before SQLite touches the files. Cheap local stat/open probes so
// the CLI surfaces ACL or partial-writer state with a clear operator-facing
// error instead of a generic "io error" from deep inside SQLite.
//
// os.Stat failures that are NOT os.ErrNotExist (ACL denied, sharing violation,
// etc.) must not be silently coerced into "missing" — that would misroute case
// (c) into case (b) and let SQLite proceed against the possibly-stale main
// file. Any non-ENOENT stat failure surfaces as a case (c) error naming the
// sidecar.
func validateReadOnlySidecars(dbPath string) error {
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"

	walExists, err := sidecarExists(walPath)
	if err != nil {
		return sidecarAccessError(dbPath, "WAL", walPath, err)
	}
	shmExists, err := sidecarExists(shmPath)
	if err != nil {
		return sidecarAccessError(dbPath, "SHM", shmPath, err)
	}

	if !walExists && !shmExists {
		slog.Info(
			"telemetry: read-only open without WAL sidecars; live writer data may be ≤1 commit stale",
			"path", dbPath,
		)
		return nil
	}

	if walExists {
		f, err := os.Open(walPath)
		if err != nil {
			return sidecarAccessError(dbPath, "WAL", walPath, err)
		}
		_ = f.Close()
	}

	if walExists && !shmExists {
		return fmt.Errorf(
			"telemetry: read-only open of %s blocked: WAL sidecar %s present but SHM sidecar %s missing "+
				"(partially-closed writer) — refusing silent fallback to stale main file",
			dbPath, walPath, shmPath,
		)
	}

	if shmExists {
		f, err := os.Open(shmPath)
		if err != nil {
			return sidecarAccessError(dbPath, "SHM", shmPath, err)
		}
		_ = f.Close()
	}

	return nil
}

// sidecarExists distinguishes "file not present" (ok, returns false/nil) from
// any other stat error (returns false + the underlying error for the caller to
// wrap). os.IsNotExist handles both POSIX ENOENT and Windows
// ERROR_FILE_NOT_FOUND / ERROR_PATH_NOT_FOUND.
func sidecarExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func sidecarAccessError(dbPath, kind, sidecarPath string, err error) error {
	return fmt.Errorf(
		"telemetry: read-only open of %s blocked: cannot read %s sidecar %s: %w "+
			"(CLI process needs read permission on the drainctl data directory)",
		dbPath, kind, sidecarPath, err,
	)
}

// WALCheckpoint runs a PASSIVE checkpoint, escalating to TRUNCATE when the WAL
// exceeds walSizeEscalateBytes. Called by the retention goroutine each cycle.
//
// busy_timeout=0 on checkpointDB means SQLITE_BUSY/SQLITE_LOCKED surfaces
// immediately as an error; we log WARN and skip rather than blocking writers.
//
// wal_checkpoint returns a (busy, log, checkpointed) row. Contention is
// signalled via busy>0 in that row — not always as a Go error — so we scan
// the row rather than relying on err alone.
func (db *DB) WALCheckpoint(ctx context.Context) {
	walPath := db.path + "-wal"
	walInfo, statErr := os.Stat(walPath)

	if statErr == nil && walInfo.Size() > walSizeWarnBytes {
		slog.Warn("telemetry: WAL size exceeds 64 MB", "size_mb", walInfo.Size()>>20, "path", walPath)
	}

	callCtx, cancel := context.WithTimeout(ctx, checkpointDeadline)
	defer cancel()

	var passiveBusy, passiveLog, passiveDone int
	err := db.checkpointDB.QueryRowContext(callCtx, "PRAGMA wal_checkpoint(PASSIVE)").
		Scan(&passiveBusy, &passiveLog, &passiveDone)
	if err != nil {
		if isSQLiteContention(err) || isDeadlineExceeded(err) {
			slog.Warn("telemetry: WAL checkpoint(PASSIVE) skipped", "reason", err)
			return
		}
		slog.Warn("telemetry: WAL checkpoint(PASSIVE) error", "error", err)
		return
	}

	// Escalate to TRUNCATE only when WAL is still large and we haven't tried recently.
	if statErr != nil || walInfo.Size() <= walSizeEscalateBytes {
		return
	}
	if time.Since(db.lastTruncateAt) < walTruncateHoldoff {
		return
	}

	callCtx2, cancel2 := context.WithTimeout(ctx, checkpointDeadline)
	defer cancel2()

	var truncBusy, truncLog, truncDone int
	err = db.checkpointDB.QueryRowContext(callCtx2, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&truncBusy, &truncLog, &truncDone)
	if err != nil {
		if isSQLiteContention(err) || isDeadlineExceeded(err) {
			slog.Warn("telemetry: WAL checkpoint(TRUNCATE) skipped", "reason", err)
			return
		}
		slog.Warn("telemetry: WAL checkpoint(TRUNCATE) error", "error", err)
		return
	}
	if truncBusy > 0 {
		// Readers are still active; don't record as successful — retry next cycle.
		slog.Warn("telemetry: WAL checkpoint(TRUNCATE) partially blocked", "busy", truncBusy, "log", truncLog)
		return
	}
	db.lastTruncateAt = time.Now()
}

// runQuickCheck runs PRAGMA quick_check on the open database and returns an
// error if any row other than "ok" is returned, citing the DB path and the
// SQLite diagnostics. Kept on the writer connection so it shares the WAL view.
func runQuickCheck(db *sql.DB, dbPath string) error {
	rows, err := db.Query("PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("telemetry: quick_check query at %s: %w", dbPath, err)
	}
	defer func() { _ = rows.Close() }()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("telemetry: quick_check scan at %s: %w", dbPath, err)
		}
		if line != "ok" {
			lines = append(lines, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("telemetry: integrity check failed at %s: %w", dbPath, err)
	}
	if len(lines) > 0 {
		return fmt.Errorf("telemetry: integrity check failed at %s: %s", dbPath, strings.Join(lines, "; "))
	}
	return nil
}

func isSQLiteContention(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "SQLITE_LOCKED")
}

func isDeadlineExceeded(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "context canceled"))
}

// seedMeta inserts initial schema_meta rows on first open. INSERT OR IGNORE
// makes subsequent opens no-ops for each key.
func seedMeta(db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339)
	rows := []struct{ k, v string }{
		{"schema_version", "1"},
		{"jsonl_migrated", "false"},
		{"created_at", now},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO schema_meta(key,value) VALUES(?,?)`, r.k, r.v,
		); err != nil {
			return fmt.Errorf("telemetry: seed schema_meta %s: %w", r.k, err)
		}
	}
	return nil
}

func restrictFileACL(path string) error {
	if !isElevated() {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	serviceModify := windows.ACCESS_MASK(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_EXECUTE | windows.DELETE)
	return winacl.RestrictFile(path,
		winacl.Grant{SIDType: windows.WinLocalSystemSid, Permissions: windows.GENERIC_ALL},
		winacl.Grant{SIDType: windows.WinBuiltinAdministratorsSid, Permissions: windows.GENERIC_ALL},
		winacl.Grant{SIDType: windows.WinServiceSid, Permissions: serviceModify},
	)
}

func isElevated() bool {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid,
	); err != nil {
		return false
	}
	defer func() { _ = windows.FreeSid(sid) }()
	member, err := windows.Token(0).IsMember(sid)
	return err == nil && member
}
