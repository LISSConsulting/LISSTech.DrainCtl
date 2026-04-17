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
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	sqlite "modernc.org/sqlite"
)

const dbFileName = "drainctl.db"

// DB holds the writer and reader sql.DB pools for drainctl.db.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string
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

// pragmaConnector implements driver.Connector so that every connection obtained
// from either pool receives the full connectionPragmas block before use.
// modernc.org/sqlite's Driver implements driver.Driver but not DriverContext,
// so we call drv.Open(dsn) directly and apply pragmas on the resulting conn.
type pragmaConnector struct {
	dsn string
	drv driver.Driver
}

func (c *pragmaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if err := applyConnectionPragmas(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telemetry: connection pragmas: %w", err)
	}
	return conn, nil
}

func (c *pragmaConnector) Driver() driver.Driver { return c.drv }

func applyConnectionPragmas(ctx context.Context, conn driver.Conn) error {
	for _, p := range connectionPragmas {
		if err := execDriverConn(ctx, conn, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

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

	connector := &pragmaConnector{dsn: path, drv: &sqlite.Driver{}}

	writer := sql.OpenDB(connector)
	writer.SetMaxOpenConns(1)
	reader := sql.OpenDB(connector)

	if err := applySchema(writer); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("telemetry: apply schema: %w", err)
	}

	if err := seedMeta(writer); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, err
	}

	db := &DB{writer: writer, reader: reader, path: path}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		restrictFileACL(path + suffix)
	}
	return db, nil
}

// Close releases all database connections.
func (db *DB) Close() error {
	werr := db.writer.Close()
	rerr := db.reader.Close()
	if werr != nil {
		return werr
	}
	return rerr
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
