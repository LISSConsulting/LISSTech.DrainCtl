# Codex Review Synthesis — 007 SQLite Telemetry Store

**Date**: 2026-04-17
**Branch**: `007-sqlite-telemetry-store` (commit 85b42de)
**Scope**: Design review of planning artifacts only (no code yet)
**Files reviewed**: spec.md, plan.md, tasks.md, research.md, data-model.md, contracts/http-*.md, quickstart.md
**Reviewers**: codex-cli 0.121.0 (gpt-5.4), three parallel lenses (Consistency / Technical soundness / Completeness)

Raw findings: 47 (Lens A: 15, Lens B: 12, Lens C: 20). After deduplication and verification against the actual documents: 32 distinct items. Opus (me) verified each claim by re-reading the cited text.

## Classification key

- **CONFIRMED** — verified against the docs, real issue, worth fixing before implementation starts
- **PARTIAL** — real concern but narrower than codex phrased, or currently acceptable with a note
- **DUPLICATE** — already covered by another confirmed item
- **REJECTED** — hallucination, false positive, or pre-existing / out-of-scope (none this round)

## Tier 1 — Blockers (fix before implementing US1 / US2)

| ID | Severity | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| **T1.1** | BLOCKER | **Migration runs AFTER drift reconciliation** | tasks.md T025 runs reconciliation `after telemetry.Open() and before live ingest`; T044 runs `MigrateJSONL` `after telemetry.Open(), after drift reconciliation`. Drift reconciliation writes an audit row — so new audit events are written BEFORE legacy JSONL import, directly violating spec.md FR-020 ("import every record into the audit store before new events begin to be written"). Also, `AuditStore.LatestByHost` used by reconciliation sees an empty table, so the reconciliation baseline is wrong. | Swap the order. Correct boot sequence: `telemetry.Open()` → `MigrateJSONL` → drift reconciliation → live ingest. Updates needed in T025 and T044 wording, plus tasks.md `Implementation Strategy` if it cares. |
| **T1.2** | BLOCKER | **Reconciliation principal value: three sources, three values** | spec.md FR-001a: `principal unknown`. tasks.md T025: `principal=""`. research.md §6: `principal = "reconciliation"`. data-model.md has a partial index `audit_principal ... WHERE principal <> ''` that depends on empty-string semantics. contracts/http-audit.md example also uses `"principal": ""`. | Pick one. Recommend empty string (matches tasks + contract + partial-index intent). Fix research.md §6 and any stray wording in spec.md ("unknown" can stay as a concept with the operational value `""`). |
| **T1.3** | BLOCKER | **PRAGMAs may not apply to every pooled connection** | tasks.md T003/T004 use `*sql.DB` (Go's connection pool). research.md §2 says "on every open." But `busy_timeout`, `synchronous`, `temp_store`, `mmap_size`, `foreign_keys`, `wal_autocheckpoint` are connection-local in SQLite. If PRAGMAs are issued once on the first-acquired connection, later pool connections (dashboard reads, CLI queries, maintenance workers) won't have them → intermittent `database is locked`, unexpected sync behavior, wrong checkpoint cadence. | Use `sql.OpenDB(connector)` with a `driver.Connector` that applies PRAGMAs in every `Connect()` call, OR embed them in the DSN using modernc.org/sqlite's `_pragma=` form. Additionally pin writer to `SetMaxOpenConns(1)` so the single writer has a predictable connection with predictable PRAGMAs. Add a unit test `TestOpen_PragmasPersistAcrossPoolConnections`. |
| **T1.4** | BLOCKER | **Aggregator freezes on late raw samples** | research.md §4 + tasks.md T012/T035: aggregator uses `INSERT ... ON CONFLICT(host, bucket_ts, counter) DO NOTHING`. Once a bucket row exists, any later raw sample with a timestamp in that bucket is silently ignored — the aggregate never reflects it. This happens whenever a raw write lands after the bucket's aggregator pass (GC pause, slow disk, burst ingest). Breaks FR-008 "aggregation must be idempotent" in spirit: re-running over more complete raw data should produce the correct aggregate, not freeze the first attempt's values. | Introduce a watermark: aggregator only materializes buckets whose upper bound is older than a safety lag (e.g., 5 min past the bucket's end), AND switch to `ON CONFLICT DO UPDATE SET avg_value=..., sample_count=...` keyed on the current full bucket contents. Update T012 and T035 to spell out the watermark and the UPDATE path. Test: `TestAggregator_LateSampleUpdatesBucket`. |
| **T1.5** | BLOCKER | **Hourly aggregator uses `minute=0` wall-clock gate** | tasks.md T012: `gated to run only on minute=0 ticks`. If a 60-second tick drifts even 1s (GC, NTP adjust, OS scheduling), the equality check misses — that hour never runs. Next hour's pass doesn't catch up unless the logic also scans older incomplete buckets. | Replace the gate with "on every pass, materialize every hourly bucket whose end-boundary + watermark is ≤ now and which does not yet exist in `metrics_hourly`." No wall-clock equality. Works out of the same implementation approach T1.4 requires. |
| **T1.6** | CONFIRMED | **Drift reconciliation cannot detect A→B→A oscillation** | research.md §6 + tasks.md T025: reconciliation fires only when current registry state ≠ last audit `new_state`. If drain toggled on→off→on while service was down, current state matches last audit → no drift row. data-model.md already stores `key_modified_ts` (registry LastWriteTime); the algorithm doesn't use it. | Extend reconciliation: ALSO emit a "possible downtime activity" row when registry `LastWriteTime > last_audit.key_modified_ts`, regardless of whether states match. The row marks an uncertainty window, not a specific transition. Updates to research.md §6 + tasks.md T025 + test list in T032. |
| **T1.7** | CONFIRMED | **`drift_reconciliation` maintenance_job row never written** | data-model.md line 151 + contracts/http-maintenance.md line 61 both list `drift_reconciliation` in the closed set of job names. No task writes a `maintenance_jobs` row for it (T025 writes the audit row; T045 writes `jsonl_migration`, T048 aggregators, T049 retention). | Add to T025 the maintenance_jobs upsert. Alternatively, drop `drift_reconciliation` from the closed set and contract if it's deemed not worth surfacing — but since the dashboard widget explicitly lists five job names, adding the row is cheaper than doc-churn. |
| **T1.8** | CONFIRMED | **`expected_interval_seconds` meaningless for one-shot jobs** | contracts/http-maintenance.md line 67-68: `overdue = server_time - finished > 2 * expected_interval_seconds` for every job. But `jsonl_migration` and `drift_reconciliation` are one-shot startup jobs — they have no repeating interval, so `2 * interval` makes them *always overdue* after the interval window. | Make `expected_interval_seconds` nullable in the contract and in the data model (or use `0` = "N/A"). Update overdue formula in T051: `overdue = false when expected_interval_seconds <= 0`. Fix http-maintenance.md response schema wording. |
| **T1.9** | CONFIRMED | **plan.md retention math contradicts every other doc** | plan.md line 20: `≈ 8.6M raw rows per 5 days before downsample purge`. research.md §3 + data-model.md + tasks.md all say raw retention is **25 hours**. The 8.6M/5-day figure is wrong (actual is ~1.44M / 25h) and misleading to future readers planning capacity. | One-line edit in plan.md Technical Context: change to `≈ 1.44M raw rows in the 25h retention window` (matches data-model.md storage footprint estimate). |

## Tier 2 — Operational / robustness concerns (fix before ship, not before start)

| ID | Severity | Finding | Fix direction |
|---|---|---|---|
| **T2.1** | HIGH | **`synchronous=NORMAL` does not guarantee FR-003's "committed = survives"** for OS crash / power loss | Choose one: (a) upgrade audit writes to `synchronous=FULL` (audit volume is low); or (b) soften FR-003 to explicitly tolerate ≤1 sampling interval on power loss (already the SC-004 reality). Update research.md §2 with the decision. |
| **T2.2** | HIGH | **WAL file can grow unbounded under long readers** | research.md §2 + §5 don't spell out checkpoint policy. Add: explicit `PRAGMA wal_checkpoint(TRUNCATE)` on a cadence, a read-timeout strategy for old snapshots (e.g., max read tx = 60s), and WAL size monitoring that alerts past a threshold. |
| **T2.3** | HIGH | **`incremental_vacuum` does not reclaim OS disk space** | research.md §5 implies it frees space to the OS. It only frees to SQLite's internal free-list; file size stays at high-water mark. Acknowledge the high-water behavior explicitly in research.md and (optionally) add an off-hours `VACUUM INTO`-based compaction task. Update operator docs in README / quickstart to set expectations. |
| **T2.4** | HIGH | **Corruption detection untested** | Add `PRAGMA integrity_check` on startup behind a flag (off-hours or first-open). Add test `TestOpen_CorruptFileReturnsClearError` using a deliberately corrupted fixture. spec.md Edge Cases already says "refuse to start with a clear error" — make that observable. |
| **T2.5** | HIGH | **Disk-full path untested** | Add injected-error test `TestAppend_DiskFullIsGracefullyHandled` for both audit and metrics writers. The spec already says "continue running and recover automatically when space returns" — test it. |
| **T2.6** | HIGH | **Host clock skew behavior unverified** | spec.md edge case says future-dated samples are stored. Add `TestAppend_FutureTimestampStoresAndLogs` asserting the sample lands AND a warning is logged. |
| **T2.7** | HIGH | **Retention shrink (30d → 1d) through dashboard** | Add integration test: seed 30 days of data, PUT settings with `metrics_days=1`, run the retention worker once, assert bounded chunked purge completed and dashboard responsiveness stayed < 100 ms. Either add a task or extend T055. |
| **T2.8** | HIGH | **UTC vs local for bucket boundaries not stated** | data-model.md uses Unix ms (UTC-neutral). But aggregator's bucket-floor math (e.g., floor to 5 min / hour) must specify UTC flooring. Add explicit requirement to research.md §4. Add DST crossover test. |
| **T2.9** | HIGH | **WAL-aware backup procedure missing** | quickstart.md §4 says "close the sqlite3 session"; research.md §15 "rollback plan" implies copying the DB. Neither covers `-wal` / `-shm` safety. Add a documented procedure using SQLite's online backup API (or a `PRAGMA wal_checkpoint(TRUNCATE)` + copy-three-files recipe) and smoke-test in quickstart. |
| **T2.10** | HIGH | **Version downgrade after feature ships** | research.md §15 rollback assumes the `.bak` JSONL is restored. But if the operator runs v27 for weeks accumulating new audit rows and then downgrades to v26, those rows are invisible to v26 (which only reads JSONL). Either add an "export audit to JSONL on-demand" tool, OR document explicitly that downgrade loses post-migration audit events. |
| **T2.11** | MEDIUM | **100 MB JSONL migration single transaction** | research.md §7 imports everything in one tx. Consider chunked import with intermediate commits and a progress marker, which matches FR-021 idempotency and avoids large-WAL rollback costs. |
| **T2.12** | MEDIUM | **CLI read-only WAL sidecar handling underspecified** | research.md §13 says CLI opens read-only. But behavior when `-wal`/`-shm` are missing, stale, or inaccessible isn't specified. Add explicit mode (e.g., SQLite URI `?mode=ro&_journal_mode=wal&_txlock=deferred`) + tests for existing / absent / inaccessible sidecars. |
| **T2.13** | MEDIUM | **Audit pagination on `rowid` is fragile** | audit table is NOT WITHOUT ROWID, so rowid exists. Pagination keyed on `(ts DESC, rowid)` works today but breaks if audit ever becomes WITHOUT ROWID. Prefer cursor on the declared primary key: `(ts, host, new_state)`. One-line edit to tasks.md T023. |
| **T2.14** | MEDIUM | **New counter round-trip untested** | data-model.md says counter is TEXT so new counters "just work." Add a unit test: ingest a `gpu_pct` counter, verify it materializes to 5min and hourly, renders through /api/v1/metrics with no schema change. |
| **T2.15** | MEDIUM | **Concurrent PUT /api/v1/settings for retention fields** | research.md §12 adds new retention knobs to config.json. Existing named mutex covers config.json, but no task verifies the retention fields are wired through the same scoped-updater path or tests concurrent PUTs. Add an HTTP test. |
| **T2.16** | MEDIUM | **Network-share data directory not addressed** | FR-004 allows the data dir to be configurable. SQLite WAL has caveats on SMB/UNC. Declare supported/unsupported explicitly and either refuse or warn on startup when the data dir is on a non-local volume. |

## Tier 3 — Acceptance-criteria gaps & spec polish

| ID | Severity | Finding | Fix direction |
|---|---|---|---|
| **T3.1** | MEDIUM | SC-001 "unbroken 5-day chart" not validated at 50 hosts × 15s raw cadence | Extend T069 or add new task: run the service (or a simulator) at full cadence for 5 days on a fixture host set, assert per-host chart has no gap > 2 sampling intervals. |
| **T3.2** | MEDIUM | SC-004 "≤ 1 sampling interval loss" not quantitatively tested | Add `TestRestart_LosesAtMostOneSamplingInterval` — drive ingest, SIGKILL mid-flight, restart, compare pre/post timelines, assert ≤ 1 interval gap per host. |
| **T3.3** | MEDIUM | SC-006 "< 500 MB" not measured | Extend T069 to record disk size (DB + WAL + SHM) after seeding 5 days raw + 1 year audit. |
| **T3.4** | MEDIUM | SC-007 "no pause > 100 ms" not measured | Add to T069: while aggregator + retention are running, measure dashboard latency p95/p99; assert p99 < 100 ms. |
| **T3.5** | MEDIUM | FR-026 "no operator-visible regression" is untestable as written | Add a regression checklist to quickstart (or a new T067.x): CLI history output shape stable, dashboard empty state unchanged, auth flow unchanged, host-add flow unchanged, existing notification triggers unchanged. |
| **T3.6** | MEDIUM | FR-032 "ETW + file logs continue in parallel with UI" not tested | Add tests around aggregator + retention that assert at least one log record lands per run and matches the `maintenance_jobs` row. |
| **T3.7** | LOW | "Chronological order" in spec.md FR-016 + US2 vs "ts DESC" in contract | Spec wording bug. Replace "chronological" with "reverse chronological" or "ordered newest-first" to match the contract. |
| **T3.8** | LOW | "Continuous line" / "smooth transitions" in US1/US3 Independent Tests are subjective | Tighten: "no gap > 2 × sampling interval" for US1, "re-render completes < 500 ms and next render starts within one animation frame" for US3. |
| **T3.9** | LOW | FR-027 (config mutex ≠ SQLite locking) has no task | Already noted in tasks.md Notes. Optionally add a one-line doc comment requirement to the package-level doc in T004 so the intent survives code review. |
| **T3.10** | LOW | SC-010 "no extra runtime" — T068 MSI smoke covers implicitly but doesn't assert | Extend T068 acceptance: confirm `drainctl.exe` has no dependency on `sqlite3.dll`, `mingw*`, or `vcruntime*` beyond what ships today. |
| **T3.11** | LOW | SC-009 "storage paths drop" not verified after removals | T014 + T059 + T060 do the deletes. Add a one-line verification task: `grep -r MemAuditStore` returns zero hits, `grep -r "history map\[" internal/dashboard` returns zero, etc. |
| **T3.12** | LOW | Metrics indexes for multi-counter filtering | Acceptable at 6 counters; revisit only if counter count grows. No action. |

## Rejected claims

None this round — codex was well-grounded. The only quasi-rejections are redundancies (A4/A5/B8 all described the same migration-ordering bug; A10/C1, A11/C2, A12/C3, A13/C4 overlap across lenses) which were deduplicated upward into Tier 1/Tier 2.

## Counts

- Total raw findings: 47
- Deduplicated: 32
- Tier 1 (blockers): 9
- Tier 2 (operational): 16
- Tier 3 (acceptance / polish): 12 (= 37 totals; T3 items include some absorbed duplicates)
- Rejected: 0

## What this means for /speckit-implement

The Tier 1 blockers are spec/design bugs that *will* cause wrong behavior or broken releases if implemented as-written. They should be fixed in spec.md / research.md / tasks.md before code is written, so the plan the implementer follows is self-consistent.

Tier 2 items are things you'd want fixed before the feature ships but can be scheduled alongside implementation — most are additional tasks or test requirements, not changes to the core design.

Tier 3 is spec polish / test-coverage hardening that can absorbed into Polish phase or deferred.

Proceeding to Stage 3 — remediation plan.
