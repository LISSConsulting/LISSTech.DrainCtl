# Quickstart — Telemetry Store Dev Loop

How to work on this feature locally. Assumes the existing DrainCtl dev environment (Go 1.26+, WiX 5, MinGW, .NET SDK 8+) from `BUILD.md`.

## 1. Branch & pull dependencies

```bash
git switch 007-sqlite-telemetry-store
go get modernc.org/sqlite@latest
go mod tidy
```

## 2. Build & run the service locally (unsigned)

```bash
just all                 # builds CLI + DLL + PS module + MSI, unsigned
```

On first run against a clean data dir the service will:
1. Create `%ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl.db` with ACLs matching `config.json`.
2. Skip the JSONL migration (no `audit.jsonl` present) and write `schema_meta[jsonl_migrated]=true`.
3. Start the aggregator + retention goroutines.

To exercise the migration path:
```powershell
# Drop a synthetic audit.jsonl in place before first service start
Copy-Item tests/fixtures/sample-audit.jsonl `
          "$env:ProgramData\LISS Technologies\LISSTech DrainCtl\audit.jsonl"
sc.exe start drainctl
```
Check the event log / file log for the `jsonl_migration` completion record and confirm `audit.jsonl.bak.<timestamp>` exists.

## 3. Run unit tests

```bash
go test ./internal/telemetry/...
go test ./internal/dashboard/...
go test ./...                 # everything
```

All `internal/telemetry` tests use `t.TempDir()` — nothing touches `%ProgramData%`.

## 4. Inspect the DB by hand

The Microsoft Store ships `sqlite3.exe` or download it from sqlite.org. On dev boxes:
```powershell
sqlite3 "$env:ProgramData\LISS Technologies\LISSTech DrainCtl\drainctl.db"
```
Useful queries:
```sql
-- how many audit events, and oldest/newest?
SELECT COUNT(*), datetime(MIN(ts)/1000, 'unixepoch'), datetime(MAX(ts)/1000, 'unixepoch') FROM audit;

-- how well is the aggregator keeping up?
SELECT name, outcome, datetime(finished_ts/1000, 'unixepoch'), rows_affected
  FROM maintenance_jobs ORDER BY name;

-- last 10 drain-mode changes for a host
SELECT datetime(ts/1000, 'unixepoch'), prev_state, new_state, changed_by, reconciliation
  FROM audit WHERE host = 'RDSH-07' ORDER BY ts DESC LIMIT 10;

-- storage per tier
SELECT 'raw', COUNT(*) FROM metrics_raw UNION ALL
SELECT '5min', COUNT(*) FROM metrics_5min UNION ALL
SELECT 'hourly', COUNT(*) FROM metrics_hourly;
```

**Important**: The service holds the DB open in WAL mode. Your interactive `sqlite3` session is a *reader* and will see committed data; it will not block the service. Do NOT open the DB with a writer tool while the service is running.

## 4b. WAL-aware backup procedure

The WAL (`drainctl.db-wal`) and shared-memory (`drainctl.db-shm`) sidecars are part of the live database. Copying only `drainctl.db` while the service is running will produce a backup that silently omits every commit still in the WAL. Two supported recipes — pick one based on whether you can take downtime.

**Recipe A — service-stopped checkpoint + copy** (one-shot, simplest, requires ~5 s downtime):
```powershell
$data   = "$env:ProgramData\LISS Technologies\LISSTech DrainCtl"
$backup = "$PWD\backup-$(Get-Date -Format yyyyMMdd-HHmmss)"
New-Item -ItemType Directory -Force -Path $backup | Out-Null

Stop-Service drainctl

# TRUNCATE checkpoint folds pending WAL frames into drainctl.db and empties the
# sidecar, so a straight file copy captures the complete state.
sqlite3 "$data\drainctl.db" "PRAGMA wal_checkpoint(TRUNCATE);"

# Copy the DB plus any sidecars that remain (-shm may still exist; -wal should
# be zero-sized after TRUNCATE). Copy the whole set to stay robust against
# checkpoint-skew edge cases.
Copy-Item "$data\drainctl.db"     $backup\
Copy-Item "$data\drainctl.db-wal" $backup\ -ErrorAction SilentlyContinue
Copy-Item "$data\drainctl.db-shm" $backup\ -ErrorAction SilentlyContinue

Start-Service drainctl

