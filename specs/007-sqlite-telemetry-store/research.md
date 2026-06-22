# Phase 0 — Research & Decisions

All unknowns from Technical Context resolved below. No `NEEDS CLARIFICATION` markers remain.

---

## 1. Embedded database engine

**Decision**: SQLite via `modernc.org/sqlite` (pure-Go transpile of mainline SQLite).

**Rationale**:
- Satisfies the no-CGO / no-MinGW-at-runtime constraint. The service binary remains a single static .exe with no external runtime footprint beyond what's in-module.
- Tracks mainline SQLite closely; supports WAL, `PRAGMA user_version`, `PRAGMA incremental_vacuum`, `WITHOUT ROWID` tables, JSON operators, partial indexes. Everything the schema needs.
- Battle-tested in production Go projects (Caddy, Temporal's Go ecosystem tools, several observability stacks).
- Go `database/sql` adapter already present, `ctx`-aware, no bespoke binding layer needed.

**Alternatives considered**:

| Candidate | Rejected because |
|---|---|
| `mattn/go-sqlite3` | Requires CGO; fails constraint #11 from the spec input. |
| `crawshaw.io/sqlite` | Pure-Go but less active maintenance; modernc has broader adoption. |
| **libsql** (Turso fork) | Local embedded Go driver is CGO-based. Pure-Go libsql client only talks to a remote `sqld` server, which violates FR-028 ("no extra runtime dependency"). Features libsql adds (edge replication, Turso cloud, vector search, Wasm UDFs) are irrelevant to a single-host RDSH monitor. |
| **DuckDB** | Columnar, excellent for analytics, but Go driver is CGO-only and not SQLite-compatible. Different project. |
| **BoltDB / bbolt** | KV store, no SQL, would require hand-rolling range queries and ad-hoc aggregation. Worse developer ergonomics for audit search. |
| **TimescaleDB** | PostgreSQL extension — needs a running PG server. Violates FR-028 and the single-file design. See §10 below for why the shape is still informative. |
| **BadgerDB / Pebble** | Excellent LSM stores but no SQL; same ergonomic loss as Bolt plus a heavier dependency. |

---

## 2. SQLite open pragmas

**Decision**: On every open, issue the following pragmas in this order:

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;       -- ms
PRAGMA wal_autocheckpoint = 1000; -- pages (~4 MB at default page_size)
PRAGMA cache_size = -20480;       -- kB (20 MB)
PRAGMA temp_store = MEMORY;
PRAGMA mmap_size = 67108864;      -- 64 MB
PRAGMA auto_vacuum = INCREMENTAL; -- set once before first write
```

**Rationale**:
- WAL + `synchronous = NORMAL` is the standard crash-safe/fast combination for a single-writer telemetry workload. Data committed before a crash survives; an OS-level crash can lose at most the most recent unfsynced sample, which aligns with SC-004 ("at most one sampling interval").
- `busy_timeout` absorbs transient contention between the writer and dashboard readers without making either side surface an error.
- `auto_vacuum = INCREMENTAL` enables `PRAGMA incremental_vacuum(N)` to reclaim space after retention deletes without rewriting the whole file.
- `mmap_size` speeds repeated range scans for the chart without raising RSS unboundedly.

**Application strategy**: PRAGMAs are applied per connection (not once per open) because most of the above are connection-local in SQLite. Implementation uses `sql.OpenDB` with a `driver.Connector` whose `Connect()` issues the full PRAGMA block. The writer `*sql.DB` is pinned to `SetMaxOpenConns(1)`; readers use a separate `*sql.DB` with the same connector and rely on WAL for concurrency. DSN-embedded `_pragma=` is explicitly NOT used for this codebase — the connector path is the single supported approach so behaviour is one place to audit.

**Audit durability**: audit writes upgrade to `PRAGMA synchronous = FULL` for commit-durability on OS crash / power loss (low volume). Implementation: `AuditStore` owns a dedicated `*sql.Conn` obtained from the writer `*sql.DB` for its lifetime; on first acquisition it issues `PRAGMA synchronous = FULL` on that connection, then keeps it. Audit writes are routed exclusively through that pinned connection. Metrics writes stay at `synchronous = NORMAL` (SC-004 allows ≤1 sample loss).

**WAL checkpoint policy**: a dedicated `*sql.Conn` (separate from the writer and reader pools) with `busy_timeout=0` applied at `Connect` time is used by the retention goroutine to drive checkpoints. On each cycle it runs `PRAGMA wal_checkpoint(PASSIVE)` first (non-blocking, handles most of the work), then only escalates to `wal_checkpoint(TRUNCATE)` when the WAL still exceeds 16 MB. Each call is wrapped in a 5-second `context.Context` deadline. `SQLITE_BUSY` / `SQLITE_LOCKED` / deadline-exceeded are logged at WARN and the cycle is skipped — never retried within the same call, never blocks writers or readers. Any read transaction older than 60s is cancelled with a logged notice so it cannot pin the WAL snapshot indefinitely. WARN when WAL file size > 64 MB, repeat escalation hourly.

**Alternatives considered**:
- `synchronous = FULL` everywhere — unnecessary for metrics given SC-004 allows ≤1 sample loss; used only for audit writes where durability matters more than throughput.
- `journal_mode = DELETE` (rollback journal) — blocks readers during writes; WAL is the feature we explicitly need (FR-005).

---

## 3. Storage layout & downsample tiers

**Decision**: Three physical tables (`metrics_raw`, `metrics_5min`, `metrics_hourly`) plus `audit`, `maintenance_jobs`, and `schema_meta`. Per-tier retention is:

| Tier | Retention | Purpose |
|---|---|---|
| `metrics_raw` | 25 hours (1-hour buffer above the 24 h 5-min tier) | Full-resolution zoom window |
| `metrics_5min` | 6 days (1-day buffer above the 5-day hourly tier) | Mid-range view |
| `metrics_hourly` | configured retention (default 30 days, clamped 1–365) | 5-day chart default + long-tail |
| `audit` | configured retention (default 365 days, clamped 1–3650) | Compliance trail |

**Rationale**:
- Matches the three FR-017/FR-018 resolution tiers with explicit purge boundaries.
- Bounded storage: at 50 hosts × 6 counters the raw tier caps at ~1 M rows before purge; the 5-min tier at ~85 k rows; hourly at ~24 k rows for 30 days. Comfortably under SC-006 (500 MB).
- Each tier table has the minimal index needed for its access pattern (see data-model.md).

**Alternatives considered**:
- Single `metrics` table with runtime downsampling via `GROUP BY strftime(...)` — simpler schema but each chart render re-aggregates ~8 M rows. Fails the 500 ms zoom latency budget.
- ATTACH-based daily partitions — cheap drop for retention but adds complexity to every read path; unnecessary at our scale.
- Storing counter snapshots as one JSON blob per (host, ts) — compact but queries per-counter become slow JSON extraction; not worth it.

---

## 4. Aggregator worker cadence

**Decision**: A single background goroutine owned by `internal/telemetry` wakes every 60 seconds and:
1. Advances the 5-min tier by inserting any eligible 5-minute buckets from `metrics_raw`. Uses `INSERT … ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET avg_value=excluded.avg_value, min_value=excluded.min_value, max_value=excluded.max_value, sample_count=excluded.sample_count`. The conflict target matches the primary key declared in data-model.md for all three metrics tables.
2. On every tick, materializes every hourly bucket whose end-boundary + watermark ≤ now and whose source rows (in `metrics_5min` for the hourly tier, in `metrics_raw` for the 5-min tier) still exist; unconditional recompute via ON CONFLICT DO UPDATE. No wall-clock `minute = 0` gate.
3. **Watermark**: aggregator only rolls a bucket whose upper bound is older than `now - 5 minutes`, so late raw samples land before the bucket becomes eligible. After the source-tier retention horizon the bucket becomes ineligible (source purged) and further updates are impossible by construction.
4. **UTC-only bucket floors**: bucket start timestamps are computed using `time.UTC` with `Truncate(5*time.Minute)` / `Truncate(time.Hour)` so DST transitions do not corrupt boundaries.

**Rationale**:
- Both tiers built by upward rollup (raw → 5min → hourly) so each pass reads a small bounded window and commits a small batch.
- One goroutine, one connection, serialized writes — no need for a cross-goroutine lock beyond SQLite's internal serialization.
- `ON CONFLICT DO UPDATE` keyed on full bucket contents is restart-safe AND robust to late-arriving raw samples: after a crash mid-aggregation or a delayed raw sample for an already-materialized bucket, the next pass recomputes and overwrites. Watermark + source-retention horizon provides the freeze guarantee operators can trust (FR-008).
- Tick missing exact minute=0 boundaries no longer matters — every eligible bucket is caught on the next tick regardless of wall-clock alignment.

**Alternatives considered**:
- Streaming aggregation on every insert (trigger-based) — simpler logic but writes amplify per-sample cost; not free at 50 hosts × 6 counters × 4 samples/min.
- External scheduler/cron-like library — no value add for a single periodic task; a `time.Ticker` is enough.

---

## 5. Retention worker

**Decision**: A second goroutine wakes every 15 minutes (jittered) and:
1. For each tier, deletes rows older than its configured window via a bounded `DELETE … WHERE ts < ? LIMIT 10000` loop until exhausted.
2. Runs `PRAGMA incremental_vacuum(5000)` once per pass.
3. Writes a `maintenance_jobs` row with started/finished/duration/outcome/rows_deleted.

**Rationale**:
- Chunked DELETE avoids a multi-gigabyte write transaction that would block readers for noticeable periods (FR-006, SC-007 < 100 ms).
- Incremental vacuum amortizes space reclamation; a full `VACUUM` would require a write lock on the whole file and briefly block everything.
- Jitter (±30 s) prevents thundering-herd behaviour if multiple DrainCtl instances ever share a host (not expected, but cheap defensive move).

**High-water behavior**: `incremental_vacuum` reclaims free pages to SQLite's **internal free-list**, not to the OS. The `drainctl.db` file size stays at its high-water mark after large retention purges. This is documented behavior that operators must be aware of — don't interpret a stable file size as "retention isn't running". A proper shrink requires an offline `VACUUM` or `VACUUM INTO` sweep, which is out of scope for this feature.

**Alternatives considered**:
- Full `VACUUM` on schedule — blocks writers for the duration; unacceptable.
- Drop-partition-style retention (requires ATTACH layout) — rejected in §3.

---

## 6. Drift reconciliation on startup (FR-001a)

(After JSONL migration has completed so `LatestByHost` sees imported rows.)


**Decision**: After opening the DB and before the service begins subscribing to registry changes or opening the named pipe:
1. For each known host in `ServerState`, read the current registry drain state (existing `registry.go` code).
2. Look up the most recent `audit` row for that host.
3. If the current drain state differs from that row's `new_state` (or no prior row exists but drain is non-default), insert a single reconciliation audit row: `principal = ""` (empty; matches the `audit_principal` partial index `WHERE principal <> ''` in data-model.md; defined once as the `reconciliationPrincipal` constant in `reconcile.go` so any future mutation breaks the partial-index semantics loudly), `changed_by = ""`, `reason = "service-downtime drift: last-known X, observed Y"`. Timestamp = current time; `key_modified` = current-row registry timestamp; record a `before_ts` == last audit ts so the uncertainty window is inspectable.
4. Additionally: if registry `LastWriteTime` (`key_modified_ts`) is newer than the last audit record's `ts` for that host, emit a reconciliation row marking a downtime-activity window regardless of current-vs-last equality — this detects A→B→A oscillations where endpoints match but intermediate changes occurred while the service was down.
5. Write one `maintenance_jobs` row `name="drift_reconciliation"` capturing started/finished/duration/outcome/rows_affected for the full pass.

**Rationale**:
- Single row per host per gap (not one per polling miss) keeps the audit trail readable.
- Timestamp window captured in two fields lets operators see the uncertainty range.
- Runs before any live ingest, so reconciliation and live events can't race.

**Alternatives considered**:
- Always emitting a reconciliation row on startup — noisy; only emit when actual drift detected.
- No reconciliation (option A from clarify) — rejected by Q1.

---

## 7. Legacy JSONL migration (FR-020 – FR-023)

**Decision**: During first DB open:
1. Check `schema_meta` for key `jsonl_migrated`. If present, skip.
2. Check for `audit.jsonl` in the data directory. If absent, record `jsonl_migrated = true` and skip.
3. Open the JSONL, stream records in **chunked transactions of 10,000 records each** with `INSERT … ON CONFLICT(ts, host, new_state) DO NOTHING`. Each chunk updates `schema_meta[jsonl_migrated_line_count]` (count of source lines consumed so far — NOT byte offset, because line length varies and JSONL is line-oriented). Malformed trailing records at EOF are treated as skipped and counted separately in `schema_meta[jsonl_migrate_skipped]`.
4. On EOF, rename `audit.jsonl` → `audit.jsonl.bak.<UTC-timestamp>` and set `jsonl_migrated = true`. Drift reconciliation runs only after this flag is set (not after partial resume) — see FR-020 and tasks.md T044.
5. On partial failure (crash mid-chunk), leave the JSONL in place, log the error. Next start resumes from `jsonl_migrated_line_count`; the ON CONFLICT DO NOTHING makes it safe even if the chunk boundary was ambiguous.

**Rationale**:
- Chunked import avoids holding a 100+ MB transaction open with a correspondingly large WAL; each chunk commits cleanly.
- Idempotent retry by construction; satisfies FR-021.
- Rename preserves the original for forensic comparison.
- Unique key on `(ts, host, new_state)` handles rare duplicate entries in degenerate JSONL without failing.
- Reconciliation-after-full-migration invariant means `LatestByHost` baseline is never computed against partial data.

**Alternatives considered**:
- Streaming without a transaction — faster but leaves the table in a partially imported state on failure, complicating retry logic.
- Copy JSONL into a staging table, then swap — overkill for a one-shot import.
- Never rename the JSONL — operators would have to remember to clean up; rename is explicit in FR-022.

---

## 8. Query contract & resolution selection

**Decision**: Expose two metric endpoints:
- `GET /api/v1/metrics/{host}?from=<iso8601>&to=<iso8601>&resolution=raw|5min|hourly|auto` (default `auto`)
- `GET /api/v1/audit?from=<iso8601>&to=<iso8601>&host=<host>&actor=<principal>&limit=<n>&cursor=<opaque>`

With `resolution=auto`, the server picks the coarsest tier that still yields ≥ 240 datapoints in the window, capped at the finest tier whose retention covers the range:

| Visible window | Selected tier |
|---|---|
| ≤ 1 h | raw (if still retained) |
| 1 h – 24 h | 5min |
| > 24 h | hourly |

The response includes the actual tier served and the oldest/newest timestamps in the window so the dashboard can show a "some data older than retention window" badge if the request range exceeds available data.

**Rationale**:
- Keeps the client simple: always send the visible window; the server picks the tier.
- Explicit overrides (`resolution=raw`) allow debugging without a client change.
- ≥ 240 datapoints target = chart pixel width at typical dashboard resolution; avoids over- or under-sampling.

**Alternatives considered**:
- Client chooses tier — pushes the decision across the network boundary and duplicates retention logic in JS.
- Dynamic GROUP BY at query time — already rejected in §3.

---

## 9. Dashboard chart integration (FR-017 – FR-019a)

**Decision**: uPlot zoom/pan handlers call the existing `api.js` wrapper with the new visible range and `resolution=auto`. The wrapper debounces to 150 ms so a drag doesn't spam the server. Empty results render `"Collecting data…"` text inside the plot area (FR-019a). Coarser-tier fallback (FR-019) is transparent — the server just returns hourly when raw is purged.

**Rationale**:
- Debounce is required regardless of backend; a drag can emit dozens of events per second.
- Server-decided tier means no client-side retention logic; the server already knows what's available.

**Alternatives considered**:
- Client-side resolution table — adds drift risk when retention settings change.
- Pre-fetch all three tiers and switch locally — wastes bandwidth and memory.

---

## 10. Why TimescaleDB's patterns still inform the design

TimescaleDB (PostgreSQL extension, rejected as an engine in §1) gets three things right that we are explicitly mirroring at our scale:

| TimescaleDB feature | Our SQLite analogue |
|---|---|
| Hypertables (time-partitioned storage, cheap partition drop) | Per-tier tables with composite (host, ts) index + `DELETE WHERE ts < ?` + incremental vacuum. We don't need physical partitioning at our scale. |
| Continuous aggregates (auto-refreshing materialized views) | Explicit 5-min + hourly aggregator goroutine writing to materialized tables. Manual, but the semantics match. |
| Retention & compression policies (declarative, engine-enforced) | Retention worker (`internal/telemetry/retention.go`) configured via `config.json`. |

Recording this explicitly so the rejection isn't re-litigated later.

---

## 11. ACL on the DB file

**Decision**: Reuse the existing logic that applies ACLs to `config.json`. Extract the helper (currently in `config.go`) into a small utility inside `internal/telemetry/db.go` (or a shared `internal/winacl/` if the helper isn't already shared) and apply it once to `drainctl.db`, `drainctl.db-wal`, `drainctl.db-shm` after open.

**Rationale**:
- FR-004 is explicit: same ACL as `config.json` (SYSTEM + local Administrators + DrainCtl service account).
- WAL and SHM companion files inherit the permissive default ACL of their parent dir by default; we must clamp them explicitly.

**Alternatives considered**:
- Relying on directory ACL inheritance — too implicit, easy to regress.

---

## 12. Configuration shape (no viper, stays in config.json)

**Decision**: Extend `Config` in `config.go` with:
```go
type Config struct {
    // … existing fields …
    Retention     RetentionConfig     `json:"retention"`
    Telemetry     TelemetryConfig     `json:"telemetry"`
}

type RetentionConfig struct {
    MetricsDays int `json:"metrics_days"` // default 30, clamp 1..365
    AuditDays   int `json:"audit_days"`   // default 365, clamp 1..3650
}

type TelemetryConfig struct {
    AggregatorIntervalSeconds int `json:"aggregator_interval_seconds"` // default 60
    RetentionIntervalMinutes  int `json:"retention_interval_minutes"`  // default 15
}
```
with matching clamps in `ClampRetention()` (extended or renamed — pick one, don't double-clamp in callers).

**Rationale**:
- Matches the existing "no viper, JSON with atomic writes" pattern.
- Scoped updaters already in place for dashboard PUT /api/v1/settings; extend the same pattern for the new knobs.

**Alternatives considered**:
- New dedicated `telemetry.json` file — splits truth across files; rejected.
- Hardcoded intervals — operators have asked for tunability in other areas; keep it consistent.

---

## 13. CLI history behaviour when the service is stopped

**Decision**: `drainctl history` opens the DB file **read-only** using SQLite URI `?mode=ro&_txlock=deferred` (do NOT force `_journal_mode` — inherit the file's persisted mode) when it can't reach the service pipe. Behavior cases:

- (a) `-wal` and `-shm` present and readable — normal WAL read open, live writer data visible up to the last committed transaction.
- (b) `-wal` / `-shm` missing (service was cleanly shut down and checkpointed) — SQLite opens the main file in rollback-journal read mode automatically; CLI proceeds but logs at INFO that live writer data may be ≤1 commit stale.
- (c) `-wal` present but CLI user cannot open it (ACL mismatch) — open fails with a clear error citing the sidecar path and required permissions; no silent fallback.
- (d) `-wal` exists but `-shm` is stale or corrupt — explicit failure, no silent fallback.

**Rationale**:
- Preserves the existing UX where the CLI can answer history queries off-box or with the service stopped.
- Read-only open does not require exclusive access; WAL allows readers without a writer too.
- Consistent with the clarify decision that the service is the single writer (Q1).
- Explicit-fail on sidecar permission / corruption errors prevents a CLI that silently shows stale data an operator might trust.

**Alternatives considered**:
- CLI requires the service — regression from today's behaviour.
- CLI writes when service is down — explicitly forbidden by Q1 (option D was not chosen).

---

## 14. Test strategy

**Decision**: Three layers.

1. **Unit** — per-function tests in `internal/telemetry/*_test.go` against a temp-directory DB. Cover: open with pragmas, audit append + query, metrics append + 5-min/hourly aggregation determinism, retention delete bounds, drift reconciliation, JSONL migration (with malformed lines, with duplicates, with partial failure simulation).
2. **Integration** — end-to-end tests in `internal/svc/` that boot the service loop with a temp data dir and assert that a simulated registry change produces an audit row and that metrics collected via the existing `perfmon` pipeline land in the store.
3. **HTTP contract** — tests against the dashboard HTTP handlers in `internal/dashboard/` using `httptest` that hit `/api/v1/metrics/{host}`, `/api/v1/audit`, `/api/v1/maintenance/status` and validate the response shape matches the contracts in `contracts/`.

**Rationale**:
- The existing project already uses a mix of root-package tests (`audit_test.go`, `config_test.go`, `format_test.go`, `notify_test.go`, etc.) and package-scoped tests in `internal/*/`. New code follows the same pattern.
- No network DB fixtures, no Docker — everything runs against temp files. Matches the project's "runs on a plain Windows box" culture.

**Alternatives considered**:
- Golden-file comparisons against real recorded sessions — brittle; reserve for formatting-shape tests not telemetry ingest.

---

## 15. Rollback plan

**Decision**: If a deploy of this feature goes wrong in production, the rollback is:
1. Stop the DrainCtl service.
2. Downgrade to the prior MSI (operators do this for any release; nothing special here).
3. The `audit.jsonl.bak.<timestamp>` file is left in the data directory by the migration — rename back to `audit.jsonl` to restore the pre-upgrade file. The old binary will pick it up on next start.
4. The new `drainctl.db` file can be deleted or left in place — the old binary ignores it.
5. Post-incident: analyse `drainctl.db` offline (it's just SQLite).

**Rationale**:
- No irreversible migration — the JSONL backup is always on disk.
- Downgrade is the standard MSI operation, not a bespoke rollback script.
- The new file is inert to the old binary; leaving it in place is harmless.

**Important: post-migration downgrade caveat**. If v26.107+ has been running for days/weeks after a successful migration, audit events recorded into SQLite in that window are **invisible to pre-007 binaries** — the pre-007 binary only reads `audit.jsonl`. Rolling back v26.107+ → v26.106 then restores the `.bak` JSONL, but every audit event recorded in the new store since the migration is effectively hidden from the old binary (the data is still there in `drainctl.db` for later forensics). Operators who anticipate potentially long-lived downgrades should export audit data to JSONL before downgrade (an export tool is a follow-up feature per the spec Clarifications Q4 decision — deliberately not in this feature).

**Alternatives considered**:
- Keep writing JSONL in parallel during a soft-launch window — doubles write cost and tempts operators to diverge. Not worth it given the clean JSONL backup path.

---

## 16. Version bump

**Decision**: CalVer is git-derived as `YY.MM.BUILD` per CLAUDE.md. The /speckit-tasks phase will produce one task per commit boundary that reminds the implementer to confirm `just version` before `just lint`.

**Rationale**: Non-negotiable project convention. Recording it in research to avoid a later "oops".

---

## 17. Network-share data directory

**Decision**: Only **local fixed-disk volumes** (NTFS / ReFS) are supported for `drainctl.db`. SMB / UNC / mapped network drives are NOT supported. On `Open()` the service resolves the data-dir volume via `GetDriveTypeW` and, if the result is not `DRIVE_FIXED`, refuses to start with a clear operator-actionable error naming the path and the policy (FR-004). A WARN log entry is written before the abort so the error is visible in Event Viewer.

**Rationale**: SQLite WAL's `-shm` shared memory file has undefined semantics on most SMB implementations. Allowing WAL on a share risks silent data corruption. The policy is "refuse to start" rather than "log warning and continue" because WAL correctness cannot be guaranteed on SMB — continuing would give operators false confidence that their farm is monitored durably.

**Alternatives considered**:
- Log warning + continue — rejected. An operator running on SMB by accident should be loudly refused, not allowed to silently corrupt audit data.
- Force `journal_mode=DELETE` on detected SMB — re-introduces reader-blocking which FR-005 explicitly forbids.
