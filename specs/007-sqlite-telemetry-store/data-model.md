# Phase 1 — Data Model

Schema lives in `internal/telemetry/schema.go` as an embedded DDL string applied on open. `PRAGMA user_version` tracks schema revisions; the first shipping version is `1`.

All timestamps are Unix milliseconds stored as `INTEGER`. All boolean-ish fields are `INTEGER` with `CHECK` constraints. Host names stored as `TEXT` without normalization to keep parity with the existing code paths.

---

## Table: `schema_meta`

Generic key/value store for schema-level state (migration marker, etc.). One row per key.

```sql
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
```

**Initial rows** (populated at first open):
- `schema_version` → `"1"`
- `jsonl_migrated` → `"true"` or `"false"`
- `created_at` → ISO-8601 UTC string at first open

---

## Table: `audit`

One row per drain-mode transition. Immutable after insert (FR-001b); only deletion path is the bulk retention job.

```sql
CREATE TABLE IF NOT EXISTS audit (
    ts              INTEGER NOT NULL,       -- Unix ms, event time
    host            TEXT    NOT NULL,
    prev_state      INTEGER NOT NULL,       -- 0..5 (DrainMode enum)
    new_state       INTEGER NOT NULL,       -- 0..5
    principal       TEXT    NOT NULL DEFAULT '',
    changed_by      TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT '',
    key_modified_ts INTEGER,                -- Unix ms, registry LastWriteTime if known
    reconciliation  INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation IN (0,1)),
    before_ts       INTEGER,                -- for reconciliation rows: last-known-good ts
    PRIMARY KEY (ts, host, new_state)
);

CREATE INDEX IF NOT EXISTS audit_host_ts   ON audit(host, ts DESC);
CREATE INDEX IF NOT EXISTS audit_ts        ON audit(ts DESC);
CREATE INDEX IF NOT EXISTS audit_principal ON audit(principal, ts DESC) WHERE principal <> '';
```

**Notes**:
- `PRIMARY KEY (ts, host, new_state)` survives duplicate inserts from an idempotent JSONL import.
- `audit_principal` is a partial index: skips empty principals (reconciliation rows, unknown actors).
- `reconciliation = 1` flags rows written by the startup drift detector (FR-001a). Query surface exposes them distinctly.
- No UPDATE path is exposed; the only access methods are `Append(rec)`, `QueryRange(from, to, filter)`, `DeleteOlderThan(threshold)`.

---

## Table: `metrics_raw`

One row per (host, ts, counter) full-resolution sample. Retention: 25 hours.

```sql
CREATE TABLE IF NOT EXISTS metrics_raw (
    ts      INTEGER NOT NULL,
    host    TEXT    NOT NULL,
    counter TEXT    NOT NULL,               -- 'cpu_pct', 'mem_avail_mb', 'active_sessions', …
    value   REAL    NOT NULL,
    PRIMARY KEY (host, ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_raw_ts ON metrics_raw(ts);
```

**Notes**:
- `WITHOUT ROWID` keeps the table tight; the primary key *is* the row layout.
- `PRIMARY KEY (host, ts, counter)` orders data on disk by host-then-time, which matches the dominant query pattern ("one host, a time window").
- `metrics_raw_ts` supports the retention sweep (`DELETE WHERE ts < ?`).
- Counter name as `TEXT` keeps the schema stable when DrainCtl adds a new counter — no ALTER required.

---

## Table: `metrics_5min`

One row per (host, 5-minute bucket, counter) with avg/min/max/sample_count. Retention: 6 days.

```sql
CREATE TABLE IF NOT EXISTS metrics_5min (
    bucket_ts    INTEGER NOT NULL,          -- Unix ms of bucket start (floor to 5 min)
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_5min_ts ON metrics_5min(bucket_ts);
```

---

## Table: `metrics_hourly`

One row per (host, hour bucket, counter). Retention: configured `metrics_days`.

```sql
CREATE TABLE IF NOT EXISTS metrics_hourly (
    bucket_ts    INTEGER NOT NULL,          -- Unix ms of hour start
    host         TEXT    NOT NULL,
    counter      TEXT    NOT NULL,
    avg_value    REAL    NOT NULL,
    min_value    REAL    NOT NULL,
    max_value    REAL    NOT NULL,
    sample_count INTEGER NOT NULL,
    PRIMARY KEY (host, bucket_ts, counter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS metrics_hourly_ts ON metrics_hourly(bucket_ts);
```

---

## Table: `maintenance_jobs`

One row per named background job; overwritten on each run (FR-029, Maintenance Job Run entity).

```sql
CREATE TABLE IF NOT EXISTS maintenance_jobs (
    name         TEXT PRIMARY KEY,          -- 'aggregator_5min', 'aggregator_hourly', 'retention'
    started_ts   INTEGER NOT NULL,
    finished_ts  INTEGER NOT NULL,
    duration_ms  INTEGER NOT NULL,
    outcome      TEXT    NOT NULL CHECK (outcome IN ('success','failure','skipped')),
    reason       TEXT    NOT NULL DEFAULT '',
    rows_affected INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
```

**Notes**:
- Written inside the same transaction that does the job's work when possible, so "last successful run" never lies.
- `name` is the authoritative job identifier; the UI renders whatever names it finds, so adding a new job later needs no dashboard change.

---

## Relationships & invariants

- `audit` is standalone — no FK to any other table. Drain state history is its own truth.
- `metrics_5min[host, bucket_ts, counter]` is deterministically derivable from a 5-minute window of `metrics_raw`. Hourly from 5-min. We never FK across tiers because the raw tier is purged before the hourly tier.
- `maintenance_jobs.name` is a closed set today: `{aggregator_5min, aggregator_hourly, retention, jsonl_migration, drift_reconciliation}`. Not enforced by CHECK so new jobs can appear without schema change.

## State transitions

| Entity | Transitions |
|---|---|
| Audit Record | `(absent) → inserted` (one-shot, immutable). Only retention may remove. |
| Metric Sample (raw) | `(absent) → inserted → [optionally purged]`. Never updated. |
| Metric 5-min Aggregate | `(absent) → inserted by aggregator`. Never updated. Deleted only by retention. |
| Metric Hourly Aggregate | same as 5-min. |
| Maintenance Job Run | `(absent) → inserted → overwritten on each run`. Only one row per `name`. |
| Migration Marker | `false → true` exactly once per install. |

## Storage footprint estimate

At 50 hosts × 6 counters × 15-second sampling:
- `metrics_raw`: ~1.44 M rows / 25 h ≈ 60 MB with primary-key indexing.
- `metrics_5min`: 6 days × 288 buckets/day × 50 hosts × 6 counters ≈ 518 k rows ≈ 25 MB.
- `metrics_hourly` at 30-day retention: 30 × 24 × 50 × 6 ≈ 216 k rows ≈ 10 MB.
- `audit` at 365-day retention + ~100 changes/day: ~37 k rows ≈ 3 MB.
- `maintenance_jobs`: ~5 rows total.

Total ≈ 100 MB. Well under the SC-006 budget of 500 MB.
