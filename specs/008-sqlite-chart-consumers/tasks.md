---
description: "Task list for remaining persistent telemetry consumers"
---

# Tasks: Remaining Persistent Telemetry Consumers

**Input**: Design documents from `/specs/008-sqlite-chart-consumers/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md, data-model.md, contracts/

**Tests**: Included. This feature changes retained-history authority, dashboard query surfaces, and operator-visible chart behavior across storage, service, and frontend boundaries. Backend handler tests plus end-to-end quickstart validation are load-bearing.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g. `[US1]`, `[US2]`, `[US3]`)
- Include exact file paths in descriptions

## Path Conventions

- Backend dashboard handlers and tests live under `internal/dashboard/`
- Telemetry query logic lives under `internal/telemetry/`
- Dashboard frontend code lives under `frontend/src/`
- Feature docs live under `specs/008-sqlite-chart-consumers/`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the shared API, state, and verification scaffolding used by all stories.

- [x] T001 Review current history consumers in `frontend/src/App.svelte`, `frontend/src/lib/state.svelte.js`, `frontend/src/components/MetricsChart.svelte`, `frontend/src/components/ServerTable.svelte`, and `frontend/src/components/ServerDetail.svelte`; record any newly discovered in-scope/deferred surfaces in `specs/008-sqlite-chart-consumers/plan.md`
- [x] T002 [P] Add or refine fleet/seed API client helpers and JSDoc shapes in `frontend/src/lib/api.js` to match `specs/008-sqlite-chart-consumers/contracts/http-fleet-metrics.md` and `specs/008-sqlite-chart-consumers/contracts/http-metrics-seed.md`
- [x] T003 [P] Add shared Overview window state scaffolding and non-authoritative local UI persistence boundaries in `frontend/src/lib/state.svelte.js`
- [x] T004 [P] Add reusable dashboard test fixtures/helpers for fleet metrics and metrics-seed responses in `internal/dashboard/server_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Backend retained-history query surfaces and authority rules that must exist before any user story can ship.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T005 Implement `_fleet` host support and fleet aggregation query plumbing in `internal/dashboard/server.go`, reusing `internal/telemetry/metrics.go` tier selection and preserving current LOAD/HIC/SESSIONS/REMOTE FX semantics
- [ ] T006 Implement the bounded production metrics-seed route in `internal/dashboard/server.go` for recent per-host retained history used by sparkline consumers
- [ ] T007 Wire `frontend/src/lib/api.js` to the production fleet and metrics-seed routes and remove the dev-only assumption called out in its `fetchAllServerMetrics()` comments
- [ ] T008 Update `frontend/src/lib/state.svelte.js` so retained telemetry, not browser-local history, is treated as the authoritative source for migrated historical surfaces
- [ ] T009 Add foundational handler coverage in `internal/dashboard/server_test.go` for `_fleet`, metrics-seed, invalid range/resolution, bounded responses, and `storage_error` behavior

**Checkpoint**: Retained-history APIs and authority rules are in place; story work can proceed.

---

## Phase 3: User Story 1 - Persistent Overview Charts (Priority: P1) 🎯 MVP

**Goal**: Overview LOAD, HIC, SESSIONS, and REMOTE FX charts load retained history from SQLite-backed queries on a fresh browser session.

**Independent Test**: Populate retained telemetry, clear browser-local state, load the Overview page, and verify all four chart families render retained history without requiring warmed localStorage.

### Tests for User Story 1 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [ ] T010 [US1] Add backend retained-history tests for `_fleet` data, empty series responses, and retained coverage bounds in `internal/dashboard/server_test.go`

### Implementation for User Story 1

