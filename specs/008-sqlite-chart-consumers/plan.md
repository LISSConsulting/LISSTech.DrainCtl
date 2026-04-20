# Implementation Plan: Remaining Persistent Telemetry Consumers

**Branch**: `008-sqlite-chart-consumers` | **Date**: 2026-04-20 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/008-sqlite-chart-consumers/spec.md`

## Summary

Replace the Overview page's browser-local history sources (`metricsHistory`,
`sessionHistory`, `remoteFxHistory`) with durable SQLite-backed queries, preserving the
current chart semantics and visuals while adding a shared 5M/1H/1D/3D/5D window model,
zoom, pan, and scroll. Review the remaining dashboard history consumers and migrate the
ones already backed by existing telemetry, most notably the per-host sparkline seed path,
so a fresh browser session no longer depends on warmed localStorage to show retained
history.

Technical approach in one sentence: extend the existing dashboard metrics API with a
fleet sentinel query and a production sparkline-seed route, centralize shared Overview
time-window state in the frontend, and keep SQLite as the only authoritative history
source for all migrated dashboard surfaces.

## Technical Context

**Language/Version**: Go 1.26.2 for the service/dashboard backend; Svelte 5 + Vite 8 for the embedded dashboard frontend.
**Primary Dependencies**: `modernc.org/sqlite`, `cobra`, `golang.org/x/sys`, `layercake`, `lucide-svelte`.
**Storage**: Existing SQLite telemetry store (`drainctl.db`, WAL mode) plus transient browser-local UI state that is not authoritative for retained history.
**Testing**: `go test ./...`, focused `internal/dashboard` handler/store tests, frontend manual validation in the embedded dashboard, `just lint`, and pre-commit (`prek`).
**Target Platform**: Windows Server service plus browser-based dashboard clients on the same authenticated operator network.
**Project Type**: Single Go module with embedded Svelte dashboard served from `internal/dashboard` and `frontend/`.
**Performance Goals**: Overview initial retained render in under 2 seconds on a cold LAN client; time-window changes in under 1 second; no stale response wins after rapid zoom/pan gestures.
**Constraints**:
- No new telemetry class unless implementation proves an existing requested chart cannot be derived from already-stored counters.
- Preserve current Overview aggregation semantics exactly: LOAD stays fleet average + CPU P95; HIC, SESSIONS, and REMOTE FX keep their current fleet rollups.
- Shared Overview window across all chart families; a missing-data chart stays visible with explicit empty or error state.
- Retained telemetry remains authoritative; browser-local history cannot be used as fallback truth.
- New or moved Go files must carry `//go:build windows`; config remains in `config.json`; release version remains git-derived.
**Scale/Scope**: Dashboard API additions in `internal/dashboard/server.go` and tests, frontend rewiring in `frontend/src/App.svelte`, `frontend/src/lib/api.js`, `frontend/src/lib/state.svelte.js`, `frontend/src/components/MetricsChart.svelte`, `frontend/src/components/ServerTable.svelte`, `frontend/src/components/ServerDetail.svelte`, and related chart helpers. No installer, CLI, DLL, or schema migration expected by default.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Windows-first delivery**: PASS. Backend changes stay in the Windows service and
  embedded dashboard server. Any new Go code will live under `internal/dashboard/` and
  carry `//go:build windows`. Affected operator surfaces: dashboard and service query
  endpoints. Root package, CLI, DLL/interop, PowerShell, and installer remain unchanged.
- **Stable operator surfaces**: PASS. Public behavior changes are confined to dashboard
  APIs and UI behavior: `GET /api/v1/metrics/{host}` grows a `_fleet` sentinel contract,
  `GET /api/v1/metrics` becomes a production sparkline-seed route, and Overview charts
  stop treating browser-local history as authoritative. No command-line or config-field
  migration is required.
- **Tests and zero-noise verification**: PASS. Required verification: backend handler
  and tier-selection tests, frontend interaction validation for shared-window behavior and
  empty/error states, `go test ./...`, and `just lint`. The feature crosses storage,
  service, and dashboard boundaries, so both unit and integration-level tests are
  required.
- **Config and release discipline**: PASS. No new config system, retention class, or
  release artifact is introduced. The feature reuses the existing telemetry store and
  current retention policy. If implementation discovers a missing counter that forces a
  telemetry schema change, that will be recorded explicitly in Complexity Tracking.
- **Operational observability**: PASS. Success and failure remain observable through
  dashboard states (retained data, empty, unavailable, query error) and existing backend
  `storage_error` logging paths in `internal/dashboard/server.go`. No silent fallback to
  local history is permitted.

**Gate result**: Pass. No constitutional violations are currently justified.

## Project Structure

### Documentation (this feature)

```text
specs/008-sqlite-chart-consumers/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── http-fleet-metrics.md
│   └── http-metrics-seed.md
└── tasks.md
```

### Source Code (repository root)

