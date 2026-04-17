---

description: "Task list for feature 007-sqlite-telemetry-store"
---

# Tasks: Unified SQLite Telemetry Store

**Input**: Design documents from `/specs/007-sqlite-telemetry-store/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/ (all present)

**Tests**: Included — `research.md` §14 defines a test strategy (unit, integration, HTTP contract). Test tasks are scoped per user story and run alongside implementation (not strict TDD ordering).

**Organization**: Tasks are grouped by user story. Each story maps to a functional MVP increment that can be merged and demoed on its own.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3, US4, US5)
- All paths are relative to the repository root (`drainctl/`)

## Path Conventions

- Go source: new package `internal/telemetry/`; modifications to root `*.go`, `internal/dashboard/`, `internal/svc/`
- Frontend: `frontend/src/lib/` and `frontend/src/routes/`
- Tests live next to code (`*_test.go`) — existing project convention

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Pull dependencies and scaffold the new package.

- [x] T001 Add `modernc.org/sqlite` to `go.mod` (`go get modernc.org/sqlite@latest && go mod tidy`); commit `go.mod` and `go.sum`
- [x] T002 [P] Create `internal/telemetry/` directory with empty stub files (each with `//go:build windows` header and `package telemetry`): `db.go`, `schema.go`, `audit.go`, `metrics.go`, `aggregator.go`, `retention.go`, `maintenance.go`, `reconcile.go`, `migrate_jsonl.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The telemetry engine itself — nothing in any user-story phase can run until the DB opens, applies the schema, and integrates with the service boot path.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T003 Embed DDL from `data-model.md` as a string constant in `internal/telemetry/schema.go`; add `applySchema(db *sql.DB)` that runs it inside a transaction and sets `PRAGMA user_version = 1`
- [x] T004 Implement `Open(dataDir string) (*DB, error)` and `Close()` in `internal/telemetry/db.go`: open `drainctl.db`, apply every pragma from `research.md` §2 via a `driver.Connector` whose `Connect()` issues the full PRAGMA block (so every pooled connection — reader and writer — has them); pin the writer `*sql.DB` to `SetMaxOpenConns(1)`; readers use a separate `*sql.DB` with the same connector; call `applySchema`, seed `schema_meta[created_at]`, and apply ACLs to `drainctl.db`, `drainctl.db-wal`, `drainctl.db-shm` using the existing config.json ACL helper (extract or reuse from `config.go`). Include a package-level doc comment stating the named mutex for `config.json` is separate from SQLite's WAL locking (FR-027).
- [x] T005 [P] Extend `Config` in `config.go` with `RetentionConfig{MetricsDays, AuditDays}` and `TelemetryConfig{AggregatorIntervalSeconds, RetentionIntervalMinutes}`; set defaults (metrics=30, audit=365, aggregator=60, retention=15) in the constructor; preserve JSON field ordering
- [x] T006 [P] Extend `ClampRetention()` in `config.go` to clamp `Retention.MetricsDays` to 1..365 and `Retention.AuditDays` to 1..3650; update existing callers if the function signature changes
- [x] T007 Wire `telemetry.Open()` into service boot in `internal/svc/` (wherever `MemAuditStore` is opened today): open telemetry before the named pipe listener and before the HTTP server start; close on shutdown; surface open errors as a fatal service-start failure
- [x] T008 [P] Unit test: `internal/telemetry/db_test.go` — `TestOpen_AppliesPragmas` confirms `journal_mode=wal`, `synchronous=1`, `foreign_keys=1`, `user_version=1` after Open; `TestOpen_IsIdempotent` opens twice against the same path; `TestOpen_AppliesACL` verifies the resulting file ACL matches the current config.json (parity check, not absolute SID validation); `TestOpen_PragmasAppliedToEveryConnection` — open the reader `*sql.DB`, use `db.Conn(ctx)` to acquire two distinct `*sql.Conn` handles explicitly (keep both open simultaneously), then on each run `PRAGMA foreign_keys`, `PRAGMA busy_timeout`, `PRAGMA temp_store` and assert the expected values from research.md §2. Do NOT assert on `journal_mode` (database-level/persistent, would pass even with the bug); the test must check connection-local pragmas that would diverge if only the first pooled connection received the block.
- [x] T009 [P] Unit test: `internal/telemetry/schema_test.go` — schema applies cleanly against a fresh DB and against a DB already at `user_version=1` (no-op)
- [x] T004a WAL checkpoint policy: a dedicated `*sql.Conn` (separate from writer and reader pools) with `busy_timeout=0` applied at `Connect` time is used by the retention goroutine. On each cycle run `PRAGMA wal_checkpoint(PASSIVE)` first (non-blocking, handles most of the work); only escalate to `wal_checkpoint(TRUNCATE)` when the WAL still exceeds 16 MB. Wrap each call in a `context.Context` with 5s deadline. On `SQLITE_BUSY` / `SQLITE_LOCKED` / deadline-exceeded: log WARN and skip the cycle (never retry inside the same call; never block writers or readers). Reader side: any read transaction older than 60s is cancelled with a logged notice so it cannot pin the WAL snapshot indefinitely. Log WARN when WAL file size > 64 MB, repeat escalation hourly.
- [x] T004b PRAGMA `integrity_check quick_check` at every service startup (not just first-install); run once after pragmas are applied and before the service begins ingest; on non-ok result abort service start with a clear error citing the DB path and the SQLite diagnostic. On clean start the check adds well under 1s at target volumes. Add test `TestOpen_CorruptFileReturnsClearError` (plant a fixture DB with a corrupted page, assert Open returns an error whose message contains the DB path and the SQLite diagnostic).
- [x] T004c Detect non-local data dir on Open() via `GetDriveTypeW` / `DRIVE_FIXED` check; refuse start with an operator-actionable error naming the path and the policy (FR-004) if the volume is not DRIVE_FIXED. Tests `TestOpen_RefusesNetworkShare` AND `TestOpen_AllowsLocalFixedDrive`.

