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

## User Story 2 Validation — Shared Window Navigation

### Pill selection

1. On the Overview page, click each window pill: `5M`, `1H`, `1D`, `3D`, `5D`.
2. Verify LOAD, HIC, SESSIONS, and REMOTE FX all update to the selected window.
3. Confirm the active pill highlights in accent color after each click.
4. Confirm that switching windows clears any pinned crosshair position.

### Wheel-zoom

1. Hover over the LOAD chart and scroll the mouse wheel up (zoom in) and down (zoom out).
2. Confirm the active pill advances to the adjacent preset on each scroll step.
3. Confirm the fleet data refreshes after each step without stale data from the previous window.

### Drag-pan

1. Click and drag left or right on the LOAD chart.
2. Release the mouse button.
3. Confirm the chart loads a historical window offset by the drag distance (amber `↺ LIVE` button appears).
4. Click `↺ LIVE` and confirm the chart returns to the live (most recent) window.

### Empty/error under navigation

1. Switch to a window where no retained data exists.
2. Confirm all chart families show the "No retained history for this window" placeholder.
3. Confirm switching back to a populated window restores the chart data.

---

## User Story 3 Validation — Remaining Historical Consumers

### Sparkline cold-start validation

1. Clear browser-local storage (`localStorage.clear()` in DevTools console) and hard-reload
   the dashboard.
2. Navigate to the Servers view.
3. Confirm that per-host CPU, Memory, Input Delay, and Sessions sparklines in the server
   table render retained history immediately — no polling cycles needed.
4. Confirm that the Server Detail sparklines (expand any row) also show retained data from
   the seed without requiring a warm session.

### Empty state

1. Register a new host that has no retained telemetry yet.
2. Confirm that its sparkline columns are blank (no sparkline shown) rather than showing
   stale data from a different host.

### Fallback semantics

1. Clear `localStorage` and reload while the backend is temporarily unavailable.
2. Confirm that sparklines remain blank rather than showing stale browser-local data or a
   cross-host fallback.
3. Once the backend is available again, confirm the seed fills in on the next load.

---

## Suggested Verification Commands

```powershell
go test ./...
just lint
just frontend
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
