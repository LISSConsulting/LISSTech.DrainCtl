# Tasks: Vite + Svelte Dashboard Migration

**Input**: Design documents from `/specs/002-vite-svelte-dashboard/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api-routes.md

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## User Story Mapping

- **US1**: Core Dashboard Monitoring (P1)
- **US2**: I/O Ring Gauge Threshold Colors (P1)
- **US3**: Configuration Modal UX Improvements (P2)
- **US4**: Notification Target Modal Scrollbar Consistency (P2)
- **US5**: Migrated Build and Embedding Workflow (P1)
- **US6**: Theme Support and Visual Fidelity (P2)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize Vite + Svelte 5 + Tailwind v4 project scaffold and Go embed changes

- [ ] T001 Initialize `frontend/` with `npm create vite@latest` using Svelte template, add Svelte 5, Tailwind CSS v4, and Layercake as dependencies in `frontend/package.json`
- [ ] T002 Configure Vite build in `frontend/vite.config.js`: Svelte plugin, build output to `frontend/dist/`, asset path prefix `/assets/`, dev proxy for `/api/*` to Go backend
- [ ] T003 [P] Create `frontend/index.html` entry with theme flash prevention `<script>` (reads `localStorage.drainctl-theme`, sets `data-theme` on `<html>` before first paint), font preload links
- [ ] T004 [P] Create minimal `frontend/src/main.js` entry point that mounts `App.svelte` to `#app`
- [ ] T005 [P] Create placeholder `frontend/src/App.svelte` that renders "DrainCtl Dashboard" text to verify build pipeline
- [ ] T006 [P] Add `frontend/dist/` and `internal/dashboard/dist/` to `.gitignore`
- [ ] T007 Add `frontend` recipe (runs `npm run build` in `frontend/`) and `frontend-copy` recipe (copies `frontend/dist/` to `internal/dashboard/dist/`) to `justfile`; wire `frontend` as dependency of `cli` recipe

**Checkpoint**: `cd frontend && npm run build` produces `frontend/dist/` with `index.html` + hashed assets

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Go embed changes and core frontend infrastructure that ALL user stories depend on

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [ ] T008 Update `internal/dashboard/server.go`: replace `//go:embed dashboard.html` with `//go:embed all:dist` using `embed.FS`; update `handleUI` to serve `dist/index.html` from embedded FS; add `/assets/*` route with `http.FileServer` and `Cache-Control: public, max-age=31536000, immutable`
- [ ] T009 Update `cspHeader` in `internal/dashboard/server.go`: replace `script-src 'unsafe-inline'` with `script-src 'self' 'nonce-{random}'`; replace `style-src 'unsafe-inline'` with `style-src 'self'`; add nonce generation in `handleUI` and inject into `index.html` theme script
- [ ] T010 [P] Create `frontend/src/app.css` with Tailwind v4 `@import "tailwindcss"` and `@theme` directive mapping all 17 CSS custom properties (--color-bg, --color-fg, --color-accent, --color-green, --color-amber, --color-red, --color-border, --color-shadow, --color-card, --color-muted, --color-subtle, --color-surface, --color-code-bg, --color-code-fg, --radius-default, --spacing-bw, --spacing-so); add dark mode overrides via `html[data-theme="dark"]`; import Google Fonts
- [ ] T011 [P] Create `frontend/src/lib/theme.js` exporting `$state` theme binding, `toggleTheme()` function, and `initTheme()` that reads `localStorage.drainctl-theme` and `prefers-color-scheme`
- [ ] T012 [P] Create `frontend/src/lib/api.js` with typed fetch wrappers for all API routes: `fetchServers()`, `fetchHealth()`, `fetchHistory(host, limit, changesOnly)`, `fetchNotifyConfig()`, `saveNotifyConfig(config)`, `sendNotifyTest(target?)`, `deleteServer(host)`
- [ ] T013 [P] Create `frontend/src/lib/state.svelte.js` with global `$state` runes: `servers` (Server[]), `events` (EventEntry[]), `metricsHistory` (MetricsSample[]), `config` (NotifyConfig), `connected` (boolean); add `$derived` computations: `counters` ({total, ok, grace, alert, off}), `stateBarSegments` (percentage widths), `avgCpu`, `avgMem`, `avgInputDelay`, `totalSessions`
- [ ] T014 [P] Create `frontend/src/lib/thresholds.js` exporting `getThresholdColor(value, warnThreshold, critThreshold, direction)` that returns `'green'`/`'amber'`/`'red'`/`'neutral'`; export default threshold constants for Disk Queue (warn: 2, crit: 5) and TCP Retransmits (warn: 5%, crit: 10%); export `resolveThresholds(metric, perfConfig)` that uses config thresholds when available, falls back to defaults
- [ ] T015 Define neobrutalist utility classes in `frontend/src/app.css`: `.shadow-brutal` (offset drop shadow using --spacing-so and --color-shadow), `.btn-brutal` (hover translate -2px/-2px + increased shadow, active translate +2px/+2px + reduced shadow), `.pill` (badge pill style), `.border-brutal` (2.5px solid using --color-border), `.scrollbar-styled` (thin scrollbar, subtle thumb, accent on hover, transparent track)

**Checkpoint**: Foundation ready — `just all` produces binary serving placeholder dashboard with correct CSP, theme toggle, and API client ready

---

## Phase 3: User Story 5 — Migrated Build and Embedding Workflow (Priority: P1) 🎯 MVP

**Goal**: End-to-end build pipeline producing a single Go binary with embedded Svelte dashboard

**Independent Test**: Run `just all`, start binary, navigate to dashboard URL, verify it loads and all API endpoints respond

### Implementation for User Story 5

- [ ] T016 [US5] Wire `App.svelte` to import `app.css`, call `initTheme()` on mount, and start 30-second `$effect` refresh loop calling `fetchServers()` and `fetchHealth()` from `api.js`, updating `state.svelte.js` stores in `frontend/src/App.svelte`
- [ ] T017 [US5] Run full build pipeline: `just all`; verify Go binary embeds `dist/` assets; start server and confirm dashboard loads at root URL with correct CSP headers, HSTS, X-Frame-Options, and Cache-Control
- [ ] T018 [US5] Verify `/assets/*` static files serve with `Cache-Control: public, max-age=31536000, immutable`; verify `/` serves `index.html` with `Cache-Control: no-cache`; verify all 11 API routes respond correctly per `contracts/api-routes.md`

**Checkpoint**: Single-binary deployment works — dashboard assets embedded, all API routes functional

---

## Phase 4: User Story 6 — Theme Support and Visual Fidelity (Priority: P2)

**Goal**: Light and dark themes match existing dashboard pixel-for-pixel

**Independent Test**: Toggle between light/dark themes, verify colors, fonts, shadows, and spacing match existing dashboard

### Implementation for User Story 6

- [ ] T019 [P] [US6] Build `Nav.svelte` in `frontend/src/components/Nav.svelte`: brand logo with "DC" badge, server status summary (ok/grace/alert/off counts), CONFIG button, theme toggle button (☽/☀); use `.btn-brutal` hover animation
- [ ] T020 [P] [US6] Build `Footer.svelte` in `frontend/src/components/Footer.svelte`: connection status pill (green "Connected" / red "Disconnected"), last updated timestamp from `$derived`
- [ ] T021 [US6] Integrate `Nav.svelte` and `Footer.svelte` into `App.svelte` layout; verify theme toggle persists to localStorage, dark mode colors match spec (--color-bg: #110a0a, --color-fg: #f5e8e8, etc.), fonts render correctly (DM Serif Display for titles, Fraunces for headings, Work Sans for body, JetBrains Mono for metrics)

**Checkpoint**: Theme toggle works, both modes visually match existing dashboard

---

## Phase 5: User Story 1 — Core Dashboard Monitoring (Priority: P1)

**Goal**: Full dashboard with counters, state bar, performance chart, event log, server table, and history modal

**Independent Test**: Navigate to dashboard, verify all sections render with live data, updates every 30s

### Implementation for User Story 1

- [ ] T022 [P] [US1] Build `CounterGrid.svelte` in `frontend/src/components/CounterGrid.svelte`: 5 counter cards (Total Servers, Healthy, Grace, Alert, Sessions) using `$derived` counters from `state.svelte.js`; apply `.shadow-brutal` and `.border-brutal` styling
- [ ] T023 [P] [US1] Build `StateBar.svelte` in `frontend/src/components/StateBar.svelte`: proportional stacked segments (ok/grace/alert/off) using `$derived` stateBarSegments; color-coded with --color-green, --color-amber, --color-red, --color-subtle
- [ ] T024 [P] [US1] Build Layercake chart primitives in `frontend/src/components/chart/`: `AreaPath.svelte` (filled area with transparency), `LinePath.svelte` (bold 2.5px stroke), `AxisX.svelte` (time axis with neobrutalist tick marks), `AxisY.svelte` (value axis with thick grid lines)
- [ ] T025 [US1] Build `MetricsChart.svelte` in `frontend/src/components/MetricsChart.svelte`: Layercake `<LayerCake>` composing AreaPath + LinePath + AxisX + AxisY layers; 4 toggleable series (CPU% in accent, Memory% in green, Input Delay in amber, Sessions in red); toggle buttons as pills above chart; feed from `$derived` metricsHistory; neobrutalist container with `.shadow-brutal`
- [ ] T026 [US1] Update refresh loop in `App.svelte` to compute and append metrics samples to `metricsHistory` each cycle: aggregate `avgCpu`, `avgMem`, `avgInputDelay`, `totalSessions` from server list; cap at 60 samples (MAX_HIST)
- [ ] T027 [P] [US1] Build `EventLog.svelte` in `frontend/src/components/EventLog.svelte`: scrollable log with search filter input, severity-colored entries (`.sev-ok`, `.sev-grace`, `.sev-alert`, `.sev-off`), transition highlights, expandable fullscreen mode, auto-scroll to latest
- [ ] T028 [P] [US1] Build `ServerTable.svelte` in `frontend/src/components/ServerTable.svelte`: sortable 10-column table (status dot, host, status badge, mode, duration, sessions, CPU, mem free, input delay, last seen); clickable rows toggle accordion expansion via `$state` expandedRow
- [ ] T029 [US1] Build `ServerDetail.svelte` in `frontend/src/components/ServerDetail.svelte`: 3-tile layout — resource utilization tile (CPU/Memory/Sessions rings + sparklines), I/O metrics tile (Disk Queue/Input Delay/TCP Retransmits rings), server details tile (registration date, last seen, version, changed_by)
- [ ] T030 [P] [US1] Build `RingGauge.svelte` in `frontend/src/components/RingGauge.svelte`: reusable SVG circular progress ring accepting `value`, `max`, `warnThreshold`, `critThreshold`, `direction` props; uses `getThresholdColor()` from `thresholds.js` to determine fill color; renders label and value text centered
- [ ] T031 [P] [US1] Build `Sparkline.svelte` in `frontend/src/components/Sparkline.svelte`: mini Layercake line chart accepting `data` array and `color` prop; fixed height ~40px; no axes, just the line path; used in ServerDetail for per-server CPU/memory/input delay history
- [ ] T032 [US1] Build `HistoryModal.svelte` in `frontend/src/components/HistoryModal.svelte`: overlay modal showing server transition timeline from `fetchHistory(host)`; toggleable "transitions only" filter via `changesOnly` param; entries show timestamp, status badge, drain mode, duration, changed_by
- [ ] T033 [US1] Integrate all Phase 5 components into `App.svelte` layout: CounterGrid → StateBar → MetricsChart → EventLog → ServerTable; wire HistoryModal to open from server row click action; verify full data flow from API → state → components

**Checkpoint**: Full dashboard functional — all sections render, update in real time, history modal works

---

## Phase 6: User Story 2 — I/O Ring Gauge Threshold Colors (Priority: P1)

**Goal**: Disk Queue, Input Delay, and TCP Retransmits ring gauges display green/amber/red coloring

**Independent Test**: Expand server detail row, verify I/O rings show color-coded thresholds matching CPU/Memory/Sessions ring behavior

### Implementation for User Story 2

- [ ] T034 [US2] Wire `ServerDetail.svelte` I/O tile to pass threshold props to `RingGauge.svelte` for all three I/O metrics: Disk Queue (warn: 2, crit: 5 from `thresholds.js` defaults), Input Delay (warn/crit from `perfConfig` or defaults 50ms/100ms), TCP Retransmits (warn: 5%, crit: 10% from defaults); verify all 6 ring types (CPU, Memory, Sessions, Disk Queue, Input Delay, TCP Retransmits) render with correct threshold colors
- [ ] T035 [US2] Handle edge cases in `RingGauge.svelte`: missing/zero metric data renders empty ring in neutral color (--color-subtle); when performance monitoring is disabled, use default thresholds from `thresholds.js`; when thresholds change in config, ring colors update reactively

**Checkpoint**: All ring gauges show green/amber/red — I/O coloring bug fixed

---

## Phase 7: User Story 3 — Configuration Modal UX Improvements (Priority: P2)

**Goal**: Config modal with improved button layout, dirty state, save/error feedback, consistent hover animations

**Independent Test**: Open config modal, modify setting, observe dirty indicator, save, observe success feedback, verify button positions

### Implementation for User Story 3

- [ ] T036 [US3] Build `ConfigModal.svelte` in `frontend/src/components/ConfigModal.svelte`: overlay modal with grace period radio pills + custom input, session warning threshold (0-100%), performance monitoring toggle + CPU/Memory/Input Delay warn/crit threshold inputs, per-session and RemoteFX checkboxes; load config via `fetchNotifyConfig()` on open
- [ ] T037 [US3] Implement button bar layout in `ConfigModal.svelte`: "Send Test" button aligned far-left (`mr-auto`), "Save" and "Close" buttons grouped far-right; all buttons use `.btn-brutal` class for consistent hover animation (translate -2px/-2px + shadow on hover, translate +2px/+2px on active)
- [ ] T038 [US3] Implement dirty-state detection in `ConfigModal.svelte`: `$state` dirty flag set when any input differs from loaded config snapshot; when dirty, modal background subtly shifts color (e.g., amber-tinted at 5% opacity); when clean, background returns to default
- [ ] T039 [US3] Implement save feedback in `ConfigModal.svelte`: on successful `saveNotifyConfig()`, display "Settings saved successfully" inline banner with green-tinted background flash (fades after 3s), clear dirty flag; on failure, display error message inline banner with red-tinted background flash (FR-015), dirty flag persists
- [ ] T040 [US3] Wire "Send Test" button in `ConfigModal.svelte` to call `sendNotifyTest()` from `api.js`; show success/failure feedback inline

**Checkpoint**: Config modal has correct button layout, dirty/save/error states all work, hover animations consistent

---

## Phase 8: User Story 4 — Notification Target Modal Scrollbar Consistency (Priority: P2)

**Goal**: Scrollbars in notification target modal match config modal styling

**Independent Test**: Open config modal, open notification target modal, resize window to trigger scrollbars, verify identical styling

### Implementation for User Story 4

- [ ] T041 [P] [US4] Build `NotificationTargets.svelte` in `frontend/src/components/NotificationTargets.svelte`: targets table within config modal showing type icon, destination, trigger count, repeat interval; add/edit/delete buttons per row
- [ ] T042 [P] [US4] Build `TargetEditModal.svelte` in `frontend/src/components/TargetEditModal.svelte`: layered modal (higher z-index than config modal) with type pills (Webhook/Ntfy/Email), destination input, trigger checkbox grid (12 triggers), repeat frequency dropdown; apply `.scrollbar-styled` class for consistent thin scrollbar
- [ ] T043 [P] [US4] Build `TargetDeleteModal.svelte` in `frontend/src/components/TargetDeleteModal.svelte`: confirmation dialog with target details, "Delete" and "Cancel" buttons using `.btn-brutal` styling
- [ ] T044 [US4] Verify `.scrollbar-styled` class (defined in T015) applies identically to both `ConfigModal.svelte` and `TargetEditModal.svelte`: thin scrollbar, subtle-colored thumb, accent color on hover, transparent track; test by resizing viewport to trigger overflow on both modals

**Checkpoint**: Scrollbars visually identical across all modal dialogs

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Final verification, cleanup, and cross-cutting improvements

- [ ] T045 [P] Visual regression comparison: screenshot existing dashboard (light + dark), screenshot migrated dashboard (light + dark), compare all sections for pixel-level fidelity
- [ ] T046 [P] Verify all 15 functional requirements (FR-001 through FR-015) per `spec.md`
- [ ] T047 [P] Verify all 8 success criteria (SC-001 through SC-008) per `spec.md`
- [ ] T048 Remove `internal/dashboard/dashboard.html` (replaced by embedded `dist/` directory)
- [ ] T049 [P] Update `justfile` prettier recipe to include Svelte files: `npx --yes prettier --write "frontend/src/**/*.svelte" "frontend/src/**/*.js"`
- [ ] T050 Update CSP comment block in `internal/dashboard/server.go` to document new security posture (no `'unsafe-inline'`, nonce-based theme script)
- [ ] T051 Verify bundle size: run `npx vite build --report` in `frontend/`, confirm total <150 KB (Layercake ~8KB + Svelte runtime ~15KB + app code)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion — BLOCKS all user stories
- **US5 Build Pipeline (Phase 3)**: Depends on Foundational — validates embed works
- **US6 Theme (Phase 4)**: Depends on Foundational — can run parallel with US5
- **US1 Core Dashboard (Phase 5)**: Depends on Foundational + US6 (needs theme/styling)
- **US2 I/O Ring Colors (Phase 6)**: Depends on US1 (needs RingGauge and ServerDetail)
- **US3 Config Modal UX (Phase 7)**: Depends on Foundational — can run parallel with US1
- **US4 Scrollbar Consistency (Phase 8)**: Depends on US3 (needs ConfigModal)
- **Polish (Phase 9)**: Depends on all user stories being complete

