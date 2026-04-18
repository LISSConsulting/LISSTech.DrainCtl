//go:build windows

package telemetry

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	auditJSONLFilename  = "audit.jsonl"
	migrationChunkLines = 10000
	migrationScanBufMax = 1 << 20 // 1 MiB — legacy audit lines with perf+session fields
)

// MigrationResult reports the outcome of a single MigrateJSONL invocation.
type MigrationResult struct {
	JSONLFound bool // audit.jsonl existed at migration time
	LineCount  int  // total source lines consumed this call (cumulative across resumes not reflected here)
	Imported   int  // rows submitted via INSERT … ON CONFLICT this call (conflict no-ops still count)
	Skipped    int  // malformed lines observed cumulatively (loaded from schema_meta + this call)
	Duration   time.Duration
}

// legacyAuditRecord is the JSONL-on-disk shape produced by pre-007 code
// (drainctl.AuditRecord). Redeclared here because internal/telemetry cannot
// import the root drainctl package (cyclic). Only the fields migration
// consumes are listed; unknown JSON fields are silently discarded by
// encoding/json, which preserves forward compatibility.
type legacyAuditRecord struct {
	Timestamp      time.Time `json:"ts"`
	Host           string    `json:"host"`
	DrainMode      int       `json:"mode"`
	KeyModified    time.Time `json:"key_modified,omitempty"`
	Changed        bool      `json:"changed,omitempty"`
	ChangedBy      string    `json:"changed_by,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Reconciliation bool      `json:"reconciliation,omitempty"`
}

// MigrateJSONL streams the legacy audit.jsonl into the SQLite audit table.
// Idempotent and resumable: progress is checkpointed after every
// migrationChunkLines source lines via schema_meta[jsonl_migrated_line_count],
// and every INSERT uses ON CONFLICT(ts, host, new_state) DO NOTHING so a
// retry never duplicates rows (FR-021).
//
// Early-exit cases:
//
//	(1) schema_meta[jsonl_migrated] == "true" → already done, return zero result.
//	(2) audit.jsonl absent → write schema_meta[jsonl_migrated]="true" and return
//	    (nothing to rename, no T043 follow-up needed).
//
// Streaming case: re-opens the JSONL from the start, skips to
// schema_meta[jsonl_migrated_line_count] (cheap since scanning advances by
// newlines), and commits every migrationChunkLines lines as one transaction
// that INSERTs the accumulated transition rows AND updates the checkpoint
// keys. A crash mid-chunk leaves the previous checkpoint intact; the next
// service start resumes from there.
//
// Only transition records (legacyAuditRecord.Changed == true) are inserted.
// The new audit schema stores one row per drain-mode transition
// (data-model.md). Non-transition observations still update the per-host
// state tracker so PrevState can be derived for subsequent transitions. On
// resume the tracker is seeded from the newest audit row per host, so
// transitions that land after a restart still get an accurate PrevState.
//
// In the streaming case, MigrateJSONL does NOT flip schema_meta[jsonl_migrated]
// to "true" on EOF. That flip is atomic with the audit.jsonl → audit.jsonl.bak
// rename owned by T043: if the flag were set here and T043 then crashed
// mid-rename, a second start would see flag=true with audit.jsonl still
// present, and the legacy CLI path could keep appending records that nothing
// would ever migrate. Keeping the flag-set co-located with the rename in
// T043 preserves the invariant "flag=true ⇒ audit.jsonl has been renamed."
func MigrateJSONL(ctx context.Context, db *DB, dataDir string) (MigrationResult, error) {
	started := time.Now()
	res := MigrationResult{}

	migrated, err := readSchemaMeta(ctx, db.writer, "jsonl_migrated")
	if err != nil {
		return res, fmt.Errorf("telemetry: migrate read marker: %w", err)
	}
	if migrated == "true" {
		return res, nil
	}

	jsonlPath := filepath.Join(dataDir, auditJSONLFilename)
	if _, statErr := os.Stat(jsonlPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			if _, err := db.writer.ExecContext(ctx,
				`INSERT INTO schema_meta(key, value) VALUES('jsonl_migrated', 'true')
                 ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
				return res, fmt.Errorf("telemetry: migrate mark absent: %w", err)
			}
			res.Duration = time.Since(started)
			return res, nil
		}
		return res, fmt.Errorf("telemetry: migrate stat %s: %w", jsonlPath, statErr)
	}
	res.JSONLFound = true

	lineOffset, err := readSchemaMetaInt(ctx, db.writer, "jsonl_migrated_line_count")
	if err != nil {
		return res, fmt.Errorf("telemetry: migrate read line_count: %w", err)
	}
	priorSkipped, err := readSchemaMetaInt(ctx, db.writer, "jsonl_migrate_skipped")
	if err != nil {
		return res, fmt.Errorf("telemetry: migrate read skipped: %w", err)
	}
	res.Skipped = priorSkipped

	// Seed the per-host state tracker from the audit table so PrevState
	// reconstruction survives a mid-migration restart. On a fresh migration
	// (lineOffset == 0) the map starts empty — the first record parsed for
	// each host seeds its own baseline via the DrainMode field.
	lastState := make(map[string]int)
	if lineOffset > 0 {
		baseline, berr := latestNewStatePerHost(ctx, db.writer)
		if berr != nil {
			return res, fmt.Errorf("telemetry: migrate baseline: %w", berr)
		}
		for host, st := range baseline {
			lastState[host] = st
		}
	}

	f, err := os.Open(jsonlPath)
	if err != nil {
		return res, fmt.Errorf("telemetry: migrate open %s: %w", jsonlPath, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), migrationScanBufMax)

	chunk := make([]AuditRecord, 0, 256)
	lineIndex := 0
	linesSinceFlush := 0

	flush := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := db.writer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("telemetry: migrate begin tx: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		if len(chunk) > 0 {
			stmt, perr := tx.PrepareContext(ctx, `INSERT INTO audit
                (ts, host, prev_state, new_state, principal, changed_by, reason,
                 key_modified_ts, reconciliation, before_ts)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(ts, host, new_state) DO NOTHING`)
			if perr != nil {
				return fmt.Errorf("telemetry: migrate prepare insert: %w", perr)
			}
			for _, r := range chunk {
				var keyMod sql.NullInt64
				if r.KeyModifiedTs != nil {
					keyMod = sql.NullInt64{Int64: r.KeyModifiedTs.UTC().UnixMilli(), Valid: true}
				}
				reconc := 0
				if r.Reconciliation {
					reconc = 1
				}
				if _, err := stmt.ExecContext(ctx,
					r.Ts.UTC().UnixMilli(), r.Host, r.PrevState, r.NewState,
					r.Principal, r.ChangedBy, r.Reason, keyMod, reconc, nil,
				); err != nil {
					_ = stmt.Close()
					return fmt.Errorf("telemetry: migrate insert: %w", err)
				}
			}
			if err := stmt.Close(); err != nil {
				return fmt.Errorf("telemetry: migrate close stmt: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_meta(key, value) VALUES('jsonl_migrated_line_count', ?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(lineIndex)); err != nil {
			return fmt.Errorf("telemetry: migrate update line_count: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_meta(key, value) VALUES('jsonl_migrate_skipped', ?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(res.Skipped)); err != nil {
			return fmt.Errorf("telemetry: migrate update skipped: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("telemetry: migrate commit: %w", err)
		}
		committed = true
		res.Imported += len(chunk)
		chunk = chunk[:0]
		linesSinceFlush = 0
		return nil
	}

	for scanner.Scan() {
		lineIndex++
		if lineIndex <= lineOffset {
			continue
		}
		linesSinceFlush++

		line := scanner.Bytes()
		if len(line) > 0 {
			var leg legacyAuditRecord
			if err := json.Unmarshal(line, &leg); err != nil {
				res.Skipped++
				slog.Debug("telemetry: migrate skip malformed line",
					"line", lineIndex, "error", err)
			} else {
				prev := lastState[leg.Host]
				lastState[leg.Host] = leg.DrainMode
				if leg.Changed {
					rec := AuditRecord{
						Ts:             leg.Timestamp.UTC(),
						Host:           leg.Host,
						PrevState:      prev,
						NewState:       leg.DrainMode,
						Principal:      leg.ChangedBy,
						ChangedBy:      leg.ChangedBy,
						Reason:         leg.Reason,
						Reconciliation: leg.Reconciliation,
					}
					if !leg.KeyModified.IsZero() {
						km := leg.KeyModified.UTC()
						rec.KeyModifiedTs = &km
					}
					chunk = append(chunk, rec)
				}
			}
		}

		if linesSinceFlush >= migrationChunkLines {
			if err := flush(); err != nil {
				return res, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("telemetry: migrate scan: %w", err)
	}

	if linesSinceFlush > 0 {
		if err := flush(); err != nil {
			return res, err
		}
	}

	res.LineCount = lineIndex
	res.Duration = time.Since(started)
	return res, nil
}

// readSchemaMeta returns the value for key, or an empty string when the row
// is absent. Absence is not an error — callers that need a default translate
// the empty string as they see fit.
func readSchemaMeta(ctx context.Context, db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// readSchemaMetaInt returns the integer-encoded value for key, or 0 when the
// row is absent or the stored value cannot be parsed. "Corrupt value → 0" is
// deliberate: migration then restarts from the top, and ON CONFLICT DO NOTHING
// keeps the eventual state identical to a clean run.
func readSchemaMetaInt(ctx context.Context, db *sql.DB, key string) (int, error) {
	v, err := readSchemaMeta(ctx, db, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	// Corrupt value → 0 is deliberate: migration restarts from the top, and
	// ON CONFLICT DO NOTHING keeps the final state identical to a clean run.
	if n, parseErr := strconv.Atoi(v); parseErr == nil {
		return n, nil
	}
	return 0, nil
}

// latestNewStatePerHost returns, for each host that has at least one audit
// row, the NewState of its newest row (breaking ties on new_state DESC to
// match the PK ordering used elsewhere). Used by MigrateJSONL to seed the
// per-host state tracker on resume so PrevState stays accurate across
// restarts without re-reading the whole JSONL.
func latestNewStatePerHost(ctx context.Context, db *sql.DB) (map[string]int, error) {
	const q = `
        WITH latest AS (
            SELECT host, MAX(ts) AS ts FROM audit GROUP BY host
        )
        SELECT a.host, MAX(a.new_state)
        FROM audit a
        JOIN latest l ON a.host = l.host AND a.ts = l.ts
        GROUP BY a.host`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var host string
		var state int
		if err := rows.Scan(&host, &state); err != nil {
			return nil, err
		}
		out[host] = state
	}
	return out, rows.Err()
}
