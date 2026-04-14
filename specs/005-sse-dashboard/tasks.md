# Tasks: SSE Dashboard

**Input**: Design documents from `/specs/005-sse-dashboard/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/sse-api.md

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

---

## Phase 1: Setup

**Purpose**: No new project scaffolding needed — all infrastructure exists. This phase creates the single new file.

- [ ] T001 Create Broker struct with Subscribe/Unsubscribe/Broadcast methods in internal/dashboard/broker.go

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Wire the broker into the dashboard server so all user stories can build on top.

**CRITICAL**: No user story work can begin until this phase is complete.

- [ ] T002 Add Broker field to DashboardServer and initialize it in NewDashboardServer in internal/dashboard/server.go
- [ ] T003 Register SSE endpoint `GET /api/v1/events` with session auth middleware in internal/dashboard/server.go
- [ ] T004 Implement handleSSE handler (set SSE headers, subscribe, loop writing events from channel, unsubscribe on disconnect) in internal/dashboard/server.go

**Checkpoint**: SSE endpoint exists and can accept connections. No events are broadcast yet.

---

## Phase 3: User Story 1 — Instant Server State Updates (Priority: P1) MVP

**Goal**: Connected browsers see server state changes within 2 seconds of the report arriving.

**Independent Test**: Open dashboard in browser, trigger a drain mode change on a registered server, verify the dashboard updates without waiting for the poll cycle.

### Implementation for User Story 1

- [ ] T005 [US1] Broadcast server_update event from handleReport after state.Update() in internal/dashboard/server.go
- [ ] T006 [US1] Broadcast server_update event from local check path (dashState.ReportLocal) in internal/svc/handler.go or internal/dashboard/server.go
- [ ] T007 [US1] Add EventSource connection setup on login in frontend/src/App.svelte
- [ ] T008 [US1] Add SSE event consumer that updates appState.servers from server_update events in frontend/src/lib/state.svelte.js
- [ ] T009 [US1] Close EventSource on logout in frontend/src/App.svelte
- [ ] T010 [US1] Add broker and SSE handler tests in internal/dashboard/server_test.go

**Checkpoint**: Server state updates stream to connected browsers in real-time. Poll fallback still runs.

---

## Phase 4: User Story 2 — Resilient Connection with Graceful Fallback (Priority: P2)

**Goal**: Browser automatically reconnects after network interruption and resumes live updates.

**Independent Test**: Open dashboard, disconnect network briefly, reconnect, verify live updates resume within 5 seconds.

### Implementation for User Story 2

- [ ] T011 [US2] Handle EventSource onerror — detect auth failures (401) and clear session in frontend/src/App.svelte
- [ ] T012 [US2] Verify EventSource auto-reconnect works with session cookie in frontend/src/App.svelte
- [ ] T013 [US2] Ensure handleSSE detects session expiry and closes connection in internal/dashboard/server.go

**Checkpoint**: Connection resilience verified — auto-reconnect, auth error handling, session expiry cleanup.

---

## Phase 5: User Story 3 — Multi-Browser Support (Priority: P3)

**Goal**: Multiple concurrent browsers all receive updates simultaneously without resource exhaustion.

**Independent Test**: Open 5+ browser tabs, trigger a state change, verify all tabs update within 2 seconds.

### Implementation for User Story 3

- [ ] T014 [US3] Implement non-blocking send in Broker.Broadcast with slow subscriber eviction in internal/dashboard/broker.go
- [ ] T015 [US3] Add subscriber cleanup on channel-full eviction (log warning) in internal/dashboard/broker.go

**Checkpoint**: 50 concurrent browsers supported with graceful handling of slow subscribers.

---

## Phase 6: Settings Broadcast (Clarification: FR-009)

**Goal**: Settings changes are broadcast to all connected browsers immediately.

**Independent Test**: Open dashboard in two browsers, change a threshold in one, verify the other reflects the change without polling.

### Implementation for Settings Broadcast

- [ ] T016 [P] Broadcast settings_update event from handlePutSettings after successful save in internal/dashboard/server.go
- [ ] T017 [P] Add SSE event consumer for settings_update that refreshes appState.config in frontend/src/lib/state.svelte.js

**Checkpoint**: Settings changes stream to all connected browsers.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation, OpenAPI, and final validation.

- [ ] T018 [P] Add /api/v1/events endpoint to OpenAPI spec in internal/dashboard/openapi.yaml
- [ ] T019 [P] Document SSE endpoint in docs/guide.html
- [ ] T020 Update mock-api.js with SSE simulation for dev mode in frontend/dev/mock-api.js
- [ ] T021 Run `just lint` and `go test ./...` — fix any issues
- [ ] T022 Manual end-to-end validation per quickstart.md scenarios

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — T001 creates broker.go
- **Foundational (Phase 2)**: Depends on T001 — wires broker into server
- **US1 (Phase 3)**: Depends on Phase 2 — first broadcast + frontend consumer
- **US2 (Phase 4)**: Depends on US1 — resilience on top of working SSE
- **US3 (Phase 5)**: Can start after Phase 2 (independent of US1/US2) — broker hardening
- **Settings (Phase 6)**: Can start after Phase 2 (independent of US1) — parallel with US1
- **Polish (Phase 7)**: Depends on all previous phases

### Within Each User Story

- Backend broadcast before frontend consumer
- Frontend EventSource setup before event routing
- Core implementation before error handling

### Parallel Opportunities

- T016 and T017 (settings broadcast) can run in parallel with US1 (T005–T010)
- T014 and T015 (multi-browser hardening) can run in parallel with US2 (T011–T013)
- T018 and T019 (docs) can run in parallel with each other

---

## Parallel Example: After Phase 2 Completes

```text
# These can run in parallel:
Stream A: T005 → T006 → T007 → T008 → T009 → T010  (US1: instant updates)
Stream B: T014 → T015                                 (US3: multi-browser)
Stream C: T016, T017                                   (Settings broadcast)
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Create broker.go (T001)
2. Complete Phase 2: Wire broker into server (T002–T004)
3. Complete Phase 3: US1 — broadcast + frontend consumer (T005–T010)
4. **STOP and VALIDATE**: Open dashboard, trigger state change, verify <2s update
5. Deploy if ready — poll fallback ensures safety

### Incremental Delivery

1. T001–T004 → Broker + SSE endpoint ready
2. T005–T010 → Real-time updates working (MVP!)
3. T011–T013 → Reconnection resilience
4. T014–T015 → Multi-browser hardening
5. T016–T017 → Settings broadcast
6. T018–T022 → Docs + polish

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- No new Go or frontend dependencies required
- Broker uses buffered channels with non-blocking send — no goroutine leaks
- Existing poll_interval fallback is untouched — SSE supplements, never replaces
- Commit after each task or logical group