- [ ] T011 [P] [US1] Replace synthesized fleet bootstrap logic in `frontend/src/App.svelte` with retained fleet-history loading for Overview charts
- [ ] T012 [P] [US1] Narrow `metricsHistory`, `sessionHistory`, and `remoteFxHistory` usage in `frontend/src/lib/state.svelte.js` so they no longer behave as browser-local retained-history authorities
- [ ] T013 [US1] Rewire LOAD and HIC Overview data preparation in `frontend/src/components/MetricsChart.svelte` to consume retained fleet series while preserving current aggregation semantics
- [ ] T014 [US1] Rewire SESSIONS and REMOTE FX Overview data preparation in `frontend/src/components/MetricsChart.svelte` to consume retained fleet series while preserving current aggregation semantics
- [ ] T015 [US1] Add explicit empty and query-error states for each Overview chart family in `frontend/src/components/MetricsChart.svelte`
- [ ] T016 [US1] Update retained-history contract and operator validation notes for Overview loading in `specs/008-sqlite-chart-consumers/contracts/http-fleet-metrics.md` and `specs/008-sqlite-chart-consumers/quickstart.md`

**Checkpoint**: Overview charts load retained history on a fresh browser session and remain usable after service restart.

---

## Phase 4: User Story 2 - Historical Navigation Without Visual Regression (Priority: P1)

**Goal**: Overview charts keep their current visual meaning while gaining one shared 5M/1H/1D/3D/5D window plus zoom, pan, and scroll.

**Independent Test**: On the Overview page, switch among 5M/1H/1D/3D/5D, wheel-zoom, and drag-pan; confirm all chart families move together, preserve their visuals, and never settle on stale data.

### Tests for User Story 2 ⚠️

- [ ] T017 [US2] Add backend tier/degradation tests for fleet windows and response bounds in `internal/dashboard/server_test.go`

### Implementation for User Story 2

- [ ] T018 [P] [US2] Implement shared Overview window/preset state, reset behavior, and source tracking in `frontend/src/lib/state.svelte.js`
- [ ] T019 [P] [US2] Update Overview bootstrap and event wiring in `frontend/src/App.svelte` so all chart families use one shared time window
- [ ] T020 [US2] Add 5M/1H/1D/3D/5D pill controls and shared selection behavior in `frontend/src/components/MetricsChart.svelte`
- [ ] T021 [US2] Add shared wheel-zoom, drag-pan, and stale-response handling in `frontend/src/components/MetricsChart.svelte` and `frontend/src/components/chart/InteractiveTimeChart.svelte`
- [ ] T022 [US2] Preserve current legends, current-value displays, and chart-family semantics under shared-window navigation in `frontend/src/components/MetricsChart.svelte`
- [ ] T023 [US2] Update navigation and empty/error-state validation steps in `specs/008-sqlite-chart-consumers/contracts/http-fleet-metrics.md` and `specs/008-sqlite-chart-consumers/quickstart.md`

**Checkpoint**: Shared-window Overview navigation works across all chart families without changing their established meaning.

---

## Phase 5: User Story 3 - Remove Remaining Ephemeral History Dependencies (Priority: P2)

**Goal**: Remaining in-scope dashboard history consumers use retained telemetry when available instead of browser-local accumulation.

**Independent Test**: Clear browser-local state, reload from a fresh workstation/profile, and verify migrated sparkline and fallback-history consumers still show retained history.

### Tests for User Story 3 ⚠️

- [ ] T024 [US3] Add production metrics-seed handler tests for bounded per-host history, empty responses, and `storage_error` behavior in `internal/dashboard/server_test.go`

### Implementation for User Story 3

- [ ] T025 [P] [US3] Replace dev-only metrics-seed assumptions in `frontend/src/lib/api.js` and `frontend/src/App.svelte` with the production retained-history seed route
- [ ] T026 [P] [US3] Migrate per-host sparkline cold-start behavior in `frontend/src/components/ServerTable.svelte` to retained `serverMetrics` seed data
- [ ] T027 [US3] Migrate `frontend/src/components/ServerDetail.svelte` fallback history usage to retained `serverMetrics` seed data and preserve existing fallback semantics
- [ ] T028 [US3] Remove obsolete local-retention persistence and stale comments for migrated historical surfaces in `frontend/src/lib/state.svelte.js`
- [ ] T029 [US3] Document migrated vs deferred historical consumers in `specs/008-sqlite-chart-consumers/plan.md`, `specs/008-sqlite-chart-consumers/contracts/http-metrics-seed.md`, and `specs/008-sqlite-chart-consumers/quickstart.md`