```text
frontend/
├── src/
│   ├── App.svelte                         # poll bootstrap + Overview shared window wiring
│   ├── lib/
│   │   ├── api.js                         # dashboard HTTP client contracts
│   │   ├── chart.svelte                   # existing per-host durable chart behavior
│   │   └── state.svelte.js                # current local history state to retire or narrow
│   └── components/
│       ├── MetricsChart.svelte            # LOAD/HIC/SESSIONS/REMOTE FX overview charts
│       ├── ServerTable.svelte             # per-host sparkline consumer candidate
│       ├── ServerDetail.svelte            # fallback historical consumer candidate
│       └── chart/
│           ├── InteractiveTimeChart.svelte
│           ├── DualAxisChart.svelte
│           └── MiniHealthChart.svelte

internal/
├── dashboard/
│   ├── server.go                          # existing /api/v1/metrics/{host}, add fleet + seed routes
│   ├── server_test.go                     # handler tests for retained history APIs
│   ├── store.go                           # registered-host state, no durable history authority
│   └── client.go                          # dashboard report ingest path (unchanged unless seed needs it)
└── telemetry/
    ├── metrics.go                         # existing tiered metrics query surface reused
    ├── aggregator.go                      # unchanged unless research finds a missing aggregate
    └── *_test.go

AGENTS.md                                  # plan pointer between SPECKIT markers
```

**Structure Decision**: Keep the existing single-module + embedded-dashboard layout.
Most work lands in `internal/dashboard/server.go` and the frontend Overview state and
chart components. Reuse `internal/telemetry/metrics.go` and the existing `/api/v1/metrics/{host}`
response shape rather than introducing a second storage stack or new dashboard service.

## Historical Consumer Inventory (T001 review — 2026-04-20)

### In-Scope: Migrate to Retained Telemetry

| Surface | Variable | File | Accumulation | Persistence | Reads | Migration Path |
|---------|----------|------|--------------|-------------|-------|----------------|
| Fleet LOAD + HIC charts | `metricsHistory` | `state.svelte.js` → `MetricsChart.svelte` | `appendMetricsSample()` every 30 s; synthesized from mock seed on cold start | localStorage (`drainctl:metrics`) | `MetricsChart` LOAD and HIC tabs | Replace mock seed + live accumulation with `fetchMetrics('_fleet', ...)` retained query |
| Fleet SESSIONS charts | `sessionHistory` | `state.svelte.js` → `MetricsChart.svelte` | `appendSessionSample()` every 30 s; synthesized from mock seed on cold start | localStorage (`drainctl:session-history`) | `MetricsChart` Session Metrics tab | Same fleet sentinel query, session counters |
| Fleet REMOTE FX charts | `remoteFxHistory` | `state.svelte.js` → `MetricsChart.svelte` | `appendRfxSample()` every 30 s; synthesized from mock seed on cold start | localStorage (`drainctl:rfx-history`) | `MetricsChart` Remote FX tab | Same fleet sentinel query, RFX counters |
| Per-host sparklines | `serverMetrics[host]` | `state.svelte.js` → `ServerTable.svelte`, `ServerDetail.svelte` | `appendServerMetricsSample(host, …)` every 30 s + SSE perf events; seeded via mock `fetchAllServerMetrics()` on cold start | localStorage (`drainctl:server-metrics`) | `ServerTable` sparkline columns; `ServerDetail` sparkline row | Replace mock seed with production `GET /api/v1/metrics` seed route; live accumulation from poll/SSE continues as before |

### Confirmed Deferred: No Migration Needed

| Surface | Variable | Why Deferred |
|---------|----------|--------------|
| Per-host anomaly spikes | `recentSpikes[host]` | Already server-authoritative: seeded from `fetchRecentSpikes()` REST on `ServerDetail` expand; updated live via SSE `recent_spike` events. Not localStorage-persisted. No migration required. |
| Per-host detector status | `detectorStatuses[host]` | Already transient and server-authoritative: seeded from `fetchEvtSpikeStatus()` on expand; updated via SSE `detector_status`. Not localStorage-persisted. No migration required. |
| Drain state transitions | `HistoryModal` / `fetchHistory()` | Already a production REST API (`GET /api/v1/history/{host}`); not localStorage-based. No migration required. |
| ServerDetail durable CPU chart | `chart.svelte` tile (per-host) | Already uses the production `fetchMetrics(host, from, to, …)` SQLite-backed API. The 5M/1H/1D/3D/5D pill state is persisted to localStorage as UI preference, not as historical data. Already migrated; no action needed. |

### Implementation Gotchas Discovered During Review

1. **`clearStaleState()` / `MOCK_VERSION = "3.6"`** (`state.svelte.js` lines 45–58): On module init, if the mock version sentinel mismatches it wipes **all** `drainctl:*` localStorage keys. When the mock endpoint is removed, bump `MOCK_VERSION` (or drop the sentinel entirely) so the first post-migration page load does not erase accumulated UI preferences for existing users.

2. **`ServerDetail.svelte` fleet-fallback** (line ~24): `serverHistory = $derived(appState.serverMetrics.get(host) ?? appState.metricsHistory)` — when the per-host ring buffer is empty it currently falls back to the fleet aggregate, which is semantically misleading. After migration the production seed should fill `serverMetrics` on cold start; if no data arrives for a host the fallback must become an explicit empty state rather than the fleet aggregate.

3. **Fleet-seed synthesis block in `App.svelte`** (lines 230–314): The existing mock seed synthesizes `fleetHistory`, `sessHistory`, and `rfxHistSeed` by iterating parallel per-host arrays. This entire block is replaced by a single `fetchMetrics('_fleet', …)` call returning retained series directly; no client-side synthesis is needed.

## Complexity Tracking

> Constitution check passed; no violations require justification at planning time.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | | |
