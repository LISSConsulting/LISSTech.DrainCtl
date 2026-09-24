# Feature Specification: Chart Library Migration (layercake → LayerChart)

**Feature Branch**: `011-chart-library-migration`
**Created**: 2026-09-23
**Status**: Draft
**Input**: "I think we are beyond layercake. these graphs kinda suck, and took a lot of
ducktape (seems like it). We need robust, tested, standard graphs we can customize
to fit the look and feel."

## Problem statement

The drainctl dashboard's chart layer has grown organically into 4,277 LOC across 8
files, with chart rendering (LayerCake + custom d3-style scales), gesture handling
(pan/zoom/double-click reset), interactive state (crosshair, hover pin, threshold
toggle), and data adaptation (`adaptFleetToRfxSamples` et al.) wedged together inside
the same parent components (`HostLoadChart.svelte`, `MetricsChart.svelte`,
`lib/chart.svelte`). Three different parent files own handler logic specifically to
prevent a synthetic click after a pan from reaching the chart's pin-toggle handler —
that is the ducktape.

Worse, layercake is approaching a major-version break (10 → 11) that requires source
migration of four chart subcomponents regardless. The current code is held together
by knowledge of layercake's internal context-key string (`'LayerCake'`) and d3 scale
patterns hand-written across files. Migrating to layercake 11 would rewrite the
subcomponents without addressing the underlying coupling, leaving the ducktape
intact.

## Decision

Replace layercake with **LayerChart 2.x** (Svelte 5 native from day 1, charts as
composable components, d3-scale under the hood, theming via CSS custom properties
and Tailwind utility classes that are optional). Use **uPlot** for the
non-interactive Sparkline case in `frontend/src/components/Sparkline.svelte` only
(its 30 KB footprint and 100k+ point capacity are a better fit than LayerChart for a
non-interactive cell).

Migration is delivered as a single PR with per-chart-type commits for review.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Robust chart rendering (Priority: P1)

An operator opens the drainctl dashboard and sees the same six chart types as today
(per-host time series, session-cpu/mem with thresholds, dual-axis fleet LOAD,
session-metrics fleet overview, mini health chart with p95+p50 fills, sparkline).
The charts render from a maintained, Svelte-5-native charting library, are CSS-themed
through our design tokens, and pass Vitest unit tests for the data-adapter and
threshold-math logic.

**Independent test**: open Overview, Servers, and ServerDetail pages in the dev
dashboard; visually compare to the v26.9.12 screenshots in `docs/`; confirm zero
console warnings or errors; run `pnpm vitest run` and confirm all chart-unit tests pass.

**Acceptance scenarios**:

1. **Given** the dashboard builds with the new chart library, **When** an operator
   opens any page that previously used layercake, **Then** the chart renders with no
   "getContext is undefined" or similar warning, and the rendered SVG matches the
   brutalist outline-card style within the tolerances documented in `plan.md`.
2. **Given** a fleet has retained telemetry, **When** the operator pans the Overview
   LOAD chart by dragging horizontally, **Then** the visible window slides smoothly
   and a debounced re-fetch fires `GET /api/v1/metrics/_fleet?from=...&to=...`
   (canceling any in-flight request from the previous drag tick).
3. **Given** the operator double-clicks the per-host CPU chart, **When** the gesture
   completes, **Then** the window resets to the operator's last-selected preset
   (5M / 1H / 1D / 3D / 5D) and the new fetch fires.
4. **Given** a preset pill is clicked, **When** the click registers, **Then** the
   pill becomes `aria-pressed="true"`, the visible window jumps to that preset, and
   the pill selection is persisted to `localStorage` under
   `drainctl.chart.zoomPillMs`.

### User Story 2 — Preserved interactive semantics (Priority: P1)

The ducktape that currently keeps chart interactions coherent
(post-drag-click-pin-suppression, threshold-toggle-click-vs-pan-start disambiguation,
hover-pin crosshair) is replaced by LayerChart's `Highlight` + custom Svelte
`onpointerdown` capture-phase handlers that own the same observable behavior.

**Acceptance scenarios**:

