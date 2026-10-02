# Tasks: Excel and CSV Graph Data Export

**Input**: Design documents from `/specs/015-chart-data-export/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/http-metrics-export.md

**Tests**: Required by the constitution (Principle III) and the spec's "Required tests" enumeration. Unit, contract, integration, and frontend tests are in scope.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions

This is a single Go module with an embedded Svelte frontend (see plan.md Project Structure). Backend paths under `internal/dashboard/`. Frontend paths under `frontend/src/`. The `//go:build windows` constraint applies to every new Go file (per constitution Principle I).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the new dependency, register the dependency in `go.mod`/`go.sum`, and pin the version used in tests.

- [ ] T001 Add `github.com/xuri/excelize/v2` to `go.mod` (`go get github.com/xuri/excelize/v2`) and run `go mod tidy` so `go.sum` is updated
- [ ] T002 Verify the new dependency compiles cleanly under the `//go:build windows` constraint with `go build ./...`
- [ ] T003 [P] Add a `package export_test` build tag comment block to a stub file `internal/dashboard/export_dummy_windows.go` so subsequent test files in this package inherit the Windows build constraint without re-declaring it (scaffolding only — content is added by T007/T012/T014)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core types, writer primitives, and HTTP plumbing that both US1 and US2 need. No user story work can begin until this phase is complete.

- [ ] T004 [P] Create snapshot, series-definition, and observation types in `internal/dashboard/export_snapshot.go` per `specs/015-chart-data-export/data-model.md` (Snapshot, SeriesDefinition, Observation structs with JSON tags removed — these are internal-only)
- [ ] T005 [P] Implement `BuildSnapshot(...)` in `internal/dashboard/export_snapshot.go` that takes a metrics query result plus a graph config and returns a Snapshot (expanding per-series parallel arrays into one Observation per (timestamp × series × host_name), sorted chronologically with the deterministic tiebreaker from data-model.md)
- [ ] T006 [P] Implement filename sanitizer in `internal/dashboard/export_filename.go` with `BuildFilename(exportType, hostName, graphLabel string) (string, error)` that enforces the `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>` pattern, rejects reserved Windows filename characters in `graphLabel`, and returns the sanitized graph slug
- [ ] T007 [P] Define the graph inventory in `internal/dashboard/export_graphs.go`: a single `ExportGraph` struct (id, label, counterFamily, defaultCounters, tier default, allowed set) and a registry function `LookupExportGraph(id string) (ExportGraph, bool)` covering the concrete inventory enumerated in research.md R8 (overview.load, overview.hic.input_delay, overview.hic.pages_sec, overview.hic.tcp_retrans_sec, overview.hic.disk_queue, overview.sessions, overview.remotefx, host.load)
- [ ] T008 [P] Implement `writeJSONError` adapter in `internal/dashboard/handlers_export.go` that calls the existing `writeJSONError` helper for `invalid_range`, `invalid_resolution`, `invalid_format`, `unknown_graph`, `invalid_host_filter`, `unknown_host`, `unauthorized`, `storage_error`, plus a new `payload_too_large` body that returns `{ "error": "payload_too_large", "limit_rows": 50000, "rows": <actual>, "message": "Narrow the time range or restrict the cohort." }` with HTTP 413
- [ ] T009 Implement `enforceExportSizeLimit(snapshot Snapshot, w http.ResponseWriter) bool` in `internal/dashboard/handlers_export.go` that returns `true` if `len(snapshot.Rows) <= 50000` and otherwise writes the `413 payload_too_large` body and returns `false`
- [ ] T010 Wire the two new routes in `internal/dashboard/server.go`: `GET /api/v1/metrics/{host}/export` and `GET /api/v1/metrics/_fleet/export`, both `rlw(rs(http.HandlerFunc(ds.handleExport)))` — matching the existing metrics handler's middleware stack
- [ ] T011 Implement `handleExport(w, r)` in `internal/dashboard/handlers_export.go` that: resolves `_fleet` vs per-host via path, parses and validates `from`/`to`/`resolution`/`counters`/`host` (fleet only) using the same rules as `handleMetrics`/`handleFleetMetrics`, parses and validates `format` ∈ {`csv`, `xlsx`} and `graph` (via `LookupExportGraph`), looks up the graph config, calls the existing metrics query path, builds a Snapshot via `BuildSnapshot`, calls `enforceExportSizeLimit`, and dispatches to the CSV or XLSX writer