**Checkpoint**: In-scope non-Overview dashboard history consumers load from retained telemetry on cold start.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Finish regression coverage, documentation, and operator validation across all stories.

- [ ] T030 [P] Update `FEATURES.md` to remove or revise the now-promoted fleet persistence proposal and reflect feature 008 ownership
- [ ] T031 Clean up stale browser-local-authority comments and dead code paths in `frontend/src/App.svelte`, `frontend/src/lib/api.js`, `frontend/src/lib/state.svelte.js`, and `internal/dashboard/server.go`
- [ ] T032 Verify observability paths for retained-history empty, unavailable, and query-error states in `internal/dashboard/server.go`, `frontend/src/components/MetricsChart.svelte`, and `specs/008-sqlite-chart-consumers/quickstart.md`
- [ ] T033 Run the validation flow in `specs/008-sqlite-chart-consumers/quickstart.md` against a fresh browser profile and record any doc fixes in that file
- [ ] T034 Run `go test ./...` and fix any failures in touched files
- [ ] T035 Run `just lint` and fix any warnings in touched files

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies; can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion; blocks all user stories
- **User Story 1 (Phase 3)**: Depends on Foundational completion; delivers the MVP
- **User Story 2 (Phase 4)**: Depends on User Story 1 because shared Overview navigation builds on retained Overview data
- **User Story 3 (Phase 5)**: Depends on Foundational completion; may proceed after US1 if resourcing allows, but shares frontend authority changes with US1
- **Polish (Phase 6)**: Depends on all desired user stories being complete

### User Story Dependencies

- **User Story 1 (P1)**: First shippable increment; no dependency on later stories
- **User Story 2 (P1)**: Depends on US1's retained Overview data path
- **User Story 3 (P2)**: Reuses the retained-history authority model from US1 but is otherwise separable from US2

### Within Each User Story

- Required tests MUST be written and fail before implementation
- Backend query surfaces before frontend consumers
- Shared state before chart wiring
- Story-specific docs/contracts updated before story closeout
- Story must be independently validated before moving on

### Parallel Opportunities

- T002, T003, and T004 can run in parallel
- T011 and T012 can run in parallel after T010
- T018 and T019 can run in parallel after T017
- T025 and T026 can run in parallel after T024
- T030 can run in parallel with other polish tasks

---

## Parallel Example: User Story 1

```text
# After T010 fails, launch the independent frontend authority rewires together:
Task: "T011 [US1] Replace synthesized fleet bootstrap logic in frontend/src/App.svelte"
Task: "T012 [US1] Narrow metricsHistory/sessionHistory/remoteFxHistory authority in frontend/src/lib/state.svelte.js"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational
3. Complete Phase 3: User Story 1
4. **STOP and VALIDATE**: Confirm Overview retained history works from a fresh browser profile
5. Demo retained-history continuity across service restart

### Incremental Delivery

1. Setup + Foundational → retained query surfaces ready
2. Add User Story 1 → retained Overview history ships
3. Add User Story 2 → shared-window navigation ships
4. Add User Story 3 → remaining in-scope historical consumers ship
5. Finish with Polish → docs, observability, full verification

### Parallel Team Strategy

With multiple developers:

1. Developer A: backend fleet/seed query work in `internal/dashboard/server.go`
2. Developer B: shared Overview state and chart interaction work in `frontend/src/lib/state.svelte.js` and `frontend/src/components/MetricsChart.svelte`
3. Developer C: sparkline / server-detail consumer migration in `frontend/src/components/ServerTable.svelte` and `frontend/src/components/ServerDetail.svelte`

---

## Notes

- All tasks follow the required checklist format with IDs, labels, and concrete file paths
- Tests are intentionally included because this feature changes storage-backed query behavior and operator-visible dashboard output
- Do not introduce a new telemetry class unless implementation proves the retained store cannot express an already-approved chart semantic
- Do not let browser-local history become fallback truth for empty or error states
