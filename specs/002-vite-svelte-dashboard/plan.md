# Implementation Plan: Vite + Svelte Dashboard Migration

**Branch**: `002-vite-svelte-dashboard` | **Date**: 2026-04-09 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/002-vite-svelte-dashboard/spec.md`

## Summary

Migrate the DrainCtl dashboard from a 6,732-line monolithic HTML file (inline CSS + vanilla JS + bundled uPlot) to a Vite + Svelte 5 + Tailwind CSS v4 + Layercake architecture. The migration preserves full feature parity with the existing dashboard, replaces the drain-mode state history chart with a performance metrics chart (CPU%, Memory%, Input Delay, Sessions), fixes I/O ring gauge coloring, improves configuration modal UX, and tightens CSP by eliminating `'unsafe-inline'`. The Go backend API and single-binary deployment model remain unchanged.

## Technical Context

**Language/Version**: Go 1.26+ (backend, unchanged), JavaScript/Svelte 5 (frontend, new)
**Primary Dependencies**: Svelte 5, Vite 6, Tailwind CSS v4, Layercake (charting)
**Storage**: N/A (frontend consumes Go backend API)
**Testing**: Playwright (E2E, visual regression), Vitest (unit for utility functions)
**Target Platform**: Windows Server (Go backend), Modern browsers (Chrome/Edge, dashboard SPA)
**Project Type**: Embedded SPA within Go binary (single-binary deployment)
**Performance Goals**: Dashboard loads in <2 seconds on local network; bundle size <150 KB total
**Constraints**: Must preserve SSPI/Negotiate auth, all API contracts, security headers; Windows-only Go backend
**Scale/Scope**: ~15 Svelte components, 1 Tailwind theme, 1 Vite config, Go embed changes in 1 file

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution file contains only template placeholders — no gates are defined. **PASS** (no violations possible).

## Project Structure

### Documentation (this feature)

```text
specs/002-vite-svelte-dashboard/
├── plan.md              # This file
├── research.md          # Phase 0 output — technology decisions
├── data-model.md        # Phase 1 output — entity/state model
├── quickstart.md        # Phase 1 output — dev setup guide
├── contracts/
│   └── api-routes.md    # Phase 1 output — API contract reference
└── tasks.md             # Phase 2 output (created by /speckit.tasks)
```

### Source Code (repository root)

```text
frontend/                              # NEW — Svelte 5 + Vite project
├── src/
│   ├── App.svelte                     # Root component, layout, refresh loop
│   ├── app.css                        # Tailwind v4 @theme + global styles
│   ├── main.js                        # Entry point, mounts App
│   ├── lib/
│   │   ├── state.svelte.js            # Global $state: servers, events, history, config
│   │   ├── api.js                     # fetch wrappers for all API routes
│   │   ├── theme.js                   # Theme toggle, localStorage, flash prevention
│   │   └── thresholds.js              # Ring gauge threshold logic + defaults
│   └── components/
│       ├── Nav.svelte                 # Brand, server summary, config/theme toggles
│       ├── CounterGrid.svelte         # 5 counter cards
│       ├── StateBar.svelte            # Proportional state segments
│       ├── MetricsChart.svelte         # Layercake multi-series performance chart
│       ├── chart/
│       │   ├── AreaPath.svelte        # Layercake SVG layer: filled area
│       │   ├── LinePath.svelte        # Layercake SVG layer: stroke line
│       │   ├── AxisX.svelte           # Layercake SVG layer: time axis
│       │   └── AxisY.svelte           # Layercake SVG layer: value axis
│       ├── EventLog.svelte            # Filterable, expandable event log
│       ├── ServerTable.svelte         # Sortable server table
│       ├── ServerDetail.svelte        # Accordion: 3 tiles (resources, I/O, info)
│       ├── RingGauge.svelte           # Reusable SVG ring with threshold colors
│       ├── Sparkline.svelte           # Layercake mini line chart
│       ├── ConfigModal.svelte         # Settings modal with dirty/save feedback
│       ├── NotificationTargets.svelte # Targets table within config modal
│       ├── TargetEditModal.svelte     # Add/edit target (layered modal)
│       ├── TargetDeleteModal.svelte   # Delete confirmation dialog
│       ├── HistoryModal.svelte        # Server transition timeline
│       └── Footer.svelte              # Connection status, last updated
├── index.html                         # Vite entry HTML (theme flash script)
├── package.json
├── vite.config.js
└── dist/                              # Build output (gitignored)

internal/dashboard/
├── dist/                              # Copied from frontend/dist (gitignored)
│   ├── index.html
│   ├── assets/
│   │   ├── app-[hash].js
│   │   └── app-[hash].css
│   └── favicon.ico
├── server.go                          # MODIFIED — embed dir, serve SPA + assets, CSP update
├── dashboard.html                     # REMOVED after migration complete
└── ... (unchanged: store.go, client.go, auth, tls, etc.)
```

**Structure Decision**: Frontend source lives at `frontend/` (repo root) to keep Node.js tooling separate from the Go module. Build output is copied to `internal/dashboard/dist/` for Go embedding. This preserves the existing package structure while adding the modern build pipeline.

## Implementation Phases

### Phase 1: Build Pipeline & Scaffold (P1 — Story 5)

**Goal**: Vite + Svelte 5 project builds and Go embeds + serves the output.

1. Initialize `frontend/` with Svelte 5 + Vite + Tailwind v4
2. Create minimal `App.svelte` that renders "DrainCtl Dashboard" placeholder
3. Configure Vite build output (`frontend/dist/`)
4. Update `internal/dashboard/server.go`:
   - Change `//go:embed dashboard.html` → `//go:embed all:dist`
   - Add `http.FileServer` for `/assets/*` with immutable cache headers
   - Update `handleUI` to serve `dist/index.html`
   - Update `cspHeader` to use `'self'` + nonce instead of `'unsafe-inline'`
5. Add `frontend` and `frontend-copy` recipes to `justfile`
6. Add `frontend/dist/` and `internal/dashboard/dist/` to `.gitignore`

**Verification**: `just all` produces a binary that serves the placeholder dashboard.

### Phase 2: Theme & Design System (P2 — Story 6)

**Goal**: Tailwind v4 theme matches existing visual identity pixel-for-pixel.

1. Create `app.css` with `@theme` directive mapping all 17 CSS custom properties
2. Add dark mode overrides via `html[data-theme="dark"]` selector
3. Add theme flash prevention `<script>` in `index.html` with nonce support
4. Create `theme.js` with localStorage toggle + `$state` binding
5. Import Google Fonts (DM Serif Display, Fraunces, Work Sans, JetBrains Mono)
6. Define global utility classes for offset shadows, pill badges, border styles

**Verification**: Theme toggle works, colors match existing dashboard in both modes.

### Phase 3: Core Layout & State (P1 — Story 1, partial)

**Goal**: App shell with reactive state and API polling.

1. Create `state.svelte.js` with `$state` for servers, events, stateHistory, config
2. Create `api.js` with typed fetch wrappers for all API routes
3. Implement 30-second refresh loop in `App.svelte` using `$effect`
4. Build `Nav.svelte` (brand, summary stats, config toggle, theme toggle)
5. Build `CounterGrid.svelte` with 5 `$derived` counters
6. Build `StateBar.svelte` with proportional segments
7. Build `Footer.svelte` with connection status

**Verification**: Dashboard shows live counter/state data, updates every 30s.

### Phase 4: Charts & Visualization (P1 — Story 1, continued)

**Goal**: Layercake-based performance metrics chart and sparklines with neobrutalist styling.

1. Install `layercake` npm package
2. Build reusable Layercake SVG layers: `AreaPath.svelte`, `LinePath.svelte`, `AxisX.svelte`, `AxisY.svelte` in `components/chart/`
3. Build `MetricsChart.svelte` — 4 toggleable time-series (CPU%, Memory%, Input Delay ms, Session count) using Layercake `<LayerCake>` with SVG layers
4. Style chart with neobrutalist aesthetics: bold 2.5px strokes, offset drop shadow on container, thick grid lines, high-contrast fills with transparency
5. Build `Sparkline.svelte` — mini Layercake line chart for per-server metrics in accordion detail rows
6. Responsive via Layercake's built-in container-aware sizing (no manual ResizeObserver needed)
7. Theme-aware chart colors via CSS custom property reads