**Checkpoint**: Foundation ready — both user stories can now proceed in parallel.

---

## Phase 3: User Story 1 - Export a graph to CSV (Priority: P1) 🎯 MVP

**Goal**: Operator downloads a CSV that contains one row per (timestamp × series × host_name) for the selected graph snapshot.

**Independent Test**: `go test ./internal/dashboard -run TestExportCSV` passes; the CSV file opens in a text editor and parses with `encoding/csv` to a header row + N data rows whose column order matches data-model.md.

### Tests for User Story 1 ⚠️

> **NOTE**: Write these tests FIRST, ensure they FAIL before implementation.

- [ ] T012 [P] [US1] Unit test for CSV writer in `internal/dashboard/export_csv_test.go`: header row exactly matches `timestamp_utc,host_name,series_id,series_label,unit,value,aggregation,resolution`; missing values are empty; numeric `value` cells preserve precision (no scientific notation); row order is chronological with `(series_id, host_name)` tiebreak; UTF-8 host names with commas/quotes/newlines round-trip; literal `=`, `+`, `-`, `@`, tab, CR in series labels survive unchanged in the file body (they remain unescaped because CSV has no formula interpreter, but they must not corrupt the column structure)
- [ ] T013 [P] [US1] Contract test for `GET /api/v1/metrics/_fleet/export?format=csv&graph=overview.load` in `internal/dashboard/handlers_export_test.go`: response is `200`, `Content-Type: text/csv; charset=utf-8`, `Content-Disposition: attachment; filename="drainctl-fleet-overview-load.csv"`, body parses with `encoding/csv` to the expected column set
- [ ] T014 [P] [US1] Contract test for `GET /api/v1/metrics/{host}/export?format=csv&graph=host.load` in `internal/dashboard/handlers_export_test.go`: response is `200`, body is per-host CSV with `host_name` constant across all rows; filename includes the canonical host name

### Implementation for User Story 1

- [ ] T015 [P] [US1] Implement CSV writer in `internal/dashboard/export_csv.go`: `WriteCSV(w io.Writer, snap Snapshot) error` using `encoding/csv.NewWriter`, the fixed header row from data-model.md, `strconv.FormatFloat(v, 'f', -1, 64)` for non-nil values, empty string for nil values, ISO 8601 UTC timestamps with `Z` suffix, deterministic row order from T005
- [ ] T016 [US1] Wire CSV branch in `internal/dashboard/handlers_export.go`: after `enforceExportSizeLimit`, set `Content-Type`, `Content-Disposition` (via `BuildFilename(exportType, hostName, graphLabel)` joined with `.csv`), `Cache-Control: no-store`, then call `WriteCSV`
- [ ] T017 [US1] Update `internal/dashboard/openapi.yaml` to document the `format=csv` query parameter, the `200` response shape, and the `400 invalid_format` / `413 payload_too_large` error responses

**Checkpoint**: At this point, US1 is fully functional and testable independently. Operator can download a CSV from a supported graph.

---

## Phase 4: User Story 2 - Export a graph to Excel (Priority: P1) 🎯 MVP

**Goal**: Operator downloads an `.xlsx` workbook with a numeric Data sheet and a self-describing Context sheet that opens without a repair warning.

**Independent Test**: `go test ./internal/dashboard -run TestExportXLSX` passes; opening the produced file in Excel/LibreOffice/Numbers shows a frozen header row, numeric `value` cells (sortable/filterable), the Context sheet enumerated in data-model.md, and no repair warning.

### Tests for User Story 2 ⚠️

> **NOTE**: Write these tests FIRST, ensure they FAIL before implementation.

