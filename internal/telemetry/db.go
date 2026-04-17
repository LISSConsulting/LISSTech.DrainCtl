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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
)

// DB holds the writer, reader, and checkpoint sql.DB pools for drainctl.db.
type DB struct {
	writer         *sql.DB
	reader         *sql.DB
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
	"PRAGMA cache_size = -20480",
	"PRAGMA temp_store = MEMORY",
	"PRAGMA mmap_size = 67108864",
	"PRAGMA auto_vacuum = INCREMENTAL",
}

// checkpointPragmas is the same as connectionPragmas except busy_timeout=0 so
// the checkpoint connection never blocks writers when the WAL is contended.
var checkpointPragmas = []string{
	"PRAGMA journal_mode = WAL",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA foreign_keys = ON",
	"PRAGMA busy_timeout = 0",
	"PRAGMA temp_store = MEMORY",
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

// Open opens or creates drainctl.db in dataDir, applies the schema and pragmas,
// seeds initial schema_meta rows, and restricts file ACLs.
func Open(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, dbFileName)
	drv := &sqlite.Driver{}

	connector := &pragmaConnector{dsn: path, drv: drv}
	writer := sql.OpenDB(connector)
	writer.SetMaxOpenConns(1)

	reader := sql.OpenDB(connector)
	reader.SetConnMaxLifetime(readerMaxLifetime)

	checkpointConnector := &pragmaConnector{dsn: path, drv: drv, pragmas: checkpointPragmas}
	checkpointDB := sql.OpenDB(checkpointConnector)
	checkpointDB.SetMaxOpenConns(1)

	if err := applySchema(writer); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		_ = checkpointDB.Close()
		return nil, fmt.Errorf("telemetry: apply schema: %w", err)
	}

	if err := seedMeta(writer); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		_ = checkpointDB.Close()
		return nil, err
	}

	db := &DB{writer: writer, reader: reader, checkpointDB: checkpointDB, path: path}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		restrictFileACL(path + suffix)
	}
	return db, nil
}

// Close releases all database connections.
func (db *DB) Close() error {
	cerr := db.checkpointDB.Close()
	werr := db.writer.Close()
	rerr := db.reader.Close()
	if cerr != nil {
		return cerr
	}
	if werr != nil {
		return werr
	}
	return rerr
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

func restrictFileACL(path string) {
	if !isElevated() {
		return
	}
	cmds := [][]string{
		{"icacls", path, "/inheritance:r"},
		{"icacls", path, "/grant", "SYSTEM:(F)"},
		{"icacls", path, "/grant", "*S-1-5-32-544:(F)"},
		{"icacls", path, "/grant", "*S-1-5-6:(M)"},
	}
	for _, args := range cmds {
		_ = exec.Command(args[0], args[1:]...).Run()
	}
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
