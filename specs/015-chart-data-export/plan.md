# Implementation Plan: Excel and CSV Graph Data Export

**Branch**: `015-chart-data-export` | **Date**: 2026-09-30 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/015-chart-data-export/spec.md`

## Summary

Generate Excel (`.xlsx`) and CSV downloads from the dashboard's existing graph snapshots. The export captures one graph at a time (fleet or per-host), expands to one row per (timestamp × series × host_name), preserves actual metric values before chart rounding, and ships the file directly to the browser through a new pair of GET endpoints that mirror the existing metrics routes. CSV is Data-only; Excel adds a Context sheet for self-description. Filenames follow the `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>` pattern.

## Technical Context

**Language/Version**: Go 1.27.0; Svelte 5; JavaScript; Vite 8.
**Primary Dependencies**:
- `github.com/xuri/excelize/v2` — new, BSD-3-Clause, pure Go, Go ≥ 1.26. Used only for `.xlsx` serialization.
- `encoding/csv` — Go standard library, used for `.csv`.
- Existing `modernc.org/sqlite` and dashboard query layer are reused unchanged.
**Storage**: None added. Exports read from the same retained metrics tables the existing `/api/v1/metrics/{host}` and `/api/v1/metrics/_fleet` endpoints use.
**Testing**: Focused Go unit tests for the snapshot builder, CSV writer, XLSX writer, filename sanitizer, size-limit guard, and formula-safety primitives. Focused Go boundary tests for the new HTTP handlers (auth, host-filter validation, format selection, error responses). Svelte unit tests for the export control wiring (disabled-state matrix, download trigger, error surfacing). Browser smoke test that loads each supported chart, triggers both formats, and inspects the file. Completion requires `go test ./...`, frontend tests/build, `just lint`, and `prek`.
**Target Platform**: Windows Server service and its authenticated browser dashboard.
**Project Type**: Single Go module with embedded Svelte frontend.
**Performance Goals**:
- 10,000-row export completes in ≤ 5s on the supported acceptance environment (SC-004).
- Documented size limit: 50,000 data rows. Exceeding returns `413 payload_too_large` with explanatory body (FR-017).
- Endpoint latency is dominated by the underlying metrics query; the export serialization step adds ≤ 50ms for a 10k-row export on the supported environment.
**Constraints**:
- New Go files carry `//go:build windows`.
- No new operator privilege required; existing dashboard session auth covers the export endpoint.
- No Excel on the server; no external upload; no macros in produced files (FR-015).
- Operator-supplied text (host names, labels, graph names) MUST NOT be interpreted as a formula in the produced workbook (FR-016).
- CSV is Data-only; Excel carries Data + Context (FR-007 / FR-009).
- Filename follows `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>` (FR-013).
- Exports are not retained server-side and not logged with their contents (Quality-and-Observability guidance).
**Scale/Scope**: Same as the existing metrics endpoints. Up to 16 hosts, 500 sessions per host snapshot, 25h raw, 6d 5-minute, configured metrics-days hourly retention.

## Constitution Check

*GATE: Passed before research and re-checked after design.*

- **Windows-first delivery**: PASS. New Go files are Windows-tagged. The change touches the dashboard handlers and the dashboard API surface. Root package, CLI, DLL/interop, PowerShell, service, and installer behavior are intentionally unchanged. The Windows service continues its existing telemetry and dashboard responsibilities.
- **Stable operator surfaces**: PASS. New additive endpoints under `/api/v1/metrics/_fleet/export` and `/api/v1/metrics/{host}/export`. No existing route changes shape; no config schema change; no release artifact change beyond the rebuild picking up the new code. No deprecation. The dashboard adds an export control per supported chart but does not modify any existing chart's read interaction.
- **Tests and zero-noise verification**: PASS. Unit tests cover snapshot assembly, CSV/XLSX writers, filename sanitizer, size-limit guard, and formula-safety primitives. Boundary tests cover the new HTTP handlers end-to-end (auth, host filter, format selection, error responses, 413 path). Frontend tests cover the disabled-state matrix and the download trigger. Browser smoke confirms a real Excel open without repair warnings. Completion requires focused tests, `go test ./...`, frontend tests/build, `just lint`, `prek`, and browser smoke.
- **Config and release discipline**: PASS. No new configuration. No new retention policy. No schema migration. Release remains git-derived CalVer; the `generator` field in the Excel Context sheet pulls from the same `scripts/version.ps1` source of truth the build pipeline uses.
- **Operational observability**: PASS. Failures use the existing `writeJSONError` pattern with bounded structured logs that omit exported values, credentials, and file contents. The dashboard surfaces the error through the existing Toast component. Audit logging is unchanged: export requests are access-logged at the existing level for the metrics routes.

**Post-design gate result**: Pass. The dedicated export endpoint is justified because the existing `/api/v1/metrics/{host}` and `/api/v1/metrics/_fleet` handlers emit JSON for chart consumption; producing an `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` payload from the same handler would couple the response shape to a query parameter and force every JSON consumer to negotiate Content-Type.

## Project Structure

### Documentation (this feature)

```text
specs/015-chart-data-export/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
└── contracts/
    └── http-metrics-export.md
```

### Source Code (repository root)

