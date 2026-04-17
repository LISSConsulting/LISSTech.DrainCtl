# Implementation Plan: Unified SQLite Telemetry Store

**Branch**: `007-sqlite-telemetry-store` | **Date**: 2026-04-16 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/007-sqlite-telemetry-store/spec.md`

## Summary

Replace the parallel audit-JSONL + in-memory metrics ring with a single embedded SQLite database file (`drainctl.db`) in the existing data directory, opened in WAL mode by the service. All drain-mode audit events and all per-host metric samples land in that file. Background workers build a 5-minute downsample tier and an hourly aggregate tier so the dashboard can serve a 5-day chart window at useful resolution. Retention per record class purges old rows; a periodic `incremental_vacuum` returns space. The dashboard gains a maintenance-status widget so operators can see that background jobs are healthy. The legacy `audit.jsonl` is imported on first service start and renamed to `.bak`.

Technical approach in one sentence: pure-Go SQLite driver (`modernc.org/sqlite`), service is the single writer, CLI and dashboard read the same file concurrently via WAL, no new runtime dependencies.

## Technical Context

**Language/Version**: Go 1.26.2 (existing project). All new files `//go:build windows`.
**Primary Dependencies**: `modernc.org/sqlite` (pure-Go SQLite, no CGO, no MinGW at runtime). Existing deps (`cobra`, `alexbrainman/sspi`, `golang.org/x/sys`) unchanged.
**Storage**: Single SQLite file at `%ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl.db`, WAL mode. Companion `drainctl.db-wal` and `drainctl.db-shm` are managed by the engine.
**Testing**: `go test` against a temp-directory database per test. `just lint` (gofmt, go vet, golangci-lint) gates all commits. `prek` runs gitleaks.
**Target Platform**: Windows only. Service runs as `NT SERVICE\DrainCtl`. Dashboard hosted in-process via `net/http` with Kerberos SSO.
**Project Type**: Single Go module with a CLI (`cmd/drainctl`), a DLL shim (`cmd/cshared`), and an embedded dashboard (Svelte 5 + uPlot) served from `internal/dashboard`.
**Performance Goals**: 50 hosts × 6 counters × 15-second sampling sustained indefinitely (≈ 1.44M raw rows in the 25h retention window — matches data-model.md storage-footprint estimate). Dashboard 5-day chart render < 2 s on a cold LAN fetch. Zoom to 1-hour window < 500 ms re-render.
**Constraints**:
- No CGO in the shipped binaries (rules out all cgo SQLite drivers and libsql embedded).
- No system-level DB engine or extra runtime installer (rules out PostgreSQL/TimescaleDB, MySQL, sqld).
- Must coexist with the existing `config.json` named mutex without conflating the two locking mechanisms.
- Must preserve the existing dashboard Kerberos SSO / negotiate middleware semantics.
- CalVer `YY.DOY.patch` version must advance in all 7 places on every commit per CLAUDE.md.
**Scale/Scope**: 7 functional areas touched (audit write, metrics write, aggregation worker, retention worker, migration importer, dashboard HTTP, dashboard SPA); roughly a dozen new Go files; deletions across `internal/store/memstore.go`, `internal/dashboard/store.go`, and CLI/DLL history paths.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The project constitution at `.specify/memory/constitution.md` is the stock Spec-Kit template with unfilled placeholders (`[PRINCIPLE_1_NAME]`, etc.). There are therefore no ratified principles to gate this plan against. The plan proceeds without constitution-imposed constraints; the applicable guardrails are the project-level rules in `CLAUDE.md`:

- `//go:build windows` on every `.go` file — respected in every new file below.
- Version bump on every commit (CalVer) in the 7 canonical locations — captured as a task prerequisite.
- No viper — config stays in `config.json` via existing scoped updaters; new retention knobs fit the same file.
- Pre-commit (`prek`) must pass — plan budgets time for running `just lint` before each commit.
- `ClampRetention()` pattern — extended with separate audit vs. metrics clamps.
- Signing order — unchanged; the new binary changes do not alter the signing sequence in `just release`.

**Gate result**: Pass (no violations; no Complexity Tracking entries required).

## Project Structure