1. **Given** the operator pans the dual-axis fleet LOAD chart, **When** the pan
   gesture ends, **Then** the threshold-toggle checkboxes do not toggle (their click
   handler is suppressed during and for 100 ms after pan-end).
2. **Given** the operator hovers over the per-host CPU chart, **When** the crosshair
   appears and the operator clicks once, **Then** the crosshair becomes pinned at
   that timestamp (does not move with the cursor) and a second click unpins it.
3. **Given** the operator has CPU/MEM threshold toggles, **When** a toggle is
   clicked, **Then** the corresponding threshold band fades in/out within 150 ms and
   the underlying line plot does not re-render.

### User Story 3 — Tested data adapters and zoom math (Priority: P2)

The hand-written data adapters (`adaptFleetToRfxSamples`, `adaptFleetToOverview`,
`tsMap`, `lcData` builders) and zoom-state math (debounced re-fetch sequencing,
in-flight request cancellation, window math for presets) are extracted into pure
functions in `frontend/src/lib/chart/adapters.js` and `frontend/src/lib/chart/zoom.js`
and covered by Vitest unit tests.

**Acceptance scenarios**:

1. `vitest run chart/adapters` — at least one test per adapter covering: empty
   input, single-sample input, and a representative real-shape input (≥100 samples).
2. `vitest run chart/zoom` — covers: preset-to-window math (5M, 1H, 1D, 3D, 5D),
   debounced re-fetch sequencing (rapid calls coalesce to one fetch), in-flight
   cancellation (a stale response does not overwrite a newer one).
3. CI runs `vitest run` on every PR and fails on any test regression.

### User Story 4 — Chart-library upgrade path (Priority: P2)

The new chart components consume LayerChart as a regular npm dependency and do not
import from `'layerchart/dist/...'` internals, do not reference any undocumented
property of `<Chart>`, and pass `pnpm update layerchart` cleanly. Future LayerChart
upgrades do not require source changes in any of our chart components.

**Acceptance scenarios**:

1. `pnpm update layerchart --latest` from the current pinned version produces a diff
   only in `frontend/pnpm-lock.yaml` and the version string in
   `frontend/package.json`. No source files change.
2. No chart consumer imports from `layerchart/internal/...`,
   `layerchart/dist/...`, or any path other than `'layerchart'` and (for the geo /
   canvas sub-modules) the documented `./geo` / `./canvas` exports.

## Visual fidelity target

**Brutalist spirit preserved, pixel parity not required.** Allow these improvements
where they cost nothing:

- Threshold bands at 0.20 / 0.30 opacity (was 0.25 / 0.35) for cleaner stacking
- Slightly thicker axis lines (1.5 px → 2 px) for visibility on hi-DPI displays
- Crosshair line at 1.5 px width with a small dot at the active sample (was a thin
  line only)
- Cleaner tooltip with a `transparent: black 90%` background and 4 px padding
  (was a solid `var(--color-bg)` with 2 px padding)

Out of scope for this PR:

- Redesigning the chart aesthetic (no new colors, no new card shapes, no new layout)
- Adding wheel zoom (currently disabled per `lib/chart.svelte` comment)
- Adding keyboard navigation or screen-reader announcement
- Changing which counters are charted or how data is fetched from the backend

## Out of scope

- New chart types
- New data sources
- Server-side rendering (the embedded dashboard does not SSR)
- Mobile / touch-gesture improvements
- i18n of chart labels

## Constraints

- No new telemetry schema. Charts consume existing `/api/v1/metrics/{host}` and
  `/api/v1/metrics/_fleet` response shapes.
- No backend changes. The Go handlers stay as-is.
- No installer changes. No PowerShell module changes.
- No CI workflow changes except adding `pnpm vitest run` as a required check.
- No new third-party CSS framework (no Tailwind, no UnoCSS). All theming continues to
  flow through `frontend/src/app.css` design tokens.

## Open questions

None at time of writing. Resolved by user input above:

- Library: LayerChart 2.x for interactive charts, uPlot for Sparkline.
- Visual: improve where it costs nothing, keep brutalist spirit.
- Interactions: all current preserved.
- Testing: Vitest unit tests only, no visual regression suite.
- PR strategy: single PR with per-chart-type commits.
