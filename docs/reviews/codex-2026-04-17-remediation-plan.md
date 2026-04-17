# Remediation Plan — Codex Review 2026-04-17

Source: 32 deduplicated findings from `docs/reviews/codex-2026-04-17-synthesis.md`.
Branch: `007-sqlite-telemetry-store` (commit 85b42de, clean).
Scope: markdown planning docs only. No Go code is touched.

## 1. Summary

Three mandatory commits (Tier 1 design bugs, Tier 2 operational task additions, Tier 3 acceptance polish) plus one optional operator-docs commit. All edits live under `specs/007-sqlite-telemetry-store/` plus `README.md` / `docs/index.html` / `CHRONICLE.md` only where operator docs demand it. Plan stops at "planning docs are internally consistent and ready for `/speckit-implement`".

## 2. Ordering & dependencies

- **Tier 1 must land first.** Tier-2 task additions reference post-Tier-1 wording (e.g. the aggregator watermark test in T2 only makes sense once T1.4 has redefined the conflict strategy; T1.7's `maintenance_jobs` row for reconciliation is what T1.8 then tags as `expected_interval_seconds <= 0`).
- **Within Tier 1, the boot-sequence cluster (T1.1, T1.2, T1.6, T1.7) is tightly coupled** — all four edit `tasks.md` T025 and `research.md` §6. Do them in a single pass; re-read T025 end-to-end after each sub-edit to avoid losing earlier changes.
- **Aggregator cluster (T1.4 + T1.5) is coupled**: the same watermark fixes both the late-sample freeze and the `minute=0` gate. Edit `research.md` §4 and `tasks.md` T012/T035 in one pass.
- Per-commit CalVer bump (CLAUDE.md) is explicitly out of scope here; it happens at implementation time.

## 3. Commit 1: Tier-1 blockers

Commit message direction: `spec(007): resolve Tier-1 blockers from codex 2026-04-17 review`.

### Step 1.1 — Fix boot sequence (T1.1)

- `tasks.md` T025: `invoked from service boot after telemetry.Open() and before live ingest starts` → `invoked from service boot after telemetry.Open() AND after MigrateJSONL, before live ingest starts`.
- `tasks.md` T044: `after telemetry.Open(), after drift reconciliation, before the audit writer goes live` → `after telemetry.Open(), BEFORE drift reconciliation, before the audit writer goes live`.
- `research.md` §6 step 1: prepend `(After JSONL migration has completed so LatestByHost sees imported rows.)`.

Verify: re-read T025 + T044 + research §6 + spec FR-020 — all four must name the sequence `Open → MigrateJSONL → reconcile → live ingest`. Effort: **S**.

### Step 1.2 — Principal value → empty string (T1.2)

- `spec.md` FR-001a (line 137): change `principal unknown` → `principal unknown (stored as empty string to match the partial index in data-model.md)`.
- `research.md` §6 step 3: change `principal = "reconciliation"` → `principal = "" (empty; matches the audit_principal partial index WHERE principal <> '' in data-model.md)`.
- `tasks.md` T025 already says `principal=""` — no edit.
- `contracts/http-audit.md` example already shows `"principal": ""` — no edit.

Additionally, to prevent regression (see Risks §9): add a constant in the implementation referenced from both the writer and the test — this is called out here so the test list in Step 1.5 / 1.6 knows what to check.

- `tasks.md` T025: append `; use a single shared constant reconciliationPrincipal = "" declared in reconcile.go for the stored value (any future mutation to this constant breaks the partial-index semantics, so both writer and test reference it directly)`.
- `tasks.md` T032: append test `TestReconcile_PrincipalIsEmpty` asserting every reconciliation row emitted has `principal=""` (compare against `reconciliationPrincipal` constant).

Verify (two checks, since the concept "unknown" is allowed to survive in prose while the stored value MUST be empty): (1) any line that specifies an INSERT/stored value for reconciliation principal uses `""` — no `"reconciliation"`, no `"unknown"` literal; (2) spec wording may retain the noun "unknown" as a concept as long as it is qualified with "stored as empty string". Effort: **S**.

### Step 1.3 — PRAGMA persistence across pool (T1.3)

- `research.md` §2: append paragraph `Application strategy: PRAGMAs are applied per connection (not once per open) because most of the above are connection-local in SQLite. Implementation uses sql.OpenDB with a driver.Connector whose Connect() issues the full PRAGMA block. The writer *sql.DB is pinned to SetMaxOpenConns(1); readers use a separate *sql.DB with the same connector and rely on WAL for concurrency. DSN-embedded _pragma= is explicitly NOT used for this codebase — the connector path is the single supported approach so behaviour is one place to audit.`
- `tasks.md` T004: `apply every pragma from research.md §2` → `apply every pragma from research.md §2 via a driver.Connector whose Connect() issues the full PRAGMA block (so every pooled connection — reader and writer — has them); pin the writer *sql.DB to SetMaxOpenConns(1); readers use a separate *sql.DB with the same connector`.
- `tasks.md` T008: append test name `TestOpen_PragmasAppliedToEveryConnection`. Implementation: open the reader *sql.DB, use `db.Conn(ctx)` to acquire two distinct `*sql.Conn` handles explicitly (keep both open simultaneously), then on each run `PRAGMA foreign_keys`, `PRAGMA busy_timeout`, `PRAGMA temp_store`, and assert the expected values from research.md §2. Do NOT assert on `journal_mode` (database-level/persistent, would pass even with the bug); the test must check connection-local pragmas that would diverge if only the first pooled connection received the block.

Verify: §2 and T004/T008 both name the connector approach AND the revised test specifically targets connection-local pragmas (not `journal_mode`). Effort: **M**.

### Step 1.4 — Aggregator watermark + ON CONFLICT UPDATE (T1.4 + T1.5)

Goal: make the aggregator correct in the face of late-arriving raw samples, without inventing a bucket-digest mechanism that isn't yet modelled. The simplest sound approach: **unconditionally recompute and upsert every eligible bucket on each pass** — "eligible" means the bucket's end + watermark ≤ now AND the bucket's window still has raw rows in `metrics_raw` (i.e. raw hasn't been purged yet). Past the raw retention horizon (25h for the 5-min tier, 6 days for the hourly tier fed from 5-min), the bucket is frozen because its source data is gone. No change-detection state is required.