### Documentation (this feature)

```text
specs/007-sqlite-telemetry-store/
├── plan.md                # This file
├── research.md            # Phase 0 output
├── data-model.md          # Phase 1 output (schema DDL + indexes)
├── quickstart.md          # Phase 1 output (dev loop)
├── contracts/             # Phase 1 output
│   ├── http-metrics.md    # GET /api/v1/metrics/{host}
│   ├── http-audit.md      # GET /api/v1/audit (and deprecation of /history/{host})
│   └── http-maintenance.md# GET /api/v1/maintenance/status
├── checklists/
│   └── requirements.md    # Spec quality checklist (already present)
└── tasks.md               # Phase 2 output (/speckit-tasks, not created here)
```

### Source Code (repository root)

The existing layout is a single Go module rooted at the repo top with `cmd/` and `internal/` packages, a Svelte frontend at `frontend/`, and PowerShell / installer assets. New work lives primarily under `internal/telemetry/` (new package) with targeted edits to existing packages.

```text
drainctl/                                   # module root
├── audit.go                                # root pkg: type AuditRecord (MODIFY if schema needs new fields)
├── check.go / format.go                    # root pkg: CheckResult (UNCHANGED)
├── history.go                              # root pkg: GetHistory() — REPLACE body to hit SQLite store
├── config.go                               # root pkg: retention knobs — ADD metrics + audit retention fields
├── cmd/
│   ├── drainctl/                           # CLI — `drainctl history` path adjusted
│   └── cshared/                            # DLL — unaffected (only reads drain state)
├── internal/
│   ├── telemetry/                          # NEW package — the SQLite store
│   │   ├── db.go                           # Open/Close, PRAGMA setup, ACL enforcement
│   │   ├── schema.go                       # embedded DDL + migration (PRAGMA user_version)
│   │   ├── audit.go                        # AuditStore: Append, QueryRange
│   │   ├── metrics.go                      # MetricsStore: Append, QueryRange (raw / 5min / hourly)
│   │   ├── aggregator.go                   # 5-minute + hourly downsample workers
│   │   ├── retention.go                    # periodic DELETE + incremental_vacuum worker
│   │   ├── maintenance.go                  # JobStatusStore: last-run tracking for aggregator + retention
│   │   ├── reconcile.go                    # startup drift-detection → single reconciliation audit row
│   │   ├── migrate_jsonl.go                # one-shot import of legacy audit.jsonl → audit table
│   │   └── *_test.go
│   ├── store/
│   │   └── memstore.go                     # DELETE (MemAuditStore superseded by telemetry.AuditStore)
│   ├── dashboard/
│   │   ├── server.go                       # ADD /metrics, /audit, /maintenance handlers; deprecate /history
│   │   ├── store.go                        # REMOVE history ring; ServerState keeps only current snapshot
│   │   ├── client.go                       # adjust any report-ingest paths writing to telemetry
│   │   └── openapi.yaml                    # UPDATE: new endpoints + deprecated /history
│   └── svc/                                # service boot — wire telemetry.Open() before pipe + HTTP start
├── frontend/                               # Svelte 5 dashboard
│   ├── src/lib/api.js                      # add fetchMetrics(host, from, to, resolution)
│   ├── src/lib/chart.svelte                # uPlot: zoom/pan → requests matching resolution tier
│   ├── src/lib/maintenance-status.svelte   # NEW widget showing last-run for each job
│   └── src/routes/+page.svelte             # wire the new widget and chart resolution selector
├── audit_*.go                              # root pkg legacy audit helpers — DELETE or thin-shim after migration cut-over
└── ...                                     # unchanged: email, notify, sessions, perf, dpapi, etc.
```

**Structure Decision**: Keep the single-module layout. Introduce one new package (`internal/telemetry`) that owns the SQLite file; every other package talks to it through a small interface. The root package's legacy `audit.go` + `history.go` keep their public function signatures so external CLI and DLL callers do not break, but the implementations delegate to `telemetry`. `internal/store/memstore.go` and the history ring inside `internal/dashboard/store.go` are deleted outright (FR-025).

## Complexity Tracking

> Constitution check passed; no violations to justify.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none)    |            |                                     |