**Checkpoint**: Service starts, `drainctl.db` appears in the data dir, every table from `data-model.md` exists and is empty, pragmas are set, ACLs match config.json.

---

## Phase 3: User Story 1 — Five-Day Metrics History Survives Restart (Priority: P1) 🎯 MVP

**Goal**: Service persists metric samples per host; dashboard renders a durable chart that survives service restart. Ships with raw + hourly tiers only; zoom tier switching (US3) comes later.

**Independent Test**: Run the service for two days with a synthetic metrics source, restart it, then open the dashboard — the chart must show pre-restart samples as a continuous line without gap (modulo the restart window itself).

### Implementation

- [x] T010 [P] [US1] Implement `MetricsStore.Append(ctx, samples []Sample)` in `internal/telemetry/metrics.go` with a batched `INSERT INTO metrics_raw` (single transaction per call)
- [x] T011 [P] [US1] Implement `MetricsStore.QueryRange(ctx, host, from, to time.Time, tier Tier, counters []string) (*Series, error)` in `internal/telemetry/metrics.go`; support `TierRaw` and `TierHourly` in this phase (5-min added in US3)
- [x] T012 [US1] Implement hourly aggregator goroutine in `internal/telemetry/aggregator.go`: every `cfg.Telemetry.AggregatorIntervalSeconds` wake, roll `metrics_raw` into `metrics_hourly` using `INSERT … ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET avg_value=excluded.avg_value, min_value=excluded.min_value, max_value=excluded.max_value, sample_count=excluded.sample_count`; applies a 5-minute watermark; on every tick unconditionally recomputes every eligible hourly bucket (end + watermark ≤ now AND source rows still present in `metrics_5min`); no wall-clock minute=0 gate. Bucket floors computed via `time.UTC` with `Truncate(time.Hour)` so DST transitions do not corrupt boundaries. Stop-channel for graceful shutdown.
- [x] T013 [US1] Wire metrics ingest in `internal/dashboard/server.go` `handleReport`: write incoming `CheckResult` counters into `telemetry.MetricsStore.Append` (alongside the existing SSE broadcast); if telemetry is nil (degraded mode), log-and-skip
- [x] T014 [US1] Remove the `history map[string][]dc.CheckResult` ring and all references in `internal/dashboard/store.go`; keep `ServerState` for current-snapshot only (`LastResult`, `LastSeen`). **In the same commit**, swap the existing `GET /api/v1/history/{host}` handler in `internal/dashboard/server.go` to return HTTP 410 Gone with body `{"error":"use /api/v1/metrics/{host} or /api/v1/audit"}`, since the handler's data source no longer exists. Prevents a broken intermediate release where the route has no backing store (analysis finding I1)
- [x] T015 [US1] Implement `GET /api/v1/metrics/{host}` handler in `internal/dashboard/server.go` per `contracts/http-metrics.md`; tier=`auto` falls back to `hourly` in this phase; empty-state response shape matches contract (FR-019a)
- [x] T016 [US1] Add `fetchMetrics(host, from, to, resolution, counters)` in `frontend/src/lib/api.js` returning the parsed contract shape
- [x] T017 [US1] Replace the current history-ring chart source in `frontend/src/lib/` chart component with a `fetchMetrics` call defaulting to a 5-day window at `resolution=auto`; render "Collecting data…" status when the server returns an empty series map (FR-019a)
- [x] T018 [P] [US1] Unit tests in `internal/telemetry/metrics_test.go`: `TestAppend_BatchRoundTrips`, `TestQueryRange_RawTier`, `TestQueryRange_HourlyTier_FallsBackWhenRawPurged`, `TestAppend_ConcurrentSafety`, `TestAppend_DiskFullIsGracefullyHandled` (injected writer error), `TestAppend_FutureTimestampStoresAndLogs` (clock-skew tolerance), `TestNewCounterRoundTripsThroughAllTiersAndHTTP` (ingest a previously-unknown counter name, verify it materializes to 5min and hourly, renders through /api/v1/metrics with no schema change)
- [x] T019 [P] [US1] Unit tests in `internal/telemetry/aggregator_test.go`: `TestHourlyAggregator_IsIdempotent`, `TestHourlyAggregator_SkipsIncompleteHours`, `TestHourlyAggregator_RecordsMaintenanceRow`, `TestAggregator_LateSampleUpdatesBucket` (insert raw sample, run aggregator → bucket materialized; insert a NEW raw sample within the same bucket but before watermark closes; rerun aggregator; assert bucket row reflects both samples), `TestAggregator_EmitsLogsAndMaintenanceRowPerRun` (asserts ≥1 ETW + file log record per run matches the `maintenance_jobs` row — covers FR-032)
- [x] T020 [P] [US1] HTTP test in `internal/dashboard/server_test.go`: `TestMetricsHandler_ContractShape`, `TestMetricsHandler_EmptyStateReturns200`, `TestMetricsHandler_InvalidRangeReturns400`, `TestMetricsHandler_UnknownHostReturns404`, `TestHistoryHandler_Returns410AfterRingRemoval` (asserts the legacy route returns 410 with the documented body — gates the I1 fix)
- [x] T021 [US1] Update `internal/dashboard/openapi.yaml`: add `/api/v1/metrics/{host}` path matching `contracts/http-metrics.md`