- `research.md` §4: replace `INSERT … ON CONFLICT(bucket_ts, host, counter) DO NOTHING` → `INSERT … ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET avg_value=excluded.avg_value, min_value=excluded.min_value, max_value=excluded.max_value, sample_count=excluded.sample_count`. (The conflict target `(host, bucket_ts, counter)` matches the primary key declared in data-model.md for all three metrics tables — SQLite's ON CONFLICT matches the column set, not declaration order, but we use this canonical ordering everywhere for clarity.) Replace `Every hour boundary (when minute = 0)` → `On every tick, materialize every hourly bucket whose end-boundary + watermark ≤ now and whose source rows (in metrics_5min for the hourly tier, in metrics_raw for the 5-min tier) still exist; unconditional recompute via ON CONFLICT DO UPDATE.`. Append bullet: `Watermark: aggregator only rolls a bucket whose upper bound is older than now - 5 minutes, so late raw samples land before the bucket becomes eligible. After the source-tier retention horizon the bucket becomes ineligible (source purged) and further updates are impossible by construction.`
- `tasks.md` T012: replace `using INSERT … ON CONFLICT(host, bucket_ts, counter) DO NOTHING; gated to run only on minute=0 ticks` → `using INSERT … ON CONFLICT(host, bucket_ts, counter) DO UPDATE SET avg_value=excluded.avg_value, min_value=excluded.min_value, max_value=excluded.max_value, sample_count=excluded.sample_count; applies a 5-minute watermark; on every tick unconditionally recomputes every eligible hourly bucket (end + watermark ≤ now AND source rows still present); no wall-clock minute=0 gate`.
- `tasks.md` T035: mirror the same watermark + unconditional-recompute + UPSERT language for the 5-minute tier.
- `tasks.md` T019: append test `TestAggregator_LateSampleUpdatesBucket` (insert raw sample, run aggregator → bucket materialized; insert a NEW raw sample within the same bucket but before watermark closes; rerun aggregator; assert bucket row reflects both samples).
- `tasks.md` T040: append test `TestAggregator_MissedMinuteZeroTickStillMaterializes` (simulate ticks at 00:59 and 01:59; assert the 01:00 bucket is materialized on the 01:59 pass).
- `tasks.md` T040: append test `TestAggregator_LateSampleAfterWatermarkIsIgnored` (codifies the bound — samples arriving after watermark closes do NOT overwrite the bucket; ensures the operator can trust the aggregate once the watermark closes).

Verify: no `DO NOTHING` for aggregator remains in §4 / T012 / T035; the phrase `whose source rows … still exist` appears in §4; the conflict target `(host, bucket_ts, counter)` is identical across §4, T012, T035 AND matches the PRIMARY KEY declared in data-model.md §metrics_5min / §metrics_hourly. Effort: **M**.

### Step 1.5 — Oscillation detection (T1.6)

- `research.md` §6 step 3: after the `If the current drain state differs…` sentence, append `Additionally: if registry LastWriteTime (key_modified_ts) is newer than the last audit record's ts for that host, emit a reconciliation row marking a downtime-activity window regardless of current-vs-last equality — this detects A→B→A oscillations where endpoints match but intermediate changes occurred while the service was down.`
- `tasks.md` T025: append `; also emit reconciliation when registry LastWriteTime > last_audit.ts even if states match (oscillation case)`.
- `tasks.md` T032: append test `TestReconcile_OscillationDetectedByRegistryTimestamp`.

Effort: **S**.

### Step 1.6 — maintenance_jobs row for drift_reconciliation (T1.7)

- `tasks.md` T025: append `; write a maintenance_jobs row name="drift_reconciliation" capturing started/finished/duration/outcome/rows_affected (per-job count of reconciliation rows inserted).`
- `tasks.md` T032: append test `TestReconcile_WritesMaintenanceJobsRow`.

Effort: **S**.

### Step 1.7 — `expected_interval_seconds = 0` sentinel for one-shot jobs (T1.8)

Decision: use the sentinel value `0` rather than making the field JSON-`null`. Rationale — `0` is simpler for the Svelte widget (no null-guard), round-trips cleanly through Go's default `int`, and encodes "N/A" without introducing an optional type into the contract.

- `contracts/http-maintenance.md` line 67: `True when server_time - finished > 2 * expected_interval_seconds (FR-031).` → `True when expected_interval_seconds > 0 AND server_time - finished > 2 * expected_interval_seconds (FR-031). One-shot startup jobs (jsonl_migration, drift_reconciliation) report expected_interval_seconds = 0 and are never overdue.`
- `contracts/http-maintenance.md` `expected_interval_seconds` row (`int`): append `. 0 means "one-shot startup job"; UI must not flag as overdue. Field is never null — use 0 as the sentinel.`
- `tasks.md` T051: `compute overdue = (server_time - finished) > 2 * expected_interval_seconds per job (FR-031)` → `compute overdue = expected_interval_seconds > 0 && (server_time - finished) > 2 * expected_interval_seconds per job (FR-031); jsonl_migration and drift_reconciliation always report expected_interval_seconds=0`.
- `tasks.md` T057: append test `TestMaintenanceHandler_OneShotJobNeverOverdue` (job with `expected_interval_seconds=0`, `finished` timestamp arbitrarily old; assert `overdue=false`).

Effort: **S**.

### Step 1.8 — Fix plan.md retention math (T1.9)

- `plan.md` line 20: `≈ 8.6M raw rows per 5 days before downsample purge` → `≈ 1.44M raw rows in the 25h retention window (matches data-model.md storage-footprint estimate)`.

Effort: **S**.

## 4. Commit 2: Tier-2 task additions

Commit direction: `spec(007): add Tier-2 operational tasks from codex review`.

- **T2.1** (synchronous FULL for audit) — `research.md` §2 decision: `Audit writes upgrade to synchronous=FULL for commit-durability on OS crash / power loss (low volume); metrics writes stay at NORMAL (SC-004 allows ≤1 sample loss). Implementation: AuditStore owns a dedicated *sql.Conn obtained from the writer *sql.DB for its lifetime; on first acquisition it issues PRAGMA synchronous=FULL on that connection, then keeps it. Audit writes are routed exclusively through that pinned connection so the stronger fsync discipline is durable for audit regardless of pool behavior.` Extend T022 with `; AuditStore holds a dedicated *sql.Conn with PRAGMA synchronous=FULL applied at acquisition; all Append calls route through that connection`. Extend T031 with test `TestAudit_DedicatedConnectionHasSynchronousFull`.
- **T2.2** (WAL checkpoint policy) — Add **T004a** `WAL checkpoint policy: periodic PRAGMA wal_checkpoint(TRUNCATE) from the retention goroutine, executed on a DEDICATED *sql.Conn (separate from the writer and the reader pool) that has busy_timeout=0 applied at Connect time — this guarantees wal_checkpoint() returns SQLITE_BUSY immediately if a long reader is present, instead of blocking up to the default busy_timeout. The checkpoint call is additionally wrapped in a ctx with 5s deadline as a belt-and-braces bound. On SQLITE_BUSY / SQLITE_LOCKED / deadline-exceeded, log at WARN and skip the cycle (never retry inside the same call, never block writers or readers). Before each TRUNCATE attempt run PRAGMA wal_checkpoint(PASSIVE) first — PASSIVE never blocks and does the bulk of the work; only switch to TRUNCATE when the WAL still exceeds 16 MB afterward so the file can be reclaimed. Reader side: any read transaction older than 60s is cancelled with a logged notice so it cannot pin the WAL snapshot indefinitely. Log WARN when WAL file size > 64 MB and repeat escalation hourly.`. Mirror the dedicated-connection + busy_timeout=0 + PASSIVE-then-TRUNCATE cadence into `research.md` §2 and §5.
- **T2.3** (high-water behavior) — Amend `research.md` §5: `incremental_vacuum reclaims to the internal free-list, not to the OS; file size stays at high-water mark until a manual VACUUM INTO.` Add **T049a** `Document high-water behavior in README and quickstart; future compaction via VACUUM INTO out of scope.`
- **T2.4** (integrity_check on startup) — Add **T004b** `PRAGMA integrity_check quick_check at every service startup (not just first-install); run once after pragmas are applied and before the service begins ingest; on non-ok result abort service start with the spec-edge-case error message citing the DB path. On clean start the check adds well under 1s at target volumes.`. Add test `TestOpen_CorruptFileReturnsClearError` (plant a fixture DB with a corrupted page, assert Open returns an error whose message contains the DB path and the SQLite diagnostic).
- **T2.5** (disk-full path) — Extend T018 and T031 with `TestAppend_DiskFullIsGracefullyHandled`.
- **T2.6** (clock skew) — Extend T018 with `TestAppend_FutureTimestampStoresAndLogs`.
- **T2.7** (retention shrink) — Extend T055 with `TestRetention_ShrinkFrom30dTo1dIsChunkedAndDashboardStaysResponsive`.
- **T2.8** (UTC bucket floors + DST) — `research.md` §4: `Bucket floor is UTC-based (time.UTC with Truncate) so DST transitions do not corrupt boundaries.` Add to T040: `TestAggregator_DSTCrossoverBuckets`.
- **T2.9** (WAL-aware backup) — Add **T067a**: `Document backup: PRAGMA wal_checkpoint(TRUNCATE) then copy three files atomically, OR use SQLite online backup API. Smoke-test in quickstart.`
- **T2.10** (downgrade audit gaps) — Add **T042a**: `Document that post-007 audit events are invisible to pre-007 binaries; downgrading loses those events. Capture in CHRONICLE + research.md §15.`
- **T2.11** (chunked JSONL migration) — Extend T042 with `; chunked import in transactions of 10,000 records each. Progress marker stored in schema_meta as jsonl_migrated_line_count (count of source lines successfully consumed — NOT byte offset, because line length varies and JSONL is line-oriented). Malformed trailing records at EOF are treated as skipped and counted separately in schema_meta[jsonl_migrate_skipped]. Migration is complete only when the importer reads EOF AND writes schema_meta[jsonl_migrated]=true; until that flag is set, reconciliation MUST NOT run (Step 1.1 depends on full migration completion). Partial migration + service crash = resume from jsonl_migrated_line_count on next start.`
- **T2.12** (CLI read-only WAL sidecars) — Extend `research.md` §13 with a concrete behavior table: `CLI opens drainctl.db with SQLite URI ?mode=ro&_txlock=deferred (do NOT force _journal_mode; let SQLite inherit the file's persisted mode). Behavior cases: (a) -wal and -shm present and readable — normal WAL read open; (b) -wal / -shm missing — SQLite opens the main file in rollback-journal read mode automatically; CLI proceeds but logs at INFO that live writer data may be ≤1 commit stale; (c) -wal present but CLI user cannot open it (ACL) — open fails with a clear error citing the sidecar path and required permissions; (d) -wal exists but -shm is stale/corrupt — same as (c), explicit failure, no silent fallback. "Fall back to snapshot open" is explicitly NOT used (snapshot open requires special SQLite build and adds complexity without solving the permission case).` Add **T028a** with test matrix `TestCLIReadOnly_SidecarsPresent`, `TestCLIReadOnly_SidecarsMissing`, `TestCLIReadOnly_SidecarUnreadableAclError`, `TestCLIReadOnly_SidecarCorruptError`.
- **T2.13** (audit pagination key) — T023: `cursor-based pagination keyed on (ts DESC, rowid)` → `cursor-based pagination keyed on the full primary key declared in data-model.md (PRIMARY KEY (ts, host, new_state)). Ordering is all-descending so the tuple-comparison seek predicate matches the sort: ORDER BY ts DESC, host DESC, new_state DESC. Cursor payload carries the (ts, host, new_state) triple of the last row returned; the next page query uses WHERE (ts, host, new_state) < (:cursor_ts, :cursor_host, :cursor_new_state) — SQLite's row-value comparison is lexicographic-ascending, so this predicate under an all-DESC ordering correctly advances to the next-smaller lexicographic tuple, which IS the next row in the descending sort. (An equivalent explicit form, kept here as a comment in the task for implementer reference, is WHERE ts < :cursor_ts OR (ts = :cursor_ts AND host < :cursor_host) OR (ts = :cursor_ts AND host = :cursor_host AND new_state < :cursor_new_state).) Verification: confirm the ordering matches the declared PK in data-model.md §audit; if the PK ever changes, this task must change in lockstep.` Append test to T033: `TestAuditHandler_CursorPaginationStableAcrossSameMsEvents` (inject two audit rows with identical ts on two different hosts; assert both are returned exactly once across paginated requests — this catches any mixed-order predicate regression).
- **T2.14** (new counter test) — Extend T018 with `TestNewCounterRoundTripsThroughAllTiersAndHTTP`.
- **T2.15** (concurrent PUT /settings) — Extend T033 with `TestSettingsHandler_ConcurrentRetentionPUTIsSerialized`.
- **T2.16** (network share) — Add `research.md` §17 `Network-share data directory: only local fixed-disk volumes (NTFS / ReFS) are supported. SMB / UNC / mapped network drives are NOT supported — SQLite WAL's -shm shared memory file has undefined semantics on most SMB implementations. On Open() the service resolves the data-dir volume and, if it is not DRIVE_FIXED, refuses to start (not just "warn and continue") with a clear operator-actionable error naming the path and the policy (FR-004). A WARN log entry is written before the abort so the error is visible in Event Viewer.`. Add **T004c** `detect non-local data dir on Open() via GetDriveTypeW / DRIVE_FIXED check; refuse start with the operator-actionable error; tests TestOpen_RefusesNetworkShare AND TestOpen_AllowsLocalFixedDrive`. Note: this is a single policy (refuse on non-local) — the plan deliberately does NOT offer a separate "log warning and continue" mode, because WAL correctness cannot be guaranteed on SMB.

## 5. Commit 3: Tier-3 polish

Commit direction: `spec(007): tighten acceptance criteria from codex Tier-3 findings`.

- **T3.1** (SC-001) — T069: `run 5-day × 50-host × 15s simulator; assert per-host chart no gap > 2 sampling intervals.`
- **T3.2** (SC-004) — Add `TestRestart_LosesAtMostOneSamplingInterval` in T055.
- **T3.3** (SC-006) — T069: `record DB + WAL + SHM size after 5-day + 1-year seed; assert < 500 MB.`
- **T3.4** (SC-007) — T069: `measure dashboard latency p95/p99 while aggregator + retention run; assert p99 < 100 ms.`
- **T3.5** (FR-026 regression checklist) — Add to `specs/007-sqlite-telemetry-store/quickstart.md` a dedicated "Regression checklist" section covering: CLI history output shape (pre-feature vs post-feature same columns and ordering); dashboard empty-state render unchanged; Kerberos auth / negotiate flow unchanged; host-add flow unchanged; notification triggers unchanged. This edit is MANDATORY in Commit 3 — Commit 4 (operator docs) does not own this content; Commit 4 only adds operator-oriented content (backup recipe, network-share unsupported note, retention knob docs) and does not overlap with the regression checklist.
- **T3.6** (FR-032 log parity) — Extend T019 and T055 with `; asserts ≥ 1 ETW + file log record per run matches the maintenance_jobs row.`
- **T3.7** (spec wording) — `spec.md` US2 Independent Test (line 57) and FR-016 (line 165): `chronological` / `ordered chronologically` → `ordered newest-first (reverse chronological)`.
- **T3.8** (acceptance tightening) — US1: `continuous line without gap` → `no gap > 2 × sampling interval`; US3: `smooth transitions` → `re-render completes < 500 ms and next render starts within one animation frame`.
- **T3.9** (FR-027 doc comment) — T004: `; include a package-level doc comment stating the config.json mutex is separate from SQLite's WAL locking.`
- **T3.10** (SC-010 dumpbin) — T068: `; verify drainctl.exe has no new dependency on sqlite3.dll / mingw* / vcruntime* (dumpbin /dependents).`
- **T3.11** (SC-009 grep) — Add **T059a**: `verify removals are complete — ripgrep (rg -uu MemAuditStore) returns zero hits across the repo; rg -uu "history\s+map\[" internal/dashboard returns zero hits. (Use rg / Select-String — do NOT specify bash grep, since the project builds on Windows and contributors may not have a POSIX shell.)`

## 6. Commit 4 (optional): operator docs

- `README.md`: retention knobs, WAL-aware backup (T2.9), network-share unsupported (T2.16), UTC (T2.8), high-water size (T2.3).
- `docs/index.html`: mirror retention + endpoint list.
- `specs/007-sqlite-telemetry-store/quickstart.md` §4: replace `close the sqlite3 session` with the backup recipe.

Effort: **S**.

## 7. Deferred

- **T3.12 Multi-counter indexes** — at 6 counters the PK is sufficient. Revisit > 20 counters.
- **Full VACUUM INTO compaction task** — left as follow-up; high-water behavior documented.
- **CSV/JSON audit export** — already out-of-scope per spec Clarifications Q4.

## 8. Effort total

- Commit 1: 5×S + 3×M → **M+**
- Commit 2: 8×S + 5×M → **L-**
- Commit 3: 10×S + 1×M → **M**
- Commit 4: **S**
- **Grand total ≈ L** (one focused day of markdown editing for a single author).

## 9. Risks & interactions

- **Boot-sequence triangle (T1.1 + T1.2 + T1.6 + T1.7)** — all four edit `tasks.md` T025 and `research.md` §6. Do them in one pass with the combined wording (sequence + empty principal + oscillation rule + maintenance_jobs row). Re-read T025 end-to-end after each sub-edit; four separate passes will drop changes.
- **Aggregator coupling (T1.4 + T1.5)** — the watermark is the mechanism for both late-sample robustness and replacing the minute=0 gate. Editing §4 only for T1.4 leaves T1.5's gate language orphaned. One pass.
- **T2.16 vs T2.4 vs T1.3 — correct Open() sequence** — all three add startup-time constraints. The order inside `Open()` must be: `resolve path → local-fixed-volume check (T2.16) → open *sql.DB with the connector from T1.3 (PRAGMAs applied during Connect(), NOT as a separate later step) → PRAGMA integrity_check (T2.4) on one of the pool connections (pragmas already in effect) → apply ACL to the three files → return (*DB, nil)`. Do not treat "apply PRAGMAs" as a separate step after integrity_check; that contradicts T1.3's design. Integrity_check runs on an already-configured connection.
- **T2.11 chunked migration vs T1.1 ordering** — reconciliation MUST run only after `schema_meta[jsonl_migrated]=true`, not after any partial resume. During a mid-migration resume on startup, the boot sequence is `Open() → continue MigrateJSONL from jsonl_migrated_line_count → on EOF set jsonl_migrated=true → reconcile → live ingest`. Reconciliation never sees a partially-imported audit table. Capture this explicitly in T042 and T044 under Step 1.1.
- **T1.2 partial-index contract — correct durable guard** — `audit_principal WHERE principal <> ''` depends on reconciliation rows using empty string. The durable guard is NOT T3.9 (which is the FR-027 config-mutex doc comment). The actual guard is the test introduced under T032 (`TestReconcile_PrincipalIsEmpty`) asserting that every reconciliation row AuditStore.Append writes has `principal=""`, plus a constant `reconciliationPrincipal = ""` in `internal/telemetry/reconcile.go` referenced by both the writer and the test. Add this constant + test naming to Step 1.2 explicitly.
