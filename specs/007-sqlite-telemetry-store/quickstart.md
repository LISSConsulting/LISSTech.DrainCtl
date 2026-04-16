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

## 8. Before committing

- Bump CalVer `YY.DOY.patch` in all 7 places (see `CLAUDE.md`).
- `just resource` if you touched `.rc`.
- `just lint` (gofmt + go vet + golangci-lint).
- `prek run --all-files` (project pre-commit).
- Sanity: open the dashboard, verify the chart renders at the default 5-day view, zoom to a 1-hour window, and check the maintenance widget shows green for all jobs.