### User Story Dependencies

- **US5 (Build Pipeline)**: Independent after Foundational — MVP candidate
- **US6 (Theme)**: Independent after Foundational — parallel with US5
- **US1 (Core Dashboard)**: Depends on US6 for styling — main feature work
- **US2 (I/O Rings)**: Depends on US1's RingGauge.svelte and ServerDetail.svelte
- **US3 (Config Modal UX)**: Independent after Foundational — parallel with US1
- **US4 (Scrollbar)**: Depends on US3's ConfigModal for comparison baseline

### Within Each User Story

- State/API utilities before components
- Reusable primitives (RingGauge, chart layers) before composing components
- Integration and wiring last

### Parallel Opportunities

- T003, T004, T005, T006 (Setup) — all independent files
- T010, T011, T012, T013, T014 (Foundational) — all independent modules
- T019, T020 (US6 Nav + Footer) — independent components
- T022, T023, T024, T027, T028, T030, T031 (US1) — independent components/files
- T041, T042, T043 (US4) — independent modal components
- T045, T046, T047, T049 (Polish) — independent verification tasks

---

## Parallel Example: User Story 1

```bash
# Launch independent components in parallel:
Task T022: "Build CounterGrid.svelte"
Task T023: "Build StateBar.svelte"
Task T024: "Build chart primitives (AreaPath, LinePath, AxisX, AxisY)"
Task T027: "Build EventLog.svelte"
Task T028: "Build ServerTable.svelte"
Task T030: "Build RingGauge.svelte"
Task T031: "Build Sparkline.svelte"

# Then sequential (dependencies):
Task T025: "Build MetricsChart.svelte" (depends on T024 chart primitives)
Task T029: "Build ServerDetail.svelte" (depends on T030, T031)
Task T026: "Update refresh loop" (depends on T025)
Task T032: "Build HistoryModal.svelte"
Task T033: "Integrate all into App.svelte" (depends on all above)
```

---

## Implementation Strategy

### MVP First (US5 + US6 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (CRITICAL — blocks all stories)
3. Complete Phase 3: US5 — Build pipeline verified
4. Complete Phase 4: US6 — Theme verified
5. **STOP and VALIDATE**: Binary serves themed placeholder dashboard

### Incremental Delivery

1. Setup + Foundational → Foundation ready
2. US5 (Build Pipeline) → Embed works → Validate
3. US6 (Theme) → Visual identity established → Validate
4. US1 (Core Dashboard) → Full monitoring dashboard → Validate (primary milestone)
5. US2 (I/O Rings) → Bug fix applied → Validate
6. US3 (Config Modal UX) → UX improvements → Validate
7. US4 (Scrollbar) → Polish complete → Validate
8. Polish → Final verification → Ship

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Each user story should be independently completable and testable
- Commit after each task or logical group
- Stop at any checkpoint to validate story independently
- Layercake chart primitives (T024) are reused by both MetricsChart and Sparkline
- Threshold logic (T014) is reused by both RingGauge (US1) and I/O coloring fix (US2)
