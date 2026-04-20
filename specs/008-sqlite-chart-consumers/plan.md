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

## Complexity Tracking

> Constitution check passed; no violations require justification at planning time.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | | |
