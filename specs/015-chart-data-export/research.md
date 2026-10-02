# Research: Excel and CSV Graph Data Export

## R1 — XLSX library

**Decision**: `github.com/xuri/excelize/v2`.

**Rationale**:
- Pure Go, no cgo. DrainCtl's other libs (`modernc.org/sqlite`, `github.com/alexbrainman/sspi`) are pure-Go; CGO would force `//go:build` splits and complicate the build pipeline.
- BSD-3-Clause license is compatible with the existing module (matches `spf13/cobra`, `golang.org/x/sys`).
- Requires Go ≥ 1.26 — DrainCtl is `go 1.27.0` (go.mod), so the floor is satisfied.
- `SetCellStr` produces text cells (type `s` / shared string) that Excel does not interpret as formulas — gives us a single primitive that handles FR-016 (formula inertness) without a separate prefix-and-quote convention. Numeric `value` cells use `SetCellValue` with `float64` so they remain numeric for sorting/filtering (FR-010).
- Mature, widely used (8k+ stars on the v2 line); written against the ECMA-376 / OOXML spec, so workbooks open in Excel, LibreOffice, Numbers without repair warnings (FR/SC-003).

**Alternatives considered**:
- Hand-rolled OOXML: rejected. Writing a valid `.xlsx` requires a ZIP container, shared-strings table, sheet relationships, and styles part. Defects in any of those produce repair warnings or refuse to open. Not worth the surface area for one feature.
- `tealeg/golang-xlsx-headers` style minimal helpers: rejected. None of these write a true Data + Context two-sheet workbook; they wrap `excelize` or produce stale XLSX (the Excel 97 binary format that Excel will refuse without a converter on modern installs).
- Host-side Excel automation via `go-ole`: rejected. Requires Excel installed on the server (forbidden by FR-015), pulls COM/CGO in, and breaks Linux/macOS dev builds.

## R2 — Where exports are generated

**Decision**: Generate server-side in Go via dedicated endpoints, return the file as the HTTP response body with `Content-Disposition: attachment`.

**Rationale**:
- The existing `/api/v1/metrics/{host}` and `/api/v1/metrics/_fleet` already serve the snapshot data the spec requires. Adding a sibling `/api/v1/metrics/{host}/export` and `/api/v1/metrics/_fleet/export` (with `format=csv|xlsx`) reuses the same query path, tier resolution, host filter validation, rate limit, and session auth without duplicating logic.
- Browser-side generation was rejected because: (a) for fleet exports over 90-day windows the client payload can be tens of MB — re-encoding it in the browser is wasteful; (b) the spec mandates "actual metric values before visual normalization and display rounding" (FR-005) — the server has the raw values, the browser has only what it charted; (c) filename sanitization and `Content-Disposition` are simpler to get right on the server; (d) the size-limit guard (FR-017 / SC-004) is a server-side concern that requires inspecting the post-query row count, which is already known where the query runs.
- Reusing the existing route's auth (`requireSession`) and rate-limit (`rateLimitMiddleware`) wrappers means new code does not introduce a new trust boundary.

**Alternatives considered**:
- Client-side xlsx via SheetJS in the bundled dashboard JS: rejected for the reasons above. Useful only as a fallback for very large exports that exceed the documented size limit — and even then, the same server snapshot data should be used.
- Pre-built .xlsx templates filled in per export: rejected. The Context sheet content varies per export; a template buys little and adds a build artifact to manage.

## R3 — Formula-safety strategy

**Decision**: Use `excelize.SetCellStr` for every cell whose value originates from operator data (host names, series labels, graph names). Use `excelize.SetCellValue` with `float64` only for the `value` column. Never call `SetCellFormula`. Never emit a workbook with macros (`.xlsm`, `.xlsx` with `vbaProject.bin`).

**Rationale**:
- `SetCellStr` writes a shared-string cell (`<c t="s"><v>idx</v></c>`). Excel evaluates shared-string cells as text by definition, so a value like `=cmd|'/c calc'!A1` is rendered as the literal text and never parsed as a formula. This holds even when the value starts with `=`, `+`, `-`, `@`, tab, or CR — every formula-triggering character the spec lists.
- `SetCellValue` with a numeric `float64` writes a number cell (`<c><v>n</v></c>`). The cell type is numeric, so Excel cannot reinterpret it as a formula even though it carries a numeric payload.
- The combination satisfies FR-016 without a per-cell prefix-and-quote workaround, and it preserves the "numeric sorting/filtering" requirement from SC-003 (the `value` column remains numeric).
- Missings remain empty cells (`SetCellStr("")` or skipped); the spec's "missing values MUST be empty" rule is satisfied because there is no cell value at all.

**Alternatives considered**:
- Prefix every untrusted text with a single quote (`'=cmd|...`): rejected. Excel strips the leading quote on display but the raw cell value still contains `=cmd|...` if anyone programmatically reads it; analysis tools that ingest the workbook (Power Query, pandas) see the formula-prefixed string and trip over it. The shared-string approach is cleaner.
- A separate `safeStrings` sanitizer library: rejected. `SetCellStr` is already the canonical safe-write path; a wrapper would add surface area for no behavioral gain.

## R4 — Filename sanitization

**Decision**: Build filenames in Go with a single helper. Components:

