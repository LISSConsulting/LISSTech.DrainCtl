# Tasks: Fleet Session Operator Metrics

**Input**: Design documents from `/specs/015-fleet-session-operator-metrics/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

## Phase 1: Setup

- [X] T001 Confirm the isolated worktree branch and Speckit artifact set in `specs/015-fleet-session-operator-metrics/`
- [X] T002 Update `AGENTS.md` to reference the active implementation plan

## Phase 2: Foundational anonymous aggregates

- [X] T003 [P] Implement CPU and memory histogram codecs with explicit error bounds in `internal/sessiondata/workload.go`
- [X] T004 [P] Add deterministic codec, percentile, zero/missing, and merge tests in `internal/sessiondata/workload_test.go`
- [X] T005 Implement snapshot-to-anonymous-aggregate normalization and eligibility in `internal/sessiondata/workload.go`
- [X] T006 Add schema-v5 workload tier tables and idempotent migration in `internal/telemetry/migrate_session_workload.go` and `internal/telemetry/schema.go`
- [X] T007 Add migration/schema privacy tests proving no identity columns in `internal/telemetry/schema_test.go`

## Phase 3: User Story 1 — Understand fleet session workload (P1)

**Goal**: Persist and query pooled session-weighted AVG/P95 rather than host-summary statistics.

**Independent Test**: Unequal host populations produce pooled AVG/P95 within contract bounds.

- [X] T008 [US1] Persist newest successful anonymous aggregate per host/minute atomically in `internal/telemetry/session_store.go` and `internal/telemetry/session_workload.go`
- [X] T009 [US1] Persist fatal-attempt coverage that replaces same-minute stale success in `internal/telemetry/session_workload.go`
- [X] T010 [US1] Implement selected-host raw workload merge/query in `internal/telemetry/session_workload.go`
- [X] T011 [US1] Add unequal-population, normalization, disconnected, valid-zero, missing-value, and newest-wins tests in `internal/telemetry/session_workload_test.go`

## Phase 4: User Story 2 — Distinguish broad workload from a heavy tail (P1)

**Goal**: Expose AVG/P95 plus CPU threshold breadth, rates, denominator, and average/max concurrency.

**Independent Test**: Equal-P95 fixtures with different breadth produce different AVG/count/rate results.

- [X] T012 [US2] Retain base-sample counts and CPU ≥5/≥20 sum/max fields in workload rows
- [X] T013 [US2] Compute threshold rates and metric-specific observation denominators in fleet workload queries
- [X] T014 [US2] Add concentrated-tail versus broad-load tests in `internal/telemetry/session_workload_test.go`

## Phase 5: User Story 3 — Judge data coverage before trusting a point (P1)

**Goal**: Make expected/contributing/empty/error/stale/offline/unsupported coverage explicit and render gaps at zero contribution.

**Independent Test**: Missing and fatal selected hosts yield partial coverage; zero contributors yield no numeric point.

- [X] T015 [US3] Merge success, successful-empty, and error coverage per selected host in `internal/telemetry/session_workload.go`
- [X] T016 [US3] Classify selected hosts without rows using registered host state in `internal/dashboard/handlers_metrics.go`
- [X] T017 [US3] Add partial, successful-empty, fatal, unsupported, stale/offline, and zero-contributor API tests in `internal/dashboard/server_test.go`

## Phase 6: User Story 4 — Preserve truthful history and filters (P2)

**Goal**: Preserve semantics through 5-minute/hourly rollups, retention, restarts, and selected-host queries.

**Independent Test**: Independently merged fixtures match every tier and host subset.

- [X] T018 [US4] Implement per-host raw-to-5-minute and 5-minute-to-hourly workload rollups in `internal/telemetry/aggregator.go`
- [X] T019 [US4] Add workload tier deletion to metric retention in `internal/telemetry/retention.go`
- [X] T020 [US4] Add every-tier weighted AVG/P95/threshold/coverage and retention tests in `internal/telemetry/aggregator_test.go` and `internal/telemetry/retention_test.go`
- [X] T021 [US4] Add `session_workload` response contract and query wiring in `internal/dashboard/handlers_metrics.go`, `internal/dashboard/interfaces.go`, and `internal/dashboard/server.go`
- [X] T022 [US4] Update OpenAPI and the feature HTTP contract with normalization, eligibility, gaps, coverage, and approximation semantics

## Phase 7: User Stories 1–4 — Overview Sessions UI

- [X] T023 [US1] Add typed `session_workload` API parsing in `frontend/src/lib/api.js`
- [X] T024 [US1] Replace legacy host-summary adapters with Fleet Session P95/AVG in `frontend/src/lib/chart-data.js`
- [X] T025 [US2] Adapt CPU-active average/max counts, rates, and denominator for tooltips in `frontend/src/lib/chart-data.js`
- [X] T026 [US3] Add coverage metadata and partial/unavailable presentation to shared chart tooltips in `frontend/src/components/MetricsChart.svelte` and chart components
- [X] T027 [US1] Replace labels/help text and preserve P95-only severity in `frontend/src/components/MetricsChart.svelte`
- [X] T028 [US4] Remove obsolete `Peak host P95` / `Typical host P95` semantics and update frontend tests

## Phase 8: User Story 5 — Preserve identity boundary (P3)

**Goal**: Ensure anonymous workload retention cannot be attributed to a user/session episode.

**Independent Test**: Schema, blobs, API, logs, and fixtures contain no prohibited identity.

- [X] T029 [US5] Add storage/API privacy assertions covering identity visibility modes and retained rows
- [X] T030 [US5] Document host/time-only future history correlation and non-attribution boundary in operator documentation

## Phase 9: Polish and cross-cutting verification

- [X] T031 Update `README.md`, `CHANGELOG.md`, and operator guidance for new metrics, coverage, upgrade gaps, and unchanged Sessions Trend/Utilization
- [X] T032 Run focused backend/frontend tests and fix all failures
- [X] T033 Run the dashboard with deterministic data and browser-smoke labels, tooltips, host filter, all windows, partial coverage, and gaps
- [X] T034 Run `go test ./...`, frontend build/tests, `just lint`, and `prek run --all-files` with zero warnings
- [X] T035 Review the complete diff against every FR/SC and remove obsolete code, compatibility aliases, and temporary artifacts

## Dependencies and Execution Order

- Phase 1 precedes all implementation.
- Histogram/schema foundation (T003–T007) blocks ingestion/query work.
- US1 storage/query (T008–T011) blocks breadth, coverage, rollups, API, and UI.
- US2 and US3 may proceed independently after US1.
- US4 rollups/API require US1–US3 aggregate semantics.
- UI requires the US4 response contract.
- Privacy and docs follow the stable persisted/API shape.
- Final verification runs only after all user stories are complete.