**Checkpoint**: Service restarts do not lose metrics. Dashboard renders a 5-day chart from the hourly tier. Raw-tier data visible for queries inside the last ~25 hours. Zoom switching not yet available — always hourly for long windows.

---

## Phase 4: User Story 2 — Audit Search Across Historical Drain Events (Priority: P1)

**Goal**: Audit records persist durably in the SQL store, can be queried by host/actor/time range, and the service emits a single reconciliation row on startup when the registry drain state diverged from the last audit while the service was down.

**Independent Test**: Drive drain-mode changes across hosts (including a service restart), query the audit API for a time window spanning the restart, and confirm all events are present plus one reconciliation row per host that changed while the service was down.

### Implementation

- [x] T022 [P] [US2] Implement `AuditStore.Append(ctx, rec AuditRecord) error` in `internal/telemetry/audit.go`; immutable — no Update or Delete methods exposed (FR-001b); `AuditStore` holds a dedicated `*sql.Conn` with `PRAGMA synchronous=FULL` applied at acquisition; all `Append` calls route through that connection so audit commits are durable against OS crash / power loss even though metrics writes stay at `synchronous=NORMAL`
- [x] T023 [P] [US2] Implement `AuditStore.QueryRange(ctx, filter QueryFilter) (records []AuditRecord, nextCursor string, err error)` in `internal/telemetry/audit.go` with cursor-based pagination keyed on the full primary key declared in data-model.md (`PRIMARY KEY (ts, host, new_state)`). Ordering is all-descending so the tuple-comparison seek predicate matches the sort: `ORDER BY ts DESC, host DESC, new_state DESC`. Cursor payload carries the `(ts, host, new_state)` triple of the last row returned; next-page query uses `WHERE (ts, host, new_state) < (:cursor_ts, :cursor_host, :cursor_new_state)` — SQLite's row-value comparison is lexicographic-ascending, so under all-DESC ordering this predicate correctly advances to the next row. (Equivalent explicit form for reader reference: `WHERE ts < :cursor_ts OR (ts = :cursor_ts AND host < :cursor_host) OR (ts = :cursor_ts AND host = :cursor_host AND new_state < :cursor_new_state)`.) If the PK in data-model.md ever changes, this task must change in lockstep.
- [x] T024 [US2] Implement `AuditStore.LatestByHost(ctx) (map[string]AuditRecord, error)` in `internal/telemetry/audit.go` (one query using a window function or a correlated subquery; keep it O(hosts))
- [ ] T025 [US2] Implement startup drift reconciliation in `internal/telemetry/reconcile.go`: compare current registry drain state (via existing `registry.go` helpers) against `AuditStore.LatestByHost`; for each divergence, write a single reconciliation row (`reconciliation=1`, `principal=reconciliationPrincipal` constant declared in `reconcile.go` with value `""` — both writer and test reference this constant so the partial-index `audit_principal WHERE principal <> ''` semantics stay load-bearing, `reason="service-downtime drift: last-known X, observed Y"`, `before_ts`=last-known timestamp); also emit reconciliation when registry LastWriteTime > last_audit.ts even if states match (oscillation case — A→B→A drift is invisible from state comparison but visible from key_modified_ts); write one `maintenance_jobs` row name="drift_reconciliation" capturing started/finished/duration/outcome/rows_affected (per-job count of reconciliation rows inserted); invoked from service boot after `telemetry.Open()` AND after `MigrateJSONL` has completed (so `LatestByHost` sees imported rows per FR-020), and before live ingest starts
- [ ] T026 [US2] Wire audit writes through telemetry: replace `MemAuditStore.Append` calls in the watcher/service path with `telemetry.AuditStore.Append`; keep the existing `dc.AuditRecord` public type (map to/from internal struct)
- [ ] T027 [US2] Implement `GET /api/v1/audit` handler in `internal/dashboard/server.go` per `contracts/http-audit.md`; respects `changes_only=true` default; emits `reconciliation` and `before_ts` fields
- [ ] T028 [US2] Replace body of `history.go#GetHistory` (root package, the public CLI entrypoint) with a call into `telemetry.AuditStore.QueryRange`; keep the public signature unchanged
- [ ] T028a [US2] CLI read-only WAL sidecar handling: open `drainctl.db` with SQLite URI `?mode=ro&_txlock=deferred` (do NOT force `_journal_mode` — inherit the file's persisted mode). Behavior cases: (a) `-wal` and `-shm` present and readable — normal WAL read open; (b) `-wal`/`-shm` missing — SQLite opens the main file in rollback-journal read mode automatically; CLI proceeds but logs at INFO that live writer data may be ≤1 commit stale; (c) `-wal` present but CLI user cannot open it (ACL) — open fails with a clear error citing the sidecar path and required permissions; (d) `-wal` exists but `-shm` is stale/corrupt — explicit failure, no silent fallback. Tests: `TestCLIReadOnly_SidecarsPresent`, `TestCLIReadOnly_SidecarsMissing`, `TestCLIReadOnly_SidecarUnreadableAclError`, `TestCLIReadOnly_SidecarCorruptError`.
- [ ] T029 [US2] Verify `GET /api/v1/history/{host}` still returns 410 Gone after the audit store goes live (it was first flipped to 410 in T014 under US1); keep the route registered for one release then remove in a follow-up. No handler change expected here unless the T014 swap regressed
- [ ] T030 [US2] Update `drainctl history` CLI output formatting in `cmd/drainctl/` to display reconciliation rows with a distinguishing prefix (e.g. `[DRIFT]`) so operators notice them
- [ ] T031 [P] [US2] Unit tests in `internal/telemetry/audit_test.go`: `TestAppend_RoundTrip`, `TestAppend_DuplicateKeyIgnored`, `TestQueryRange_OrdersDesc`, `TestQueryRange_CursorPaginates`, `TestQueryRange_FilterByActor`, `TestLatestByHost_AllHosts`, `TestAudit_DedicatedConnectionHasSynchronousFull` (acquire the AuditStore's connection, `PRAGMA synchronous` returns 2 = FULL), `TestAppend_DiskFullIsGracefullyHandled`
- [ ] T032 [P] [US2] Unit tests in `internal/telemetry/reconcile_test.go`: `TestReconcile_NoDriftWritesNothing`, `TestReconcile_DriftWritesOneRow`, `TestReconcile_EmptyAuditTreatsCurrentAsKnown`, `TestReconcile_PerHostIndependent`, `TestReconcile_OscillationDetectedByRegistryTimestamp` (net-zero state but registry LastWriteTime newer than last audit ts → reconciliation row emitted), `TestReconcile_WritesMaintenanceJobsRow` (asserts the drift_reconciliation maintenance_jobs row is upserted with correct outcome/rows_affected), `TestReconcile_PrincipalIsEmpty` (asserts every emitted reconciliation row has `principal == reconciliationPrincipal == ""` so the partial index semantics hold)
- [ ] T033 [P] [US2] HTTP test in `internal/dashboard/server_test.go`: `TestAuditHandler_ContractShape`, `TestAuditHandler_FilterByHost`, `TestAuditHandler_CursorPagination`, `TestHistoryHandler_Returns410`, `TestAuditHandler_ChangesOnlyDefaultsTrue`, `TestAuditHandler_CursorPaginationStableAcrossSameMsEvents` (inject two audit rows with identical ts on two different hosts; assert both are returned exactly once across paginated requests — regression gate on the mixed-order predicate), `TestSettingsHandler_ConcurrentRetentionPUTIsSerialized` (two PUT /api/v1/settings with different retention values fired concurrently; assert the config.json mutex serializes them and final state matches one of the two intents)
- [ ] T034 [US2] Update `internal/dashboard/openapi.yaml`: add `/api/v1/audit` path; mark `/api/v1/history/{host}` as deprecated with `x-deprecated: true` and a link to the replacement

**Checkpoint**: Audit API answers time-range queries across service restarts. CLI `drainctl history` reads the same data as the dashboard. Drift reconciliation fills downtime gaps with traceable rows.

---

## Phase 5: User Story 3 — Zoom Across Resolution Tiers (Priority: P2)

**Goal**: Chart zoom/pan drives automatic tier switching. Drag to a 1-hour window → raw; drag to a 24-hour window → 5-min averages; default 5-day view → hourly.

**Independent Test**: With at least 25 hours of raw data and 6 days of 5-min data materialized, open the dashboard, zoom incrementally from 5 days → 24 hours → 1 hour; each step re-fetches at a higher resolution and renders in under 500 ms.

### Implementation

- [ ] T035 [US3] Extend aggregator in `internal/telemetry/aggregator.go` with a 5-minute tier step: every aggregator tick, roll 5-minute buckets from `metrics_raw` into `metrics_5min` with `INSERT … ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET avg_value=excluded.avg_value, min_value=excluded.min_value, max_value=excluded.max_value, sample_count=excluded.sample_count`; applies a 5-minute watermark; on every tick unconditionally recomputes every eligible 5-minute bucket (end + watermark ≤ now AND source rows still present in `metrics_raw`); the hourly tier now reads from `metrics_5min` (cheaper than re-scanning raw); bucket floors via `time.UTC` with `Truncate(5*time.Minute)`.
- [ ] T036 [US3] Add `TierFiveMinute` case to `MetricsStore.QueryRange` in `internal/telemetry/metrics.go`
- [ ] T037 [US3] Implement `resolution=auto` selection in the `/api/v1/metrics/{host}` handler per `research.md` §8 (≤1 h → raw, 1 h–24 h → 5min, >24 h → hourly; degrade to next coarser tier if the finer tier's retention window doesn't cover the request)
- [ ] T038 [US3] Add debounced (150 ms) zoom/pan handlers in `frontend/src/lib/chart.svelte` that call `fetchMetrics` with the new visible window and `resolution=auto`; cancel in-flight requests when a newer zoom arrives
- [ ] T039 [US3] Render a "Data beyond this range is not retained" badge when `oldest_available > from` in the chart component
- [ ] T040 [P] [US3] Unit test: `internal/telemetry/aggregator_test.go` — `TestFiveMinuteAggregator_IsIdempotent`, `TestFiveMinuteAggregator_BucketBoundaries`, `TestHourlyFromFiveMin_Matches_HourlyFromRaw`, `TestAggregator_MissedMinuteZeroTickStillMaterializes` (simulate ticks at 00:59 and 01:59; assert the 01:00 bucket is materialized on the 01:59 pass), `TestAggregator_LateSampleAfterWatermarkIsIgnored` (samples arriving after watermark closes do NOT overwrite the bucket), `TestAggregator_DSTCrossoverBuckets` (verify UTC-truncation keeps bucket boundaries correct across DST transitions in a non-UTC local time zone)
- [ ] T041 [P] [US3] HTTP test: `internal/dashboard/server_test.go` — `TestMetricsHandler_ResolutionAutoMatrix` (table-driven: (window, expected_tier) for every decision boundary), `TestMetricsHandler_ExplicitResolutionOverrides`, `TestMetricsHandler_DegradesWhenRawPurged`

**Checkpoint**: Zoom works smoothly across tiers. Chart remains responsive during drag. Server is the single source of tier-selection truth.

---

## Phase 6: User Story 4 — Seamless Upgrade From Existing audit.jsonl (Priority: P2)

**Goal**: Upgrading from a version that wrote `audit.jsonl` preserves the full audit trail without operator action.

**Independent Test**: Stage a pre-upgrade install with an `audit.jsonl` containing records spanning several months, install the upgrade, start the service, and confirm an audit query returns both pre- and post-upgrade records; the original JSONL is renamed to `.bak.<timestamp>` and untouched thereafter.

### Implementation

- [ ] T042 [US4] Implement `MigrateJSONL(ctx, db *DB, dataDir string) (Result, error)` in `internal/telemetry/migrate_jsonl.go`: check `schema_meta[jsonl_migrated]`, bail if `true`; locate `audit.jsonl`; if absent, write marker and return; otherwise stream records in chunked transactions of 10,000 records each with `INSERT … ON CONFLICT(ts, host, new_state) DO NOTHING`. Progress marker stored in `schema_meta` as `jsonl_migrated_line_count` (count of source lines successfully consumed — NOT byte offset, because line length varies and JSONL is line-oriented). Malformed trailing records at EOF are treated as skipped and counted separately in `schema_meta[jsonl_migrate_skipped]`. Migration is complete only when the importer reads EOF AND writes `schema_meta[jsonl_migrated]=true`; until that flag is set, reconciliation MUST NOT run (T044's order depends on full migration completion). Partial migration + service crash = resume from `jsonl_migrated_line_count` on next start.
- [ ] T043 [US4] On success, rename `audit.jsonl` → `audit.jsonl.bak.<UTC-timestamp>` and set `schema_meta[jsonl_migrated]=true`; on any error, leave the JSONL in place and return the error (retry on next start is by construction idempotent — FR-021)
- [ ] T044 [US4] Invoke `MigrateJSONL` from service boot in `internal/svc/` (after `telemetry.Open()`, BEFORE drift reconciliation, before the audit writer goes live — so reconciliation's `LatestByHost` baseline sees imported rows per FR-020); log progress (records processed, skipped, duration) at INFO
- [ ] T045 [US4] Write a `maintenance_jobs` row (`name="jsonl_migration"`) capturing the run (success/skipped/failure)
- [ ] T046 [P] [US4] Unit tests in `internal/telemetry/migrate_jsonl_test.go`: `TestMigrate_NoJSONLWritesMarker`, `TestMigrate_ValidJSONLAllImported`, `TestMigrate_MalformedLineSkipped`, `TestMigrate_DuplicateRecordsIgnored`, `TestMigrate_RerunIsNoop`, `TestMigrate_PartialFailureLeavesJSONL`, `TestMigrate_RenameIncludesTimestamp`

**Checkpoint**: A staged upgrade preserves history end-to-end. Migration is safe to re-run.

---

## Phase 7: User Story 5 — Retention & Maintenance Observability (Priority: P3)

**Goal**: Storage stays bounded. Operators can see at a glance whether background jobs are running.

**Independent Test**: Configure a short retention window, inject records older than the window, trigger the retention job, and verify (a) those records are gone, (b) the dashboard maintenance widget shows the run with outcome=success and rows_affected>0.

### Implementation

- [ ] T047 [P] [US5] Implement `MaintenanceStore.UpsertJob(name, run Result)` and `.ListJobs()` in `internal/telemetry/maintenance.go` using `INSERT … ON CONFLICT(name) DO UPDATE`
- [ ] T048 [US5] Instrument the aggregator (both tiers) in `internal/telemetry/aggregator.go` to write `maintenance_jobs` rows (`aggregator_5min`, `aggregator_hourly`) via `MaintenanceStore.UpsertJob` at the end of every run (success and failure)
- [ ] T049 [US5] Implement retention worker in `internal/telemetry/retention.go`: every `cfg.Telemetry.RetentionIntervalMinutes` (jittered ±30s) delete rows older than the per-tier threshold in chunks of `LIMIT 10000`; run `PRAGMA incremental_vacuum(5000)` once per pass; write a `maintenance_jobs` row (`name="retention"`). Note: `incremental_vacuum` reclaims freed pages to SQLite's internal free-list, not to the OS — the file size stays at high-water mark until a manual `VACUUM INTO`-based compaction. Document this expectation in README + quickstart (handled in Commit 4). Worker also drives T004a's WAL checkpoint policy on its dedicated connection.
- [ ] T049a [US5] Document the high-water file-size behavior in `specs/007-sqlite-telemetry-store/quickstart.md` and in `README.md` (retention section): operators should expect `drainctl.db` to NOT shrink after large retention purges; compaction via `VACUUM INTO` is a follow-up feature.
- [ ] T050 [US5] Wire retention + aggregator goroutine lifecycles in `internal/svc/` (start after `telemetry.Open()`, stop on service shutdown); ensure shutdown is bounded (context cancel + 10s wait)
- [ ] T051 [US5] Implement `GET /api/v1/maintenance/status` handler in `internal/dashboard/server.go` per `contracts/http-maintenance.md`; compute `overdue = expected_interval_seconds > 0 && (server_time - finished) > 2 * expected_interval_seconds` per job (FR-031); `jsonl_migration` and `drift_reconciliation` always report `expected_interval_seconds=0` and are never overdue
- [ ] T052 [US5] Create `frontend/src/lib/maintenance-status.svelte` — compact widget rendering each job's last-run timestamp (relative), duration, outcome badge (green/amber/red), and an "overdue" warning pill
- [ ] T053 [US5] Mount the widget in `frontend/src/routes/+page.svelte` (or the nearest dashboard page) with a 15-second refresh interval
- [ ] T054 [US5] Add `fetchMaintenance()` in `frontend/src/lib/api.js`
- [ ] T055 [P] [US5] Unit tests in `internal/telemetry/retention_test.go`: `TestRetention_DeletesOlderThanThreshold`, `TestRetention_LeavesNewerRowsAlone`, `TestRetention_PerTierThresholds`, `TestRetention_ChunkedDeleteBoundsTransaction`, `TestRetention_IncrementalVacuumRuns`, `TestRetention_FailureIsNonFatalAndRetryRuns` (simulates a mid-delete error, asserts the service keeps running and the next scheduled run recovers — covers FR-013), `TestRetention_ShrinkFrom30dTo1dIsChunkedAndDashboardStaysResponsive` (seed 30 days of data, PUT settings with `metrics_days=1`, run the retention worker once, assert bounded chunked purge completed and dashboard responsiveness stayed < 100ms during the purge), `TestRetention_EmitsLogsAndMaintenanceRowPerRun` (asserts ≥1 ETW + file log record per run matches the `maintenance_jobs` row — covers FR-032)
- [ ] T056 [P] [US5] Unit tests in `internal/telemetry/maintenance_test.go`: `TestUpsertJob_OverwritesOnRerun`, `TestListJobs_ReturnsAllNames`, `TestOverdue_ComputedFromInterval`
- [ ] T057 [P] [US5] HTTP test in `internal/dashboard/server_test.go`: `TestMaintenanceHandler_ContractShape`, `TestMaintenanceHandler_OverdueFlagCorrect`, `TestMaintenanceHandler_OneShotJobNeverOverdue` (job with `expected_interval_seconds=0`, `finished` timestamp arbitrarily old; assert `overdue=false`)
- [ ] T058 [US5] Update `internal/dashboard/openapi.yaml` with `/api/v1/maintenance/status`

**Checkpoint**: File size stays bounded under long runs. Operators can spot a broken retention or aggregation job from the dashboard without grepping logs.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Remove superseded code, align docs, close the loop on version hygiene.

- [ ] T059 Delete `internal/store/memstore.go` and its test file (`memstore_test.go`); delete the `internal/store/` package if no other files remain; remove any remaining imports
- [ ] T060 Delete the file-only `AuditStore` struct and methods from root-package `audit.go` (and any helpers in `audit_setup.go` that become unused); keep the public `AuditRecord` type — external DLL/CLI callers still need it
- [ ] T061 [P] Update `README.md`: retention settings section, storage location, new endpoints, removal of `/api/v1/history/{host}`
- [ ] T062 [P] Update `docs/index.html` if endpoint list is embedded there
- [ ] T063 Update `CHRONICLE.md` with a one-paragraph entry for the telemetry store
- [ ] T064 Run `just lint` (gofmt + go vet + golangci-lint) and fix every warning — project rule is zero warnings
- [ ] T065 Run `prek run --all-files` and fix any pre-commit failures
- [ ] T066 Bump CalVer `YY.DOY.patch` in all 7 canonical locations (drainctl.go, drainctl.rc, .psd1, .wixproj, README.md, CLAUDE.md, docs/index.html) per `CLAUDE.md`; run `just resource` after editing `.rc`
- [ ] T067 Run the full `quickstart.md` validation end-to-end against an unsigned local build (`just all`); confirm the chart renders, zoom works, maintenance widget is green, audit query returns results, and reconciliation rows appear after a deliberate service-stopped drain toggle. Also walk the Regression Checklist in quickstart.md (CLI `drainctl history` output shape unchanged, dashboard empty-state render unchanged, Kerberos negotiate flow unchanged, host-add flow unchanged, notification triggers unchanged — covers FR-026).
- [ ] T067a Document WAL-aware backup procedure: `PRAGMA wal_checkpoint(TRUNCATE)` first, then copy `drainctl.db` + `drainctl.db-wal` + `drainctl.db-shm` atomically (as a set); OR use SQLite online backup API. Smoke-test the recipe in `quickstart.md`. Must replace the current quickstart text that simply says "close the sqlite3 session".
- [ ] T042a [US4] Document in `CHRONICLE.md` and in `research.md` §15 that audit events written by v26.107+ are invisible to pre-007 binaries if an operator downgrades mid-feature: rolling back from v26.107+ to v26.106 after weeks of runtime restores the `.bak` JSONL but loses every audit event recorded in SQLite since the migration — operators should export audit data to JSONL before downgrade if they want to retain it.
- [ ] T068 Build signed release (`just release`); smoke test MSI install on a clean Windows VM; attach install log; verify `drainctl.exe` has no new dependency on `sqlite3.dll`, `mingw*`, or `vcruntime*` beyond today's baseline (`dumpbin /dependents drainctl.exe` — covers SC-010).
- [ ] T069 [P] Perf smoke against a pre-seeded DB (50 hosts × 5 days of 5-min aggregates + 1 year of audit records): measure and record in `CHRONICLE.md` (a) first 5-day chart render over LAN, (b) 5-day → 1-hour zoom transition, (c) 30-day audit query latency, (d) 100 MB JSONL migration duration, (e) DB + WAL + SHM disk size after seeding (SC-006 assert < 500 MB), (f) dashboard latency p95/p99 while aggregator + retention run concurrently (SC-007 assert p99 < 100 ms), (g) 5-day × 50-host × 15s cadence simulator: per-host chart has no gap > 2 × sampling interval (SC-001). Asserts SC-001, SC-002, SC-003, SC-005, SC-006, SC-007, SC-008 against real numbers.
- [ ] T055a [P] Add `TestRestart_LosesAtMostOneSamplingInterval` in `internal/telemetry/metrics_test.go` (or integration tests) — drive ingest, SIGKILL mid-flight, restart, compare pre/post timelines, assert ≤ 1 sampling interval gap per host (SC-004).
- [ ] T059a Verify removals complete: `rg -uu MemAuditStore` returns zero hits across the repo; `rg -uu "history\s+map\["` scoped to `internal/dashboard/` returns zero hits. (Use `rg` / `Select-String` — do NOT use bash `grep`; project builds on Windows and contributors may not have a POSIX shell.) Covers SC-009.

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)** → no dependencies
- **Foundational (Phase 2)** → depends on Setup; blocks every user story phase
- **US1 (P1, Phase 3)** → depends on Foundational only
- **US2 (P1, Phase 4)** → depends on Foundational only
- **US3 (P2, Phase 5)** → depends on US1 (extends `MetricsStore` and the `/metrics` handler)
- **US4 (P2, Phase 6)** → depends on US2 (writes into the audit table)
- **US5 (P3, Phase 7)** → depends on US1 and US2 (retention applies to both record classes; maintenance widget reports on aggregator + retention, which are live by then)
- **Polish (Phase 8)** → depends on every user story being complete (deletions remove code that earlier phases still reference)

### Within each user story

- Implementation tasks can run in any order unless a later task's description references an earlier one; tests marked [P] can run alongside implementation.
- Frontend tasks depend on the corresponding backend handler being in place (integration point).

### Parallel opportunities

- **Within Setup**: T002 can run in parallel with T001 once `go.mod` is writable.
- **Within Foundational**: T005, T006, T008, T009 are all [P] — config + tests are independent of the db.go/schema.go implementation path (T003, T004, T007 must be sequential).
- **Within US1**: T010, T011 are [P] (different functions, same file — write them in one sitting); T018, T019, T020, T021 are all [P].
- **Within US2**: T022–T024 can be written together; T031–T034 are all [P].
- **Within US5**: the widget (T052), handler (T051), and store (T047) can be written in parallel once the shape is fixed.
- **Across stories**: US1 and US2 are independent — if you had two people, they could proceed in parallel off the same Foundational checkpoint.

---

## Parallel Example: User Story 1

```text
# Backend store operations — same file, can be written in one sitting:
Task T010: MetricsStore.Append in internal/telemetry/metrics.go
Task T011: MetricsStore.QueryRange in internal/telemetry/metrics.go

# Tests in parallel with implementation (different files):
Task T018: Unit tests in internal/telemetry/metrics_test.go
Task T019: Unit tests in internal/telemetry/aggregator_test.go
Task T020: HTTP tests in internal/dashboard/server_test.go
Task T021: openapi.yaml update
```

---

## Implementation Strategy

### MVP first (User Story 1 only)

1. Phase 1 Setup
2. Phase 2 Foundational → telemetry engine alive, schema applied, ACL correct
3. Phase 3 US1 → metrics persist, dashboard shows a durable 5-day chart at hourly resolution
4. **STOP and VALIDATE**: restart the service, confirm the chart keeps its history.
5. This is a shippable MVP — audit still runs on the legacy `MemAuditStore`; tier switching and retention come in later increments. Decision point: merge MVP, then continue; or keep iterating on the branch through US2.

### Incremental delivery

1. Ship after US1 → durable metrics + hourly tier (MVP demo).
2. Ship after US2 → audit in SQLite + reconciliation + CLI repoint. JSONL still present as fallback.
3. Ship after US3 → chart zoom polish.
4. Ship after US4 → upgrade path proven (removes the fallback safety net).
5. Ship after US5 → retention active, dashboard widget visible.
6. Ship after Polish → old code deleted, docs updated, signed MSI.

### Parallel team strategy

- With two developers, split US1 (metrics) and US2 (audit) after Foundational is merged. They share only the telemetry package boundary, not specific files — low merge conflict risk.
- US3 waits for US1 to land on mainline (it extends US1 code paths).

---

## Notes

- `[P]` markers sometimes tag tasks that touch the same file (e.g. T010/T011 both in `internal/telemetry/metrics.go`). Read `[P]` here as "can be written in one sitting by one developer, no ordering dependency" rather than the strict "different files" meaning from the template — the distinction matters for scheduling across people, not for single-author batches.
- Every new `.go` file MUST start with `//go:build windows` per `CLAUDE.md`.
- Commit cadence: one commit per task (or per small group) so the CalVer patch advances with the work it represents.
- Each commit: run `just lint` first; if it's red, fix it, don't skip pre-commit hooks.
- `MemAuditStore` / the history ring stay live until US1 and US2 are complete — don't delete them in Phase 2 or you'll break the running service mid-implementation. Deletion is explicitly in Phase 8.
- The named mutex that protects `config.json` writes is separate from the SQLite file's own WAL locking — do not try to share them (FR-027, CLAUDE.md).