- Static prefix `drainctl-`.
- Export type literal: `fleet` or `host`.
- Host name (per-host exports only): the same canonical hostname the registered server list exposes. Sanitize by stripping any character outside `[A-Za-z0-9._-]` and replacing runs with `-`.
- Graph slug: lowercase, ASCII letters/digits/hyphens, runs of non-allowed characters become `-`, leading/trailing hyphens stripped.
- Extension: `.csv` or `.xlsx`.

Resulting pattern: `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>`.

**Rationale**:
- Matches the clarification session's chosen pattern (Q3) and the reference files at `/tmp/drainctl-mocks/`.
- Windows and Excel both tolerate `-` and `.` in filenames; the only reserved characters on Windows are `<>:"/\|?*` plus control codes 0-31. The sanitizer excludes them by construction.
- The sanitizer is centralized so audit logging can show the requested name and the actual filename separately if they diverge.

## R5 — Browser download trigger

**Decision**: Frontend uses `fetch()` against the new export endpoint, then triggers a download by creating an `<a download>` element with a `Blob` URL (`URL.createObjectURL`) and clicking it programmatically. Error responses (non-2xx) are surfaced through the same toast/error mechanism the dashboard already uses for other API failures.

**Rationale**:
- The existing dashboard already uses `fetch` everywhere — no need to introduce `<form>`-style submissions or a redirect-based pattern.
- The Blob URL approach is the modern equivalent of `Content-Disposition` from the browser's perspective; it lets the same code path set the user-visible filename (via `a.download`) without depending on server response headers being honored (some intermediaries strip them).
- Toast/notification plumbing already exists (`frontend/src/components/Toast.svelte`, `appState`); reuse rather than create a new error pipeline.
- Auth: cookies travel with `fetch` by default; no new auth handshake is needed.

**Alternatives considered**:
- `window.location.href = url` to let the browser handle the download natively: rejected. Failure modes (auth expired, partial coverage, size limit exceeded) cannot be surfaced cleanly with a navigation response.
- Embedding a hidden `<iframe>` and targeting it with the download URL: rejected. Silently swallows non-2xx responses, and modern browsers' cookie heuristics around third-party contexts can suppress the cookie.

## R6 — Documented size limit

**Decision**: 50,000 rows in the Data sheet (Data sheet only — Context sheet rows do not count). Above that, the export endpoint returns `413 payload_too_large` with a JSON body explaining the limit and advising the operator to narrow the window.

**Rationale**:
- The spec's SC-004 already fixes "10,000 observations complete within five seconds." A 5x headroom (50k) keeps the per-export latency well within the 5s budget on the supported acceptance environment while remaining high enough that operators don't routinely hit it on default 1h / 6h / 24h windows.
- Counting rows is exact and cheap (the export enumerates after the query returns); counting bytes is approximate and dependent on serialization.
- 413 is the HTTP code semantically reserved for "payload you tried to create is too large" — it is not a server-side buffer overflow, which is what 500 would imply.

**Alternatives considered**:
- Byte-size cap on the response: rejected. Hard to predict before serializing; row-count is observable from the same query that produces the data.
- Streaming the file with chunked transfer and asking the operator to confirm mid-flight: rejected. Two-phase downloads break the dashboard's "two interactions to export" success criterion (SC-001).

## R7 — Acceptance environment for SC-004

**Decision**: The SC-004 performance criterion ("10,000 observations within five seconds") is verified against the project's standard supported acceptance environment: Windows Server 2022 with the dashboard running on the same host as the SQLite store (no network round-trip), default `drainctl.db` retention, and one export per call. This matches the deployment target declared in spec FR-015 ("Windows Server remains the supported deployment platform").

**Rationale**:
- Closes the previously deferred "agreed supported acceptance environment" gap that the clarification session could not resolve.
- Windows Server 2022 is the documented deployment target — testing against the same OS family avoids the false-positive of passing on Linux CI but failing on the actual product.
- Local-only (no network) makes the 5s figure reproducible; remote dashboards add variable latency that belongs in a separate load test, not in a unit-level success criterion.

## R8 — Graph inventory (concrete)

**Decision**: The supported inventory, per FR-002, is:

| Inventory entry | Frontend component | API path | Notes |
|---|---|---|---|
| Overview Load | `MetricsChart.svelte` (LOAD panel) | `/api/v1/metrics/_fleet` | Fleet |
| Health Indicators | `MetricsChart.svelte` (HIC charts) | `/api/v1/metrics/_fleet` | Fleet |
| Session Metrics | `MetricsChart.svelte` (Sessions panel) | `/api/v1/metrics/_fleet` | Fleet |
| RemoteFX | `MetricsChart.svelte` (RFX panel) | `/api/v1/metrics/_fleet` | Fleet; hidden when not present |
| Per-host Load | `HostLoadChart.svelte` | `/api/v1/metrics/{host}` | Per-host |
| Per-host detail charts | `ServerDetail.svelte` (whatever chart components it embeds) | `/api/v1/metrics/{host}` | Per-host |

Sparklines and table cells are excluded per spec.

**Rationale**:
- The plan must enumerate the inventory before coding (FR-002). The above is exhaustive of the time-series charts in the current `develop` source.
- Decorative sparklines and non-time-series tables are explicitly out of scope.
- If a future feature adds a new chart component, it must register itself with the export controller (see data-model.md) — this is the same convention used by the existing `chart-contracts.js` `LOAD_SERIES_META` pattern.