# Smoke test: open the backup read-only and verify it answers a basic query.
sqlite3 "$backup\drainctl.db" "SELECT COUNT(*) AS audit_rows FROM audit;"
```

**Recipe B — online backup API** (no downtime, preferred for production):
```powershell
$data   = "$env:ProgramData\LISS Technologies\LISSTech DrainCtl"
$backup = "$PWD\backup-$(Get-Date -Format yyyyMMdd-HHmmss).db"

# sqlite3.exe's `.backup` dot-command uses SQLite's online backup API. It
# snapshots the live database cross-WAL without stopping writers — pages are
# copied page-by-page while the service keeps committing.
sqlite3 "$data\drainctl.db" ".backup '$backup'"

# Smoke test: verify the backup opens and matches the live schema.
sqlite3 $backup "SELECT COUNT(*) AS audit_rows FROM audit;"
```

**Do NOT** robocopy just `drainctl.db` on its own — the live WAL can hold many seconds of commits that would be silently absent from the copy. Copy the full `drainctl.db*` set (Recipe A) or use the online backup API (Recipe B).

**Restore**: stop the service, replace `drainctl.db*` at the data dir with the backup file(s), start the service. Recipe B produces a single `.db` file; the service's first open reconstructs its own WAL/SHM.

## 4c. File size does not shrink after retention purges

`PRAGMA incremental_vacuum` (run by the retention worker) reclaims freed pages to SQLite's internal free-list. The **file size stays at the high-water mark** until a manual `VACUUM INTO` sweep. A retention purge that deletes 10M rows will NOT make `drainctl.db` smaller on disk — subsequent inserts reuse the freed pages. Stable file size is normal; don't interpret it as "retention isn't running". Check `maintenance_jobs` for actual retention activity.

## 5. Exercise the HTTP contracts

With the service running and your current Kerberos ticket valid:
```powershell
$h = 'RDSH-07'
$from = (Get-Date).AddHours(-1).ToUniversalTime().ToString('o')
$to   = (Get-Date).ToUniversalTime().ToString('o')
Invoke-RestMethod -UseDefaultCredentials `
  "https://localhost:8443/api/v1/metrics/$h?from=$from&to=$to&resolution=auto"

Invoke-RestMethod -UseDefaultCredentials `
  "https://localhost:8443/api/v1/maintenance/status"

Invoke-RestMethod -UseDefaultCredentials `
  "https://localhost:8443/api/v1/audit?host=$h&limit=20"
```

## 6. Frontend dev loop

```bash
cd frontend
npm install
npm run dev
```

The Vite dev server proxies `/api/v1/*` to the running DrainCtl service. Zoom/pan interactions should trigger `fetchMetrics` with new ranges — watch the Network tab.

## 7. Retention / aggregation fast-forward (manual testing)

Aggregators and retention run on timers. To test behaviour without waiting an hour:
- Stop the service.
- Edit `config.json` to set `telemetry.aggregator_interval_seconds = 5` and `telemetry.retention_interval_minutes = 1`.
- Restart the service.
- Use the `sqlite3` session above to watch `metrics_5min` and `metrics_hourly` populate.
- Revert the config before committing.

## 8. Regression checklist (FR-026)

Before declaring the feature done, confirm none of the existing surfaces regressed. This is the manual verification that backs FR-026 "no operator-visible regression":

- [ ] `drainctl history` CLI output column shape and ordering are unchanged vs. pre-feature (run `drainctl history --limit 20` against a seeded audit set; diff against a pre-feature golden if possible).
- [ ] Dashboard empty state (fresh install, zero samples) still renders the "Collecting data…" placeholder and not an error.
- [ ] Kerberos / negotiate auth flow still succeeds from a domain-joined browser; no new 401 regressions on `/api/v1/metrics` or `/api/v1/audit`.
- [ ] Host-add flow (registering a new RDSH) is unchanged — the host appears in `ServerState` and starts collecting metrics on its next sample tick.
- [ ] Notification triggers (drain on/off, session utilization) still fire correctly and reach their configured SMTP / webhook / ntfy targets.

## 9. Before committing

- Confirm git-derived CalVer `YY.MM.BUILD` with `just version` (see `CLAUDE.md`).
- `just resource` if you touched `.rc`.
- `just lint` (gofmt + go vet + golangci-lint).
- `prek run --all-files` (project pre-commit).
- Sanity: open the dashboard, verify the chart renders at the default 5-day view, zoom to a 1-hour window, and check the maintenance widget shows green for all jobs.
