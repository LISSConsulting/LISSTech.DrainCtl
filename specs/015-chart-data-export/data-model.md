# Data Model: Excel and CSV Graph Data Export

This file defines the in-memory shape the export pipeline produces. It is the contract between the dashboard query layer and the CSV / XLSX writers.

## Entities

### Graph Snapshot (immutable)

The export is built from exactly one snapshot. A snapshot is bound to a graph instance (Overview Load, HIC chart X, per-host Load, etc.), the dashboard's current host filter, the requested time range, the tier that the underlying metrics query actually served, and the union of series that the user has not hidden via the chart legend.

| Field | Type | Source | Notes |
|---|---|---|---|
| `graph_id` | string | component config | Stable internal id (e.g. `overview.load`, `host.load`, `hic.input_delay`) |
| `graph_label` | string | component config | Human-readable label (e.g. "Overview Load", "Per-host Load") |
| `graph_slug` | string | derived | `graph_label` lowercased, non-alphanumeric → `-`, collapsed, trimmed. Used in filenames. |
| `export_type` | enum | derived | `fleet` or `per_host` — set by the route handler based on whether the graph's data source is `_fleet` or a specific host |
| `host_name` | string \| null | derived | Per-host exports only; the canonical hostname from the registered server list |
| `host_filter` | []string \| null | derived | Fleet exports only; resolved host names from the dashboard's host filter, deduplicated, in registration order |
| `host_filter_count` | int | derived | Length of `host_filter` (0 if nil) |
| `tier` | string | metrics query | `"raw"`, `"1min"`, `"5min"`, or `"hourly"` — whatever the existing tier resolver returned |
| `from` | time.Time | metrics query | Inclusive lower bound, UTC |
| `to` | time.Time | metrics query | Exclusive upper bound, UTC |
| `display_timezone` | string | operator config | Currently always `UTC`; recorded so the file remains meaningful when the operator's display zone differs |
| `coverage` | enum | metrics query | `"full"`, `"partial"`, or `"empty"` |
| `coverage_note` | string | metrics query | Free-form note when `coverage != "full"` (e.g. "requested window fully covered", "oldest 2026-04-10T12:00:00Z") |
| `series` | []SeriesDefinition | graph config | Series the user has not hidden (legend toggles) |
| `rows` | []Observation | metrics query | The chart data, expanded to one row per host × timestamp × series |

### Series Definition

| Field | Type | Notes |
|---|---|---|
| `series_id` | string | Stable id matching the underlying metric counter name (e.g. `cpu_pct`, `session_cpu_p95_pct`) |
| `series_label` | string | Human-readable label (e.g. "CPU p95", "Memory used") |
| `unit` | string | Canonical unit (e.g. `%`, `MB`, `ms`); empty string when dimensionless |
| `aggregation` | string | The aggregation recorded for this series at the snapshot tier (`"avg"`, `"p95"`, `"max"`, `"raw"`, etc.) |
| `resolution` | string | The bucket resolution applied at the snapshot tier (e.g. `"1m"`, `"5m"`, `"1h"`, `"raw"`) |

### Observation

One row per (timestamp × series × host_name). For per-host exports, host_name is constant. For fleet exports, host_name identifies the contributing host.

| Field | Type | Notes |
|---|---|---|
| `timestamp` | time.Time | UTC; the bucket boundary |
| `host_name` | string | Canonical hostname; from the registered server list when known, otherwise the raw value the metrics query returned |
| `series_id` | string | From the parent Series Definition |
| `series_label` | string | From the parent Series Definition |
| `unit` | string | From the parent Series Definition |
| `value` | float64 \| nil | The metric value at the bucket; `nil` for missing observations (rendered as empty cell) |
| `aggregation` | string | From the parent Series Definition |
| `resolution` | string | From the parent Series Definition |

### Context Block (Excel only)

The Excel Context sheet uses a fixed Field/Value two-column layout. The fields, in order:

| Field | Type | Fleet | Per-host | Source |
|---|---|---|---|---|
| `graph` | string | ✓ | ✓ | snapshot.graph_label |
| `export_type` | enum | ✓ | ✓ | snapshot.export_type |
| `host_name` | string | — | ✓ | snapshot.host_name |
| `host_filter` | string (newline-delimited) | ✓ | — | snapshot.host_filter joined by `\n` |
| `host_filter_count` | int | ✓ | — | snapshot.host_filter_count |
| `series` | string | ✓ | ✓ | Comma-separated `<series_id> (<label>, <unit>, <aggregation>, <resolution>)` |
| `time_range_start_utc` | string | ✓ | ✓ | snapshot.from RFC3339 |
| `time_range_end_utc` | string | ✓ | ✓ | snapshot.to RFC3339 |
| `display_timezone` | string | ✓ | ✓ | snapshot.display_timezone |
| `aggregation` | string | ✓ | ✓ | Coarse summary at file level (e.g. "avg (per series, over host_filter cohort)" or "raw (per-host samples)") |
| `resolution` | string | ✓ | ✓ | Coarse summary at file level (e.g. "1m", "5m", "raw") |
| `coverage` | enum | ✓ | ✓ | snapshot.coverage |
| `coverage_note` | string | ✓ | ✓ | snapshot.coverage_note |
| `exported_at_utc` | string | ✓ | ✓ | Now, UTC, RFC3339 |
| `generator` | string | ✓ | ✓ | `"DrainCtl v" + git-derived version` (uses the same `scripts/version.ps1` the build pipeline uses) |

## Ordering

- Observation rows are sorted chronologically by `timestamp`. Within the same timestamp, sort by `(series_id, host_name)` (lexicographic, both ascending). This is the deterministic tiebreaker required by FR-006.
- Series Definitions are sorted by the chart component's declaration order, then by `series_id` for ties. This makes the per-row column content predictable for downstream consumers.

## Validation Rules (applied by the writer, fail-loud)

| Rule | Failure mode |
|---|---|
| `from < to` | 400 `invalid_range` (re-using the existing error contract) |
| `len(rows) <= 50_000` | 413 `payload_too_large` with explanatory body |
| Every `series` element referenced by an Observation must exist in the snapshot's `series` list | internal panic — indicates a bug, not user input |
| `host_filter_count == len(host_filter)` | internal panic |
| `export_type == "fleet"` ⇒ `host_filter` non-nil; `export_type == "per_host"` ⇒ `host_name` non-nil | 500 — route handler bug |

## Type/Format Conventions

| Field in file | CSV format | Excel format |
|---|---|---|
| `timestamp_utc` | ISO 8601 with `Z` suffix (e.g. `2026-09-30T14:00:00Z`) | ISO 8601 with `Z` suffix (text cell) |
| `value` | Empty for nil; otherwise Go's default `strconv.FormatFloat` with `'f'` and precision -1 (preserves precision without forcing scientific notation) | Empty cell for nil; numeric cell with `number_format = "General"` otherwise |
| `host_name`, `series_label`, `unit`, `aggregation`, `resolution` | Unescaped per CSV (the writer handles quoting) | Shared-string cell via `excelize.SetCellStr` — formula-safe by construction |
| `series_id` | As above | As above |
| Context sheet `host_filter` | n/a (CSV has no Context) | Single cell, wrap-text on, `\n`-delimited |
| Context sheet `series` | n/a | Single cell, wrap-text on, comma-separated entries |
| Context sheet `exported_at_utc`, all time fields | n/a | ISO 8601 with `Z` suffix, shared-string cell |