```text
internal/
├── dashboard/
│   ├── handlers_export.go              # new: GET /api/v1/metrics/{host}/export and /api/v1/metrics/_fleet/export
│   ├── handlers_export_test.go         # boundary tests
│   ├── export_snapshot.go              # new: graph snapshot assembly
│   ├── export_snapshot_test.go
│   ├── export_csv.go                   # new: CSV writer
│   ├── export_csv_test.go
│   ├── export_xlsx.go                  # new: XLSX writer (excelize)
│   ├── export_xlsx_test.go
│   ├── export_filename.go              # new: filename sanitizer
│   ├── export_filename_test.go
│   ├── server.go                       # modified: register two new routes
│   └── openapi.yaml                    # modified: document the two new endpoints

frontend/src/
├── lib/
│   ├── export-graph-config.js          # new: graph-id → series labels, ordering, default counters
│   └── api.js                          # modified: fetchExportMetrics helper
└── components/
    ├── MetricsChart.svelte             # modified: add export control + disabled-state wiring
    ├── HostLoadChart.svelte            # modified: add export control + disabled-state wiring
    ├── ServerDetail.svelte             # modified: pass through export control to embedded charts
    └── chart/
        ├── DualAxisChart.svelte        # modified: add export button slot
        └── MiniHealthChart.svelte      # modified: add export button slot

docs/                                   # operator-facing docs (no new pages; quickstart.md is the source of truth)
README.md                               # modified: link to quickstart from the dashboard section
```

**Structure Decision**: Reuse the existing `internal/dashboard` package for both the handlers and the writers. Keep writers close to handlers because they share types (snapshot, observation, error envelope) and they all need to be exercised by the same boundary tests. New files are grouped by concern (snapshot, csv, xlsx, filename) rather than split across packages — the surface area is small and splitting would force an additional package boundary for no reuse benefit.

## Design Decisions

1. **Server-side generation, browser-side download trigger.** The server returns the file as the HTTP response; the browser wraps it in a `Blob` and triggers `a[download]`. See research.md R2 / R5.
2. **`excelize/v2` for XLSX.** Pure Go, BSD-3-Clause, matches the existing dependency floor (Go ≥ 1.26). See research.md R1.
3. **Formula safety via `SetCellStr`.** Every operator-derived string cell uses `SetCellStr`, which writes a shared-string cell that Excel never interprets as a formula. The `value` column is the only numeric cell and uses `SetCellValue(float64)`. See research.md R3.
4. **50,000-row documented size limit.** Counted on the post-query snapshot, returned as `413 payload_too_large`. See research.md R6.
5. **CSV has no Context block.** The clarification session confirmed this; CSV self-description is the filename. See spec FR-009 and the Contracts file.
6. **Filename pattern `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>`.** Sanitized at the route handler. See research.md R4.
7. **Excluded graphs are excluded.** Decorative sparklines and non-time-series tables are out of scope (spec FR-002). The supported inventory is enumerated in research.md R8; new chart components must register themselves with the export controller.
8. **Per-row shape identical for fleet and per-host.** One uniform column set; the difference lives in the Context sheet (and the filename). See spec FR-006.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Add `github.com/xuri/excelize/v2` dependency | FR-010 / FR-016 require a true `.xlsx` workbook with formula-safe text cells | Stdlib has no `.xlsx` writer; hand-rolled OOXML is fragile (shared strings, styles part, sheet relationships — any defect produces a repair warning). Stdlib `encoding/csv` covers CSV only |
| Two new HTTP routes under `/api/v1/metrics/{host}/export` and `/api/v1/metrics/_fleet/export` | The existing metrics handlers emit JSON; adding `?format=xlsx` would force every JSON consumer to negotiate Content-Type and add serialization branching to the existing handler | Reusing the existing handlers via a query parameter couples response shape to a request flag and makes the documented HTTP contract ambiguous |
| Dedicated `export_snapshot.go` rather than reusing the JSON response shape directly | The spec requires per-row `host_name`, `aggregation`, `resolution` columns — the JSON shape carries them as parallel arrays per series, not as a denormalized row stream | A JSON-to-snapshot transformer is cleaner than walking the per-series parallel arrays at write time and gets us a single testable boundary |

## Acceptance

Plan acceptance is satisfied when:

- `internal/dashboard/handlers_export.go` registers both new routes and the test file exercises the boundary: `200` for valid fleet + per-host requests across both formats; `400 invalid_format` for unknown formats; `400 unknown_graph` for unknown graph ids; `400 invalid_host_filter` for unknown / duplicate / empty host names; `404 unknown_host` for unknown per-host targets; `413 payload_too_large` for > 50,000-row snapshots; `401 unauthorized` for missing session.
- `internal/dashboard/export_xlsx_test.go` confirms `SetCellStr` is used for every untrusted cell and that no workbook embeds macros (`xl/workbook.xml` lacks `vbaProject` references).
- `frontend/src/components/MetricsChart.svelte` and `frontend/src/components/HostLoadChart.svelte` carry an export control whose disabled-state matrix is asserted by a unit test for: loading, failed, stale-for-filter, empty, no-series-enabled.
- A browser smoke run downloads one of each (fleet Excel, fleet CSV, per-host Excel, per-host CSV) and opens the .xlsx without a repair warning.
- `go test ./...`, frontend tests/build, `just lint`, and `prek` all pass without bypass.