- [ ] T018 [P] [US2] Unit test for XLSX writer in `internal/dashboard/export_xlsx_test.go`: workbook has two sheets (`Data` then `Context`); Data sheet header row is the same 8-column set; `value` cells are numeric (`<c><v>n</v></c>`); every other cell uses shared-string type (`<c t="s">`); untrusted strings starting with `=`, `+`, `-`, `@`, tab, CR remain inert on disk (asserted by reading `xl/sharedStrings.xml` and confirming the entry is present unchanged — not promoted to a formula)
- [ ] T019 [P] [US2] Formula-safety invariant test in `internal/dashboard/export_xlsx_test.go`: produced workbook must not contain a `vbaProject` part; the `xl/workbook.xml` must not declare macros; no `<f>` elements exist in the produced sheet XML (defensive check that `SetCellFormula` was never called)
- [ ] T020 [P] [US2] Contract test for `GET /api/v1/metrics/_fleet/export?format=xlsx&graph=overview.load` in `internal/dashboard/handlers_export_test.go`: response is `200`, `Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `Content-Disposition` filename ends in `.xlsx`, body is a valid OOXML container (zip with `[Content_Types].xml`, `xl/workbook.xml`, `xl/sharedStrings.xml`, two worksheets)
- [ ] T021 [P] [US2] Contract test for `GET /api/v1/metrics/{host}/export?format=xlsx&graph=host.load` in `internal/dashboard/handlers_export_test.go`: workbook's Data sheet rows all share the host's `host_name`; Context sheet includes `host_name` and omits `host_filter` / `host_filter_count`
- [ ] T022 [P] [US2] Cross-format parity test in `internal/dashboard/export_csv_test.go` (or a new `export_parity_test.go`): build a Snapshot from a fixed fixture, write CSV and XLSX, read both back, assert the observation rows are identical (same timestamps, same host_name, same series_id, same value-or-empty, same order)

### Implementation for User Story 2

- [ ] T023 [P] [US2] Implement XLSX writer in `internal/dashboard/export_xlsx.go`: `WriteXLSX(w io.Writer, snap Snapshot) error` using `excelize.NewFile()`; create `Data` sheet first, write the 8-column header with header styling (bold), use `SetCellStr` for every string cell and `SetCellValue(float64)` only for the `value` column; set `SetPanes` for frozen header row; set `AutoFilter` on the Data range; create `Context` sheet, write the field set in the order from data-model.md using `SetCellStr` for the values (including the newline-delimited `host_filter`); enable wrap-text on the `host_filter` and `series` cells; emit the `generator` field using `scripts/version.ps1`-equivalent string resolution (see T024); write to `w` via `excelize.WriteTo`
- [ ] T024 [US2] Resolve the generator version in `internal/dashboard/export_xlsx.go` via a small helper `ResolveGeneratorVersion() string` that returns `"DrainCtl v" + <version>` where `<version>` is the same git-derived CalVer the build pipeline uses (call `scripts/version.ps1` once during init via `exec.Command` or expose a tiny `version` helper — pick whichever path the existing release artifacts use, do not duplicate logic)
- [ ] T025 [US2] Wire XLSX branch in `internal/dashboard/handlers_export.go`: after `enforceExportSizeLimit`, set `Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `Content-Disposition` (via `BuildFilename` joined with `.xlsx`), `Cache-Control: no-store`, then call `WriteXLSX`
- [ ] T026 [US2] Update `internal/dashboard/openapi.yaml` to document the `format=xlsx` query parameter, the `200` response shape, and the `400 invalid_format` / `413 payload_too_large` error responses

**Checkpoint**: At this point, US1 and US2 are both fully functional and testable independently. Operator can download both formats from any supported graph.

---

## Phase 5: User Story 3 - Export confidently from any supported graph (Priority: P2)

**Goal**: The export control is consistently available on every supported graph, correctly disabled in loading/empty/partial/failed/no-series states, and emits clear failure messages with retry semantics.

**Independent Test**: Browser smoke run downloads each supported graph in both formats and verifies the disabled-state matrix; an intentional failure (e.g. `unknown_graph` injected) surfaces the same toast the dashboard already uses for other API errors.

### Tests for User Story 3 ⚠️

- [ ] T027 [P] [US3] Frontend unit test in `frontend/src/components/export-control.test.js` (or alongside the existing component tests): the export control's disabled-state predicate `shouldDisableExport({ loading, failed, empty, hasEnabledSeries, isStale })` returns true for each invalid state and false for the happy path; the control's accessible name includes the graph label per FR-001
- [ ] T028 [P] [US3] Frontend unit test for the download trigger in `frontend/src/lib/api.js`: `fetchExportMetrics` returns a `Blob` with the correct MIME type for both formats; non-2xx responses surface the same error envelope shape the existing API helpers use (so the Toast component picks them up unchanged)
- [ ] T029 [P] [US3] Backend error-surface integration test in `internal/dashboard/handlers_export_test.go`: for each documented error (`invalid_format`, `unknown_graph`, `invalid_host_filter`, `unknown_host`, `payload_too_large`, `storage_error`), assert the response body shape matches the contracts/http-metrics-export.md "Error Responses" table exactly

### Implementation for User Story 3

