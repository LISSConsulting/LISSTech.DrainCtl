# Quickstart: Remaining Persistent Telemetry Consumers

## Goal

Validate that Overview retained history and other migrated dashboard historical surfaces
load from SQLite-backed telemetry instead of browser-local warm-up state.

## Prerequisites

- A local or staging DrainCtl build with feature `008-sqlite-chart-consumers`
- Historical telemetry already present in `drainctl.db` (at least one full poll cycle)
- A browser profile with cleared `localStorage` for the dashboard origin

---

## User Story 1 Validation — Overview Retained History (MVP)

### Fresh-session load

1. Start the service and open the dashboard in a fresh browser profile (or with cleared
   `localStorage`).
2. Navigate to the Overview page.
3. Confirm that all four chart families render retained history immediately — without
   waiting for multiple poll cycles to accumulate browser-local samples:
   - **LOAD**: CPU %, Memory %, Sessions lines are populated
   - **HIC**: Input Delay, Pages/sec, TCP Retrans, Disk Queue charts show data
   - **SESSIONS**: Sessions Trend, Utilization, Session CPU, Session Memory show data
   - **REMOTEFX**: Tab is hidden if no RFX telemetry has been collected; visible and
     populated if RFX counters are present in the retained store
4. Stop and restart the service, then refresh the dashboard.
5. Confirm pre-restart retained history is still visible on a fresh browser load.

### Empty state validation

1. Select a time range where retained data does not exist (e.g. using browser DevTools to
   temporarily block the fleet endpoint or by querying a time before the service started).
2. Confirm that each chart family shows **"No retained history for this window"** rather
   than a blank or broken chart.
3. Confirm no browser-local fallback is substituted.

### Error state validation

1. Force the fleet metrics query to fail (e.g. temporarily firewall the API route or
   return a 500 from a test build).
2. Confirm each chart family shows **"Unable to reach the metrics endpoint"** in red.
3. Confirm the UI does not substitute browser-local history as fallback truth.

---

## User Story 2 Validation — Shared Window Navigation (US2, not yet shipped)

> These steps apply after Phase 4 (US2) is complete.

1. On the Overview page, click each window pill: `5M`, `1H`, `1D`, `3D`, `5D`.
2. Verify LOAD, HIC, SESSIONS, and REMOTE FX all move to the same shared window.
3. Use wheel zoom and drag pan; confirm rapid gestures do not leave stale data on screen.

---

## User Story 3 Validation — Remaining Historical Consumers (US3, not yet shipped)

> These steps apply after Phase 5 (US3) is complete.

1. Clear browser-local storage and reload.
2. Confirm that per-host sparklines in the server table show retained history cold.
3. Confirm that the Server Detail view fallback history loads from retained telemetry.

---

## Suggested Verification Commands

```powershell
go test ./...
just lint
```

## Review Targets

- `frontend/src/App.svelte`
- `frontend/src/lib/api.js`
- `frontend/src/lib/state.svelte.js`
- `frontend/src/components/MetricsChart.svelte`
- `frontend/src/components/ServerTable.svelte`
- `frontend/src/components/ServerDetail.svelte`
- `internal/dashboard/server.go`
- `internal/dashboard/server_test.go`