**Verification**: Performance metrics chart shows CPU/Mem/InputDelay/Sessions with toggleable series, sparklines render in detail rows, both respond to theme toggle.

### Phase 5: Server Table & Detail Rows (P1 — Stories 1 + 2)

**Goal**: Server table with accordion detail rows and ring gauges.

1. Build `ServerTable.svelte` — sortable columns, expandable rows
2. Build `ServerDetail.svelte` — 3-tile layout (resources, I/O, info)
3. Build `RingGauge.svelte` — reusable SVG ring with `getThresholdColor()` logic
4. Create `thresholds.js` — threshold resolution logic:
   - Use `PerformanceConfig` thresholds when available
   - Fall back to defaults (Disk Queue: warn 2/crit 5, TCP Retransmits: warn 5%/crit 10%)
5. Apply threshold colors to ALL ring gauges (CPU, Memory, Sessions, Disk Queue, Input Delay, TCP Retransmits) — **fixes the I/O coloring bug (Story 2)**

**Verification**: All rings show green/amber/red based on thresholds; I/O rings are no longer neutral-only.

### Phase 6: Event Log (P1 — Story 1, continued)

**Goal**: Filterable, expandable event log.

1. Build `EventLog.svelte` — search input, severity-colored entries, transition highlights
2. Expandable fullscreen mode
3. Auto-scroll to latest entry

**Verification**: Event log filters, scrolls, expands, shows severity colors.

### Phase 7: Configuration Modal (P2 — Stories 3 + 4)

**Goal**: Config modal with UX improvements.

1. Build `ConfigModal.svelte`:
   - Grace period pills + custom input
   - Session threshold slider
   - Performance monitoring toggles + thresholds
   - Button layout: "Send Test" far left, "Save" + "Close" far right (Story 3)
   - Dirty-state indicator: subtle background color change (Story 3)
   - Save feedback: success banner (green flash) / error banner (red flash) (Stories 3 + FR-015)
   - Consistent hover animation on all buttons (Story 3)
2. Build `NotificationTargets.svelte` — targets table within config modal
3. Build `TargetEditModal.svelte` — layered modal with styled scrollbar (Story 4)
4. Build `TargetDeleteModal.svelte` — confirmation dialog
5. Apply scrollbar styling globally to all modals (Story 4)

**Verification**: All modal UX improvements work; scrollbars consistent; dirty/save/error states correct.

### Phase 8: History Modal (P1 — Story 1, continued)

**Goal**: Server history timeline modal.

1. Build `HistoryModal.svelte` — timeline entries, transitions-only toggle
2. Fetch from `GET /api/v1/history/{host}` with limit/changes_only params

**Verification**: History modal shows transition timeline for any server.

### Phase 9: Polish & Cleanup

**Goal**: Final parity verification and cleanup.

1. Visual regression comparison against existing dashboard (screenshots)
2. Verify all 15 functional requirements (FR-001 through FR-015)
3. Verify all 8 success criteria (SC-001 through SC-008)
4. Remove `internal/dashboard/dashboard.html` (replaced by embedded dist)
5. Update `justfile` prettier recipe to format Svelte files
6. Update CSP comment in `server.go` to reflect new security posture

## Complexity Tracking

No constitution violations to justify — constitution is not defined.

## Risk Mitigation

| Risk | Mitigation |
|---|---|
| Layercake SVG rendering perf with many data points | Cap time-series to 60 samples (same as current MAX_HIST); Layercake handles this well |
| CSP nonce breaks caching | Nonce only on index.html (no-cache already); assets use content hashes |
| Font loading flash | Preload critical fonts in index.html `<link rel="preload">` |
| Go embed path issues on Windows | Use forward slashes in embed directive; test in CI |
| Bundle size exceeds 150 KB | Monitor with `vite build --report`; Layercake is ~8 KB (much smaller than uPlot's 45 KB) |