- [ ] T030 [P] [US3] Add the frontend export-control helper in `frontend/src/lib/export-graph-config.js`: a single registry mapping graph id → graph label, default counters, and the disabled-state predicate inputs each chart component already exposes
- [ ] T031 [P] [US3] Add `fetchExportMetrics` in `frontend/src/lib/api.js`: `fetchExportMetrics({ host, from, to, resolution, counters, format, graph })` returning a `Blob`; on non-2xx it parses the JSON error body and rejects with the same envelope `{ error, message?, ... }` used by the existing API helpers
- [ ] T032 [US3] Add the export control slot in `frontend/src/components/chart/DualAxisChart.svelte` (props: `exportConfig`, `onExport` callback); the control renders a button with the graph label in its `aria-label`, is keyboard-accessible (Enter/Space activate), and is disabled when `shouldDisableExport(...)` returns true
- [ ] T033 [US3] Mirror the export control wiring in `frontend/src/components/chart/MiniHealthChart.svelte` (the HIC sub-chart component) with the same props shape
- [ ] T034 [US3] Wire `MetricsChart.svelte` to instantiate one export control per supported graph panel (LOAD, each HIC chart, Sessions, RemoteFX) and pass through the disabled-state signals the chart already computes
- [ ] T035 [US3] Wire `HostLoadChart.svelte` and `ServerDetail.svelte` to instantiate the per-host export control for every chart they embed
- [ ] T036 [US3] Ensure the export button's failure path uses the existing Toast component in `frontend/src/components/Toast.svelte` — no new error pipeline, no new notification types

**Checkpoint**: All three user stories are independently functional. Operator gets consistent export UX across the supported inventory.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Improvements that affect multiple user stories and ship-readiness.

- [ ] T037 [P] Update `docs/architecture/` if the export endpoint changes the documented API surface inventory (cite `contracts/http-metrics-export.md` from the existing fleet/per-host metrics docs)
- [ ] T038 [P] Update `README.md` dashboard section to mention CSV + Excel export availability with a single link to `specs/015-chart-data-export/quickstart.md`
- [ ] T039 [P] Add `CHANGELOG.md` entry under the next release section noting the additive export endpoints and the new `excelize/v2` dependency
- [ ] T040 Verify observability paths: confirm no exported observation values, no credentials, and no file contents appear in `slog` output for either handler; the only logged fields are the request path, format, graph id, host (when relevant), and the error code on failure
- [ ] T041 Confirm release discipline: `go test ./...` passes, `just lint` passes, `prek` passes, frontend tests/build pass
- [ ] T042 Browser smoke: open the running dashboard, navigate to each of the supported graphs (Overview Load, one HIC chart, Sessions, RemoteFX when present, Per-host Load, one per-host detail chart), trigger both CSV and Excel export, and confirm the files download, open, and match the reference layouts produced during clarification
- [ ] T043 Confirm SC-004 timing: produce a 10,000-row fixture and assert the export endpoint returns within five seconds on the Windows Server 2022 acceptance environment per research.md R7
- [ ] T044 Confirm SC-006 formula-safety on a real Excel install: open a produced XLSX in Excel, type the host name `=cmd|'/c calc'!A1` into the dashboard's host filter, export, open the workbook, and verify the cell renders as inert text and not as an executed formula
- [ ] T045 Confirm FR-002 graph inventory is exhaustively covered: walk the registry added in T007 and assert each entry has a wired export control in the frontend (one assertion per entry)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup completion. **BLOCKS** all user stories.
- **User Stories (Phase 3+)**: All depend on Foundational phase completion.
- **Polish (Phase 6)**: Depends on all desired user stories being complete.

### User Story Dependencies

- **US1 (P1)**: Can start after Foundational (Phase 2). No dependency on US2.
- **US2 (P1)**: Can start after Foundational (Phase 2). No dependency on US1.
- **US3 (P2)**: Depends on US1 + US2 because the disabled-state matrix and download trigger reference both formats.

### Within Each User Story

- Tests (T012–T014, T018–T022, T027–T029) MUST be written and FAIL before implementation (T015–T017, T023–T026, T030–T036).
- Foundational types (T004) precede the writer (T015, T023) and the handler (T011).
- Filename sanitizer (T006) precedes the Content-Disposition wiring (T016, T025).
- Graph registry (T007) precedes the handler (T011).
- Backend writers/handlers precede frontend wiring (US3).

### Parallel Opportunities

