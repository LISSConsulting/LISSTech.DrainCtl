# Dashboard API Inventory

**Status:** stable. The canonical request and response contract is [HTTP Metrics Export](../../specs/015-chart-data-export/contracts/http-metrics-export.md).

## Graph data export

The dashboard exposes additive authenticated `GET` export endpoints:

- `/api/v1/metrics/_fleet/export` — fleet graph data.
- `/api/v1/metrics/{host}/export` — per-host graph data.

Both endpoints accept `from`, `to`, `resolution`, `counters`, `format`, and `graph`. `format` is `csv` or `xlsx`; `graph` selects a registered chart inventory entry. The fleet endpoint additionally accepts repeatable `host` filters for a registered subset.

Successful requests return `200 OK` with an attachment. Invalid request fields return `400`; missing or invalid sessions return `401`; an unknown per-host path returns `404`; resolved exports over the documented 50,000-row limit return `413`; rate limiting returns `429`; and metrics storage failures return `500`. Exact error names, response headers, filename rules, and data layouts remain defined by the canonical contract.

XLSX generation uses the direct `github.com/xuri/excelize/v2` dependency in `go.mod`. It is pure Go and does not require Excel on the server.
