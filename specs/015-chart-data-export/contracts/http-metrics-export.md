# HTTP Contract: Graph Export

## Routes

- **Method**: `GET`
- **Path**: `/api/v1/metrics/_fleet/export`
- **Method**: `GET`
- **Path**: `/api/v1/metrics/{host}/export`

The fleet and per-host export endpoints mirror the existing `/api/v1/metrics/_fleet` and `/api/v1/metrics/{host}` query paths. They share the same tier resolution, host-filter validation, session auth, and rate-limit middleware.

## Query Parameters

All parameters are shared with the existing metrics endpoints unless noted.

| Name | Type | Required | Notes |
|---|---|---|---|
| `from` | ISO-8601 UTC | yes | Inclusive lower bound. Same validation as `/api/v1/metrics/{host}`. |
| `to` | ISO-8601 UTC | yes | Exclusive upper bound. Must be `> from`. Same validation as the read endpoint. |
| `resolution` | `auto` \| `raw` \| `1min` \| `5min` \| `hourly` | no | Default `auto`. Same set as the read endpoint. |
| `counters` | CSV of counter names | no | Restrict the export to these counters. Unknown names are silently skipped (consistent with the read endpoint). |
| `format` | `csv` \| `xlsx` | yes | Selects the file format. Anything else returns `400 invalid_format`. |
| `graph` | string | yes | The graph inventory id (`overview.load`, `host.load`, `hic.input_delay`, …). The endpoint uses this to determine series labels, ordering, and Context fields. Unknown ids return `400 unknown_graph`. |

For fleet exports only:

| Name | Type | Required | Notes |
|---|---|---|---|
| `host` | repeatable | no | Restrict the cohort. Same case-insensitive, deduped, registered-host validation as `/api/v1/metrics/_fleet`. |

## Success Response

- **Status**: `200 OK`
- **Headers**:
  - `Content-Type`: `text/csv; charset=utf-8` for `format=csv`, `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` for `format=xlsx`
  - `Content-Disposition`: `attachment; filename="<sanitized-name>"`
  - `Cache-Control`: `no-store`
- **Body**: The file bytes.

### Filename pattern

`drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>`

`graph-slug` is the lowercased, ASCII-hyphenated form of the graph label. `host_name` is present only for per-host exports. Examples:

- `drainctl-fleet-overview-load.xlsx`
- `drainctl-host-sql-prod-01-load.csv`

### Body shape

CSV body (Data only — no Context block):

```csv
timestamp_utc,host_name,series_id,series_label,unit,value,aggregation,resolution
2026-09-30T14:00:00Z,sql-prod-01,cpu_pct,CPU,%,42.1,avg,1m
2026-09-30T14:00:00Z,sql-prod-01,mem_avail_mb,Memory available,MB,8192,avg,1m
```

XLSX body has two sheets:

- `Data` — same columns as CSV, frozen header row, auto-filter on, numeric `value` column, text columns for everything else.
- `Context` — frozen header row, two columns (`Field`, `Value`), rows enumerated in data-model.md.

## Error Responses

- `400 invalid_range` — `from`/`to` invalid (re-using the existing read endpoint contract).
- `400 invalid_resolution` — `resolution` not in the allowed set.
- `400 invalid_format` — `format` absent or not in `{csv, xlsx}`.
- `400 unknown_graph` — `graph` absent or not in the registered inventory.
- `400 invalid_host_filter` — fleet export with an empty, case-insensitive duplicate, or unregistered `host` value.
- `404 unknown_host` — per-host export with a `host` that is not registered.
- `401 unauthorized` — missing/invalid session cookie.
- `413 payload_too_large` — the resolved snapshot exceeds 50,000 data rows. Body: `{"error":"payload_too_large","limit_rows":50000,"rows":<actual>,"message":"Narrow the time range or restrict the cohort."}`
- `429 rate_limited` — existing rate-limit middleware response.
- `500 storage_error` — underlying metrics query failed. The error body MUST NOT include the file contents, credentials, or any observation values (per FR-015 and Quality-and-Observability guidance).

## Examples

### Fleet Excel export

```http
GET /api/v1/metrics/_fleet/export?from=2026-09-30T13:00:00Z&to=2026-09-30T15:00:00Z&format=xlsx&graph=overview.load&host=sql-prod-01&host=mds-ldc-01
Authorization: Negotiate <gss-token>
Cookie: drainctl_session=...

→ 200 OK
  Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
  Content-Disposition: attachment; filename="drainctl-fleet-overview-load.xlsx"

  <binary .xlsx body>
```

### Per-host CSV export

```http
GET /api/v1/metrics/sql-prod-01/export?from=2026-09-30T14:00:00Z&to=2026-09-30T15:00:00Z&format=csv&graph=host.load

→ 200 OK
  Content-Type: text/csv; charset=utf-8
  Content-Disposition: attachment; filename="drainctl-host-sql-prod-01-load.csv"

  timestamp_utc,host_name,series_id,series_label,unit,value,aggregation,resolution
  2026-09-30T14:00:00Z,sql-prod-01,cpu_pct,CPU,%,42.1,raw,1m
  ...
```

## Notes

- The export captures a snapshot at the moment the request is processed. Late-arriving data or concurrent host-filter changes do not modify the file (FR-004).
- The endpoint does NOT require any new operator privilege (FR-015). The same session auth that protects `/api/v1/metrics/{host}` protects the export route.
- The endpoint does NOT upload the file anywhere. The browser downloads it directly (FR-015).
- The endpoint does NOT require Excel on the server (FR-015). The .xlsx is produced in pure Go via `excelize`.
- The CSV body has no Context block. Self-description is via the filename.