- All Phase 1 setup tasks (T001–T003) can run sequentially but T002 and T003 can be parallelized.
- All Phase 2 foundational tasks marked [P] (T004–T008) can run in parallel after T001.
- Within US1: T012, T013, T014 (tests) can run in parallel before T015, T016.
- Within US2: T018, T019, T020, T021, T022 (tests) can run in parallel before T023, T024, T025.
- US1 and US2 themselves can run in parallel after Phase 2 (different files, different code paths through the same handler).
- Within US3: T027, T028, T029 (tests) parallel; T030, T031 (helpers) parallel; T032, T033 (component slots) parallel; T034, T035 (chart wiring) parallel.

---

## Parallel Examples

### User Story 1 (CSV)

```bash
# Launch all tests for User Story 1 together:
Task: "T012 Unit test for CSV writer in internal/dashboard/export_csv_test.go"
Task: "T013 Contract test for fleet CSV in internal/dashboard/handlers_export_test.go"
Task: "T014 Contract test for per-host CSV in internal/dashboard/handlers_export_test.go"

# Then implementation:
Task: "T015 Implement CSV writer in internal/dashboard/export_csv.go"
Task: "T016 Wire CSV branch in internal/dashboard/handlers_export.go"
Task: "T017 Update internal/dashboard/openapi.yaml"
```

### User Story 2 (Excel) — runs in parallel with US1

```bash
Task: "T018 Unit test for XLSX writer"
Task: "T019 Formula-safety invariant test"
Task: "T020 Contract test for fleet XLSX"
Task: "T021 Contract test for per-host XLSX"
Task: "T022 Cross-format parity test"

# Then implementation:
Task: "T023 Implement XLSX writer in internal/dashboard/export_xlsx.go"
Task: "T024 Resolve generator version helper"
Task: "T025 Wire XLSX branch in handlers_export.go"
Task: "T026 Update openapi.yaml"
```

### User Story 3 (any graph) — after US1 + US2

```bash
Task: "T030 Add frontend export-control helper"
Task: "T031 Add fetchExportMetrics in api.js"
Task: "T032 Export control slot in DualAxisChart.svelte"
Task: "T033 Export control slot in MiniHealthChart.svelte"
Task: "T034 Wire MetricsChart.svelte"
Task: "T035 Wire HostLoadChart.svelte and ServerDetail.svelte"
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2)

1. Phase 1: Setup (T001–T003).
2. Phase 2: Foundational (T004–T011).
3. Phase 3: US1 — CSV (T012–T017).
4. Phase 4: US2 — Excel (T018–T026).
5. **STOP and VALIDATE**: open the dashboard, export both formats from a supported graph, confirm files open cleanly, run `go test ./...`, `just lint`, `prek`.
6. Ship the MVP — operators have both formats on every supported graph, US3 (P2) is the polish that follows.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. US1 → operator can export CSV. Deploy/demo.
3. US2 → operator can export Excel. Deploy/demo.
4. US3 → operator gets the disabled-state matrix, error surfaces, and inventory coverage. Deploy/demo.
5. Polish → docs, CHANGELOG, observability review, smoke run.

### Parallel Team Strategy

- **Dev A**: Phase 1 + Phase 2 (blocking).
- **After Phase 2**:
  - **Dev A**: US1 (CSV) — backend.
  - **Dev B**: US2 (Excel) — backend.
  - **Dev C**: US3 (frontend wiring) — once US1+US2 land, can use mock blobs initially.
- **Polish**: anyone.

---

## Notes

- All new Go files MUST carry `//go:build windows` (constitution Principle I). This is enforced by the convention used throughout the existing `internal/dashboard/` package.
- Every operator-derived text cell in XLSX uses `excelize.SetCellStr` (research.md R3) — never `SetCellFormula`, never numeric-typed cells for text.
- Documented size limit is **50,000 data rows** (research.md R6). Exceeding returns `413 payload_too_large`.
- CSV has no Context block (spec FR-009, clarified in session 2026-09-30). Excel carries Data + Context.
- Exports are operator-initiated downloads — no server-side retention, no audit log of file contents, no credentials in logs.
- Acceptance environment for SC-004 is Windows Server 2022, dashboard co-located with SQLite store (research.md R7).
- `[P]` tasks = different files, no dependencies.
- `[Story]` label maps each task to its user story for traceability.
- Verify required tests FAIL before implementing (per the test-first discipline enforced by this template).
- Commit after each task or logical group.
- Stop at any phase checkpoint to validate the story independently.
- Avoid: vague tasks, same-file conflicts, cross-story dependencies that break independence.
