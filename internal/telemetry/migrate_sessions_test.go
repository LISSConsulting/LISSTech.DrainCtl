//go:build windows

package telemetry

import (
	"database/sql"
	"strings"
	"testing"
)

var sessionSchemaTables = []string{
	"session_snapshots",
	"session_latest",
	"session_generation_fence",
	"session_action_outbox",
	"session_action_audit",
	"session_action_ledger",
}

var sessionSchemaIndexes = []string{
	"session_snapshots_last_success_received_idx",
	"session_snapshots_fleet_aggregates_idx",
	"session_latest_host_state_idx",
	"session_latest_state_host_idx",
	"session_action_outbox_idempotency_idx",
	"session_action_outbox_delivery_idx",
	"session_action_outbox_expiry_idx",
	"session_action_audit_action_idx",
	"session_action_audit_retention_idx",
	"session_action_ledger_cleanup_idx",
}

func TestSessionSchemaV4_FreshDBIsCanonical(t *testing.T) {
	db := openTestDB(t)
	assertSessionSchemaV4(t, db.writer)
}

func TestSessionSchemaV4_UpgradesV3Idempotently(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open current DB: %v", err)
	}
	for _, table := range []string{
		"session_latest",
		"session_snapshots",
		"session_generation_fence",
		"session_action_outbox",
		"session_action_audit",
		"session_action_ledger",
	} {
		if _, err := db.writer.Exec("DROP TABLE " + table); err != nil {
			_ = db.Close()
			t.Fatalf("drop v4 table %q: %v", table, err)
		}
	}
	if _, err := db.writer.Exec("PRAGMA user_version = 3"); err != nil {
		_ = db.Close()
		t.Fatalf("set v3 user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v3 DB: %v", err)
	}

	upgraded, err := Open(dir)
	if err != nil {
		t.Fatalf("Open v3 DB: %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	assertSessionSchemaV4(t, upgraded.writer)
	if got := queryPragmaInt(t, upgraded, "user_version"); got != schemaVersion {
		t.Errorf("upgraded user_version = %d, want %d", got, schemaVersion)
	}

	if err := applySchema(upgraded.writer); err != nil {
		t.Fatalf("rerun v4 migration: %v", err)
	}
	assertSessionSchemaV4(t, upgraded.writer)
}

func assertSessionSchemaV4(t *testing.T, db *sql.DB) {
	t.Helper()

	wantTables := make(map[string]struct{}, len(sessionSchemaTables))
	for _, table := range sessionSchemaTables {
		wantTables[table] = struct{}{}
		assertCanonicalTableDDL(t, db, table)
	}
	for _, table := range sessionTables(t, db) {
		if strings.Contains(table, "session") {
			if _, ok := wantTables[table]; !ok {
				t.Errorf("unexpected session table %q: schema v4 has no EAV or history table", table)
			}
		}
	}

	for _, index := range sessionSchemaIndexes {
		assertCanonicalIndexDDL(t, db, index)
	}
	assertSessionLatestForeignKey(t, db)
	for _, table := range []string{
		"session_snapshots",
		"session_generation_fence",
		"session_action_outbox",
		"session_action_audit",
		"session_action_ledger",
	} {
		rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
		if err != nil {
			t.Fatalf("foreign keys for %s: %v", table, err)
		}
		if rows.Next() {
			_ = rows.Close()
			t.Errorf("%s unexpectedly has a foreign key", table)
			continue
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close foreign key rows for %s: %v", table, err)
		}
	}
}

func assertCanonicalTableDDL(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&got); err != nil {
		t.Fatalf("read DDL for table %q: %v", table, err)
	}
	want := sessionDDLStatement(t, "create table if not exists "+table)
	if normalizedSQL(got) != normalizedSQL(want) {
		t.Errorf("table %s DDL differs from canonical schema\n got: %s\nwant: %s", table, got, want)
	}

	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("columns for %s: %v", table, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close columns for %s: %v", table, err)
		}
	}()
	var columnCount int
	for rows.Next() {
		columnCount++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns for %s: %v", table, err)
	}
	if columnCount == 0 {
		t.Errorf("table %s has no inspectable columns", table)
	}
}

func assertCanonicalIndexDDL(t *testing.T, db *sql.DB, index string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&got); err != nil {
		t.Fatalf("read DDL for index %q: %v", index, err)
	}
	prefix := "create index if not exists " + index
	if index == "session_action_outbox_idempotency_idx" {
		prefix = "create unique index if not exists " + index
	}
	want := sessionDDLStatement(t, prefix)
	if normalizedSQL(got) != normalizedSQL(want) {
		t.Errorf("index %s DDL differs from canonical schema\n got: %s\nwant: %s", index, got, want)
	}
}

func assertSessionLatestForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_list(session_latest)")
	if err != nil {
		t.Fatalf("session_latest foreign keys: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close session_latest foreign keys: %v", err)
		}
	}()
	var id, seq int
	var table, from, to, onUpdate, onDelete, match string
	if !rows.Next() {
		t.Fatal("session_latest missing foreign key")
	}
	if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
		t.Fatalf("scan session_latest foreign key: %v", err)
	}
	if table != "session_snapshots" || from != "canonical_host" || to != "canonical_host" || onDelete != "CASCADE" {
		t.Errorf("session_latest foreign key = %s(%s) -> %s(%s) ON DELETE %s, want session_snapshots(canonical_host) -> canonical_host ON DELETE CASCADE", table, from, table, to, onDelete)
	}
	if rows.Next() {
		t.Error("session_latest has more than one foreign key")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session_latest foreign keys: %v", err)
	}
}

func sessionTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close tables: %v", err)
		}
	}()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return tables
}

func sessionDDLStatement(t *testing.T, prefix string) string {
	t.Helper()
	for _, statement := range splitStatements(sessionSchemaV4DDL) {
		if strings.HasPrefix(strings.ToLower(statement), prefix) {
			return statement
		}
	}
	t.Fatalf("canonical session DDL statement %q not found", prefix)
	return ""
}

func normalizedSQL(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "if not exists", "")
	return strings.Join(strings.Fields(s), "")
}
