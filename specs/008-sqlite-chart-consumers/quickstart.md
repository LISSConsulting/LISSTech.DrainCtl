# Quickstart: Remaining Persistent Telemetry Consumers

## Goal

Validate that Overview retained history and other migrated dashboard historical surfaces
load from SQLite-backed telemetry instead of browser-local warm-up state.

## Prerequisites

- A local or staging DrainCtl build with feature `008-sqlite-chart-consumers`
- Historical telemetry already present in `drainctl.db`
- A browser profile with cleared `localStorage` for the dashboard origin

## Validation Flow

1. Start the service and open the dashboard in a fresh browser profile.
2. Confirm the Overview page renders retained history without waiting for multiple poll
   cycles to accumulate browser-local samples.
3. Switch between the `5M`, `1H`, `1D`, `3D`, and `5D` pills.
4. Verify LOAD, HIC, SESSIONS, and REMOTE FX all move to the same shared window.
5. Use wheel zoom and drag pan; confirm rapid gestures do not leave stale data on screen.
6. Stop and restart the service, then refresh the dashboard.
7. Confirm pre-restart retained history is still visible.
8. Clear browser-local storage again and reload.
9. Confirm migrated historical surfaces still load from retained telemetry.

## Empty/Unavailable Validation

1. Select a window where one chart family has no retained data.
2. Confirm that chart remains visible with an explicit empty or unavailable state.
3. Confirm the other chart families continue rendering normally in the same shared window.

## Error-State Validation

1. Force the metrics query path to fail in a test build or fixture.
2. Confirm the affected chart remains visible and shows an explicit error state.
3. Confirm the UI does not substitute browser-local history as fallback truth.

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
