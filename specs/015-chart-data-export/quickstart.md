# Quickstart: Excel and CSV Graph Data Export

This is an operator walkthrough. It does not repeat the spec; it documents the user-visible behavior of the export feature once shipped.

## Try it

1. Sign into the DrainCtl dashboard.
2. Open a supported graph (Overview Load, any Health Indicator chart, Sessions, RemoteFX, Per-host Load, or any per-host detail chart).
3. Use the chart's window presets (1h / 6h / 24h / 7d / 30d) or drag-pan to the time range you want.
4. Toggle series in the legend until only the series you want are visible.
5. For fleet views, use the host filter to restrict the cohort if desired.
6. Click the graph's export control and pick CSV or Excel.
7. The file downloads. The filename looks like:
   - `drainctl-fleet-overview-load.xlsx` for a fleet export
   - `drainctl-host-sql-prod-01-load.csv` for a per-host export

## What the file contains

- **CSV**: One consistent table with eight columns: `timestamp_utc, host_name, series_id, series_label, unit, value, aggregation, resolution`. Sortable in any spreadsheet.
- **Excel**: Two sheets.
  - `Data`: Same eight columns, plus a frozen header and auto-filter.
  - `Context`: A two-column `Field` / `Value` table with graph name, scope, time range, coverage, export time, and the generator version. The `host_filter` cell (fleet only) lists each contributing host on its own line.

## What's NOT in the file

- No graph image (PNG/PDF) is embedded.
- No Excel charts/chartsheet.
- No macros (.xlsm) — Excel will not warn about macro security because there are no macros.
- No formula interpretation of operator-supplied text — host names and labels that start with `=`, `+`, `@`, etc. open as inert text.

## When the export control is disabled

| State | Why |
|---|---|
| Loading | The graph has not finished its initial fetch yet. |
| Failed | The metrics endpoint returned a non-2xx response. |
| Stale | The current host filter no longer matches the loaded snapshot (e.g. a host was added since the chart loaded). |
| Empty | The current time window has no retained history for this graph. |
| No series enabled | All series in the legend have been toggled off. |

In each case the control is still visible (so the operator knows it exists) but disabled, with the reason visible on hover or focus.

## Error messages you might see

- **"Payload too large"** — the requested window and cohort produce more than 50,000 data rows. Narrow the time range or remove some hosts from the filter.
- **"Unknown graph"** — internal error, should never happen in normal use; report it via the existing feedback channel.
- **"Invalid format"** — internal error; the dashboard only ever asks for `csv` or `xlsx`.

## Audit trail

Exports are not stored. They are operator-initiated downloads that go directly from the dashboard to the operator's browser. The metrics query that produces the export is logged at the existing dashboard rate-limit / access log level (subject to the operational observability guidance — no exported values, no credentials, no file contents in logs).

## Versioning

The `generator` field in the Excel Context sheet is the same `DrainCtl v<version>` string that the build pipeline embeds in the Windows service and MSI. When the version changes, the field changes.
