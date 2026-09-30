# Implementation Plan: Fleet Session Operator Metrics

**Branch**: `015-fleet-session-operator-metrics` | **Date**: 2026-09-29 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/015-fleet-session-operator-metrics/spec.md`

## Summary

Replace the Overview Sessions host-summary percentile charts with anonymous, session-weighted fleet CPU and memory P95/AVG metrics derived from accepted current-session snapshots. Persist one replaceable per-host aggregate per one-minute base bucket, including sums, counts, bounded-error mergeable histograms, CPU threshold counts, and collection coverage. Roll those aggregates through the existing raw/5-minute/hourly retention lifecycle, merge only selected hosts at query time, expose the results in the fleet metrics API, and render truthful coverage and breadth context without retaining session identity.

## Technical Context

**Language/Version**: Go 1.26.2; Svelte 5; JavaScript; Vite 8.
**Primary Dependencies**: `modernc.org/sqlite`, Go standard library, Svelte shared chart components.
**Storage**: Existing `drainctl.db` in WAL mode. Add schema-v5 per-host `session_workload_raw`, `session_workload_5min`, and `session_workload_hourly` tables. Histograms are anonymous binary aggregate blobs.
**Testing**: Focused Go unit/integration/contract tests, frontend Vitest tests, browser smoke against the embedded dashboard, `go test ./...`, `just lint`, and `prek`.
**Target Platform**: Windows Server service and its authenticated browser dashboard.
**Project Type**: Single Go module with embedded Svelte frontend.
**Performance Goals**: Initial Overview retained render under two seconds at 16 hosts/~614 daily users; filter/window updates under one second on LAN; bounded row cardinality through tiering.
**Constraints**:
- New Go files carry `//go:build windows`.
- No raw historical session rows or identity fields in workload storage/API/logs.
- CPU P95 error ≤0.5 percentage point; memory P95 error ≤2% relative.
- One newest successful host snapshot per one-minute base interval; fatal attempts replace that interval with error coverage.
- AVG is pooled sum/count; P95 is merged-distribution percentile; neither averages host summaries.
- Existing Sessions Trend and Utilization remain unchanged.
- Old history remains a gap; no alias or reinterpretation of legacy counters.
**Scale/Scope**: Up to 16 hosts, 500 sessions per host snapshot, 25h raw, 6d 5-minute, configured metrics-days hourly retention.

## Constitution Check

*GATE: Passed before research and re-checked after design.*

- **Windows-first delivery**: PASS. New backend files are Windows-tagged. Service snapshot ingestion, SQLite telemetry, dashboard API, embedded UI, and operator docs change. Root package, CLI, DLL/interop, PowerShell, and installer behavior are intentionally unchanged.
- **Stable operator surfaces**: PASS. `GET /api/v1/metrics/_fleet` receives an additive `session_workload` object. Overview labels/help/tooltips change as specified. Existing generic `series`, commands, config, and automation output remain unchanged. No compatibility alias preserves the old misleading chart semantics.
- **Tests and zero-noise verification**: PASS. Histogram error, normalization, replacement, rollup, selected-host, coverage, API, privacy, and frontend adapter/tooltip behaviors receive automated tests. Completion requires focused tests, `go test ./...`, frontend tests/build, `just lint`, `prek`, and browser smoke.
- **Config and release discipline**: PASS. No new setting. Schema v5 is additive and idempotent. Anonymous workload rows follow existing metric retention. Current-session retention/privacy purges do not delete anonymous workload history. Rollback leaves additive tables ignored by older binaries. Release remains git-derived.
- **Operational observability**: PASS. The API and tooltips expose expected/contributing hosts, metric-specific observation counts, and omitted-host categories. Persistence/query/rollup failures use bounded structured logs without identities or per-session values.

**Post-design gate result**: Pass. The dedicated workload tables are justified because generic scalar metric rows cannot merge percentile distributions or replace a host contribution within a base interval.

## Project Structure

### Documentation (this feature)

```text
specs/015-fleet-session-operator-metrics/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   └── http-fleet-session-workload.md
└── tasks.md
```

### Source Code (repository root)

```text
internal/
├── sessiondata/
│   └── workload.go                       # anonymous aggregate + bounded histograms
├── telemetry/
│   ├── migrate_session_workload.go       # schema v5
│   ├── session_workload.go               # persistence/query/merge
│   ├── session_store.go                  # atomic ingest integration
│   ├── aggregator.go                     # workload 5-minute/hourly rollups
│   └── retention.go                      # workload tier retention
└── dashboard/
    ├── handlers_metrics.go               # additive fleet workload response
    ├── interfaces.go                     # workload query boundary
    └── openapi.yaml                      # public contract

frontend/src/
├── lib/
│   ├── api.js                            # response typings
│   └── chart-data.js                     # workload adapter
└── components/
    ├── MetricsChart.svelte               # labels/help/coverage wiring
    └── chart/InteractiveTimeChart.svelte # tooltip metadata support if needed

README.md
CHANGELOG.md
```

**Structure Decision**: Keep the single module and existing telemetry/database owner. The workload representation belongs in `sessiondata` because it derives from wire snapshots without identity; storage/query and rollups remain in `telemetry`; the existing fleet metrics handler remains the only Overview history API.

## Design Decisions

1. **One-minute server-receipt buckets**: server receipt time avoids agent-clock skew and matches the default telemetry cadence. `(host, bucket_ts)` is unique. Every accepted attempt upserts the row; a newer successful snapshot replaces the prior aggregate, while a newer fatal attempt replaces it with error coverage so stale success is not carried forward.
2. **CPU histogram**: normalized CPU is clamped only after validation to `0..100`, quantized to nearest 0.5 percentage point, and stored in 201 fixed bins. Quantile reconstruction therefore has at most 0.25-point quantization error, below the 0.5-point requirement.
3. **Memory histogram**: zero has a dedicated bucket; positive bytes use logarithmic bins with adjacent representative ratio no greater than 1.02. Sparse delta-varint encoding retains only aggregate bin counts and guarantees no more than 2% representative error.
4. **Tier rows remain per host**: rollups merge each host's base rows and retain sum/count/distribution plus base-sample count, average/max CPU threshold counts, and success/error coverage. Query-time host filtering therefore remains exact for selected hosts.
5. **Coverage semantics**: successful rows contribute host coverage even when empty; fatal rows contribute error coverage and no measurements; selected hosts with no workload row are classified from current server state as stale/offline where available and otherwise unsupported. Zero successful contributors produces no numeric point.
6. **API isolation**: new workload data is a top-level `session_workload` object rather than overloading generic `CounterSeries` fields. Legacy generic session counters remain solely for unchanged Sessions Trend and Utilization.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Dedicated three-tier workload tables | Mergeable distributions and host-replaceable samples do not fit scalar EAV rows | Storing only AVG/P95 would make rollups and selected-host P95 mathematically incorrect; raw per-session history violates privacy |
| Custom bounded histograms | Required percentile accuracy must survive host/time merges without raw observations | External sketch dependency is unnecessary and less auditable; fixed/log bins provide explicit deterministic bounds |
