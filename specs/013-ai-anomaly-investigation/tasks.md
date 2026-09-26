# Tasks: AI Anomaly Investigation

**Input**: Design documents from `/specs/013-ai-anomaly-investigation/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `quickstart.md`, and `contracts/`

**Tests**: Required. Tests are listed before the implementation they prove. The dashboard frontend has only `dev`, `build`, and `preview`; its behavior is verified with the production build and a real browser/UI scenario, not an invented frontend test framework.

**Organization**: Setup establishes Windows-only package roots and fixtures. The blocking foundational phase establishes every shared contract and durable ownership path before story work. Story phases remain independently testable through their stated durable interfaces.

## Format: `[ID] [P?] [Story] Description`

- **[P]** marks work in distinct files with no unmet dependency in this checklist. Shared hotspots are serialized and are never marked `[P]`.
- **[US#]** maps a task to a user story. Setup, foundational, and cross-cutting tasks intentionally have no story label.
- Every new Go source and Go test file must carry `//go:build windows`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish isolated Windows-only roots and fixtures without changing runtime behavior.

- [ ] T001 Create `internal/investigation/doc.go` as the Windows-only package declaration and package comment for the central provider/evidence/lifecycle owner.
- [ ] T002 [P] Create `internal/sessiondrop/doc.go` as the Windows-only package declaration and package comment for the lower-tail detector.
- [ ] T003 [P] Create `specs/013-ai-anomaly-investigation/fixtures/schema-v3.sql` with representative pre-v4 telemetry, audit, durable `event_spikes`, exclusions, and retained-host rows for migration/rollback fixtures.
- [ ] T004 [P] Create `specs/013-ai-anomaly-investigation/fixtures/provider-canaries.json` with synthetic forbidden-data sentinels for outbound and persisted-artifact boundary checks.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Define and prove shared configuration, storage, accepted-report ownership, retention, and lifecycle contracts before implementing them. **No user-story implementation starts until this phase is complete.**

### Foundational tests (write first)

- [ ] T005 [P] Add configuration contract tests in `config_test.go` for default-disabled provider access/automatic mode, complete acknowledgement, an explicitly selected credential command (reject omission without mutating the retained credential), validated session-drop bounds, rejection of writes to fixed detector constants, DPAPI preserve/replace/clear, and absence from machine/shared-settings projections.
- [ ] T006 [P] Add v3-to-v4 migration and rollback fixture tests in `internal/telemetry/schema_test.go` using `specs/013-ai-anomaly-investigation/fixtures/schema-v3.sql` for additive DDL, old-record readability, constraints/indexes, no backfill/egress, older-reader preservation, disabled re-upgrade, and `pre_upgrade_context`.
- [ ] T007 [P] Add investigation-store tests in `internal/telemetry/investigation_attempts_test.go` for stable source-kind/ID-only rows, no canonical-host persistence, queue cap, root/retry uniqueness, terminal immutability, FIFO claim, restart interruption, safe counts, canonical closed provenance validation outcomes (`accepted` and `insufficient_evidence`) only, and WAL reader/writer snapshot contention.
- [ ] T008 [P] Add session-drop-store tests in `internal/telemetry/session_drops_test.go` for host-keyed anomaly/baseline isolation, 96 quarter-hour source-local slots across offsets/DST, local-date maturity, 96 slots/all-hours state, report-order atomicity, restart replay, exact `duplicate_report_epoch` classification enum, authorized source lookup, and v4 constraints.
- [ ] T009 [P] Add retention tests in `internal/telemetry/retention_test.go` for independent attempt `created_at` expiry, source `detected_at` expiry, cascaded attempt artifacts, chunked independent deletion of expired session-drop source rows while preserving registered-host baseline/detector state, opaque retained retry predecessor IDs, baseline preservation, and maintenance counts.
- [ ] T010 [P] Add permanent-host-removal cleanup tests in `internal/telemetry/exclusions_test.go` for deleting only the removed host's session-drop baseline/order/cooldown state while preserving other hosts and investigation history/source retention rules.
- [ ] T011 [P] Add bounded evtspike source-by-ID lookup tests in `internal/telemetry/event_spikes_test.go` for confirmed eligibility, idempotent lookup, no automatic consumption/backfill, and unchanged existing evtspike records.
- [ ] T012 [P] Add accepted-report callback tests in `internal/dashboard/store_test.go` for one common post-accept hook across HTTP and local paths, exactly-once observation, and unchanged `CheckResult`.
- [ ] T013 [P] Add service-loop construction/re-enable tests in `internal/svc/service_loop_test.go` for both dashboard construction paths, default-disabled owners, and safe re-enable/restart without agent contract changes.
- [ ] T014 [P] Add dashboard ownership-interface tests in `internal/dashboard/interfaces_test.go` for host-free investigation interfaces and accepted-report callback contracts without widening machine authority.

### Foundational implementation

- [ ] T015 Implement `InvestigationProviderConfig`, write-only credential handling, acknowledgement gating, validated session-drop defaults/ranges, rejection of omitted credential commands and fixed-detector writes, DPAPI persistence, and machine/shared-settings exclusions in `config.go`.
- [ ] T016 [P] Define Windows-only host-free source links, attempt states/reasons, strict result/status DTOs, canonical closed provenance validation outcomes, and fixed provider constants in `internal/investigation/attempts.go`.
- [ ] T017 [P] Define Windows-only session-count observation, detector configuration, durable anomaly/baseline/gap/classification, and SSE projection types in `internal/sessiondrop/status.go`.
- [ ] T018 Implement v4 additive DDL, checks, immutable-terminal trigger, indexes, and schema-version migration in `internal/telemetry/schema.go`.
- [ ] T019 [P] Implement transactional host-free attempt/evidence/result/provenance, accepting only closed local provenance (`provider_profile=typesafe_jev`, `requested_model=jev-latest`, request timing/byte counts, and canonical validation outcome), queue claim, source dedupe, retry lineage, recovery, WAL-safe snapshot reads, and safe-count APIs in `internal/telemetry/investigation_attempts.go`.
- [ ] T020 [P] Implement transactional host-keyed session-drop anomaly/baseline APIs for source-local 96 quarter-hour slots, local-date maturity, all-hours state, order watermarks, confirmation windows, drain horizons, cooldown, restart replay, exact classifications, and authorization-gated source lookup in `internal/telemetry/session_drops.go`.
- [ ] T021 Extend chunked `AuditDays` retention and maintenance outcomes in `internal/telemetry/retention.go` so attempts expire by `created_at`, sources by their own retention anchor, expired session-drop sources are independently deleted by `detected_at` in chunks, dependent artifacts cascade safely, and registered-host baselines/detector state remain independent.
- [ ] T022 Implement permanent-host removal cleanup in `internal/telemetry/exclusions.go` for session-drop detector state without deleting unrelated sources, attempts, or baselines.
- [ ] T023 Implement bounded confirmed-evtspike source-by-ID lookup in `internal/telemetry/event_spikes.go` using existing `EventSpike` APIs and identity; preserve `Insert`, `Recent`, and `Range`, and do not add consumer state, automatic-root orchestration, detection, notification, or payload changes.
- [ ] T024 Implement the common accepted-report post-commit hook in `internal/dashboard/store.go` for both HTTP and local report acceptance; preserve `CheckResult` unchanged.
- [ ] T025 Implement dashboard owner/callback interfaces in `internal/dashboard/interfaces.go` so report intake, evtspike lookup, investigation automatic-root orchestration, and session-drop components retain their existing responsibility boundaries.
- [ ] T026 Implement both dashboard construction and re-enable paths in `internal/svc/service_loop.go` to construct/start/stop central telemetry, investigation, session-drop, and recovery owners default-disabled.
- [ ] T027 Construct the dashboard-owned subsystems and lifecycle binding in `internal/dashboard/subsystem_windows.go` without changing agent, notification, or existing evtspike wiring.
- [ ] T028 Wire accepted observations and bounded evtspike source lookup through `internal/dashboard/server.go`, preserving `CheckResult`, `SpikePayload`, `/api/v1/spike`, and machine routes.

**Checkpoint**: Shared configuration, migration/store/retention contracts, permanent-host cleanup, common accepted-report handoff, bounded evtspike lookup, and both service-loop lifecycle paths are proved before story implementation.

---

## Phase 3: User Story 1 — Investigate a confirmed anomaly with bounded evidence (Priority: P1) 🎯 MVP

**Goal**: An authorized dashboard-group operator can request one investigation for an eligible durable confirmed anomaly and receive an immutable strict typed hypothesis based only on bounded host-free evidence.

**Independent Test**: With a persisted confirmed source, acknowledged synthetic configuration, and an injected recording transport at the fixed URL, request an attempt and prove one transmission, strict typed result, and zero forbidden canaries in request or persisted artifacts.

### Tests for User Story 1 (write first)

- [ ] T029 [P] [US1] Add allowlist-builder unit tests in `internal/investigation/evidence_test.go` using `specs/013-ai-anomaly-investigation/fixtures/provider-canaries.json` for the exact ordered `state_json` provider contract (`v`, `snapshot_at_ms`, `window`, `source`, `context`, optional local/fleet, `omitted`), separate evidence-table `source_time_ms` metadata, source/peer anonymization, exact clipped ±30-minute window, known/custom channels, deterministic omissions, `pre_upgrade_context`, retention/freshness/drain/detector insufficiency, zero values, fleet insufficiency, and all state/request byte limits.
- [ ] T030 [P] [US1] Add strict fixed-provider client tests in `internal/investigation/provider_test.go` for URL/TLS/header rules, four exact questions, redirect refusal, bounded headers/body/error body, acceptance of every exact metric enum member in `contracts/provider-evidence.md` and rejection of only out-of-enum/unknown values, ineligible session classifications, object-not-string state rejection, duplicate/unknown/trailing JSON rejection, unsupported content encodings, half-up PPM rounding, full persisted probability/confidence behavior, response `model` echo canaries (credential, host/IP, URL/path, and prose) used only for bounded shape validation then discarded, and rejecting prose, unsupported enums, dynamic/missing/duplicate/fifth answers, and invalid exact-decimal probability/confidence values.
- [ ] T031 [US1] Add injected-`http.RoundTripper` fixed-URL provider integration tests in `internal/investigation/worker_test.go` for authorized manual completed lifecycle, ten evtspike automatic-root redeliveries yielding one root and one send, Bearer-header-only credential use, closed local provenance containing only fixed profile/requested-model, timing/byte counts, and canonical validation outcome, and every forbidden canary—including a provider `model` echo of credential, host/IP, URL/path, or prose—absent from captured body, evidence, result, provenance, failure, and diagnostics.
- [ ] T032 [US1] Add dashboard request/history/detail contract tests in `internal/dashboard/handlers_investigations_test.go` for authorization before existence lookup/evidence creation, manual root dedupe, source eligibility, automatic-root dedupe, strict errors, host-free attempt projections, and hypothesis/insufficiency projection.

### Implementation for User Story 1

- [ ] T033 [US1] Implement field-by-field `EvidenceV1` assembly and canonical compact serialization in `internal/investigation/evidence.go` according to `contracts/provider-evidence.md`, including the exact ordered `state_json` contract and separate `source_time_ms` evidence metadata, using deterministic bounded omission rather than redaction or raw-struct marshaling.
- [ ] T034 [US1] Implement direct fixed `typesafe_jev` request construction, bounded transport reads, strict response decoding with the canonical metric enum, response-`model` shape validation followed by discard, exact decimal/half-up PPM parsing, full probability/confidence persistence, closed local provenance only, and safe failure mapping in `internal/investigation/provider.go`.
- [ ] T035 [US1] Implement authorized root creation, source eligibility, atomic evidence-before-queue persistence, ten-redelivery-safe automatic-root dedupe for durable evtspikes, and no concurrent source work in `internal/investigation/attempts.go`.
- [ ] T036 [US1] Implement the single investigation worker in `internal/investigation/worker.go` with current-config validation, 100 nonterminal cap, 10/minute burst-2 rate limit, 30-second timeout, one transmission, and strict terminal transitions.
- [ ] T037 [US1] Implement dedicated authorized manual-create/history/detail handlers in `internal/dashboard/handlers_investigations.go`; resolve canonical host only after authorization in deterministic-source projections.
- [ ] T038 [US1] Register investigation routes and dashboard-owned durable evtspike automatic-root handoff in `internal/dashboard/server.go` without changing existing evtspike/API/machine behavior.
- [ ] T039 [US1] Document investigation source/history/detail schemas, fixed error codes, automatic-root behavior, and host-free attempt boundary in `internal/dashboard/openapi.yaml`.

**Checkpoint**: US1 independently delivers one bounded manual investigation for a confirmed event spike with a durable typed hypothesis and a provably host-free/provider-safe egress boundary.

---

## Phase 4: User Story 2 — Review provenance and retry a failed or insufficient investigation (Priority: P1)

**Goal**: Authorized operators can inspect immutable history and safely retry only failed or insufficient attempts; restart, retention, and provider/storage failure states remain auditable without re-sending work.

**Independent Test**: Create completed, failed, and insufficient attempts; restart during controlled running work; verify immutable history and one linked retry; then expire attempts by creation time while preserving newer retry/source/baseline state.

### Tests for User Story 2 (write first)

- [ ] T040 [P] [US2] Add lifecycle/retry/recovery integration tests in `internal/investigation/attempts_test.go` for completed-root rejection, failed/insufficient-only linked retry, authorization and predecessor lookup before retry work, retained failed/insufficient predecessor whose expired deterministic source returns `404 source_not_found` before evidence creation, queueing, or provider work, fresh retry evidence when its source remains available, terminal immutability, interrupted recovery, and zero re-send.
- [ ] T041 [US2] Add injected-`http.RoundTripper` provider failure integration tests in `internal/investigation/worker_test.go` at the fixed URL for storage-before-egress, timeout, network, auth, rate, upstream, malformed/oversized response, queue-full rejection, and rate-delayed—not failed—work.
- [ ] T042 [US2] Add attempt history/detail/retry authorization and retention integration tests in `internal/dashboard/handlers_investigations_test.go` for authorized immutable provenance, unauthorized/machine zero disclosure, expired predecessor linkage, an expired retry source returning `source_not_found` with no evidence, new attempt, or provider work, and `AuditDays` behavior.
- [ ] T043 [US2] Add post-commit provider-failure observability tests in `internal/dashboard/broker_test.go` for safe attempt/status events after durable writes and no evidence/result/provenance/diagnostic/canonical-host disclosure.

### Implementation for User Story 2

- [ ] T044 [US2] Extend immutable attempt lifecycle, linked retry creation, startup recovery, queue cancellation, and safe structured storage/provider diagnostics in `internal/investigation/attempts.go`; after authorization and predecessor lookup, reject a retry whose deterministic source has expired with `404 source_not_found` before evidence, a new attempt, or provider work.
- [ ] T045 [US2] Extend worker finalization in `internal/investigation/worker.go` to atomically persist valid closed local provenance/results, suppress cause/impact for insufficient evidence, retain closed failure reasons only, and never retry a wire send.
- [ ] T046 [US2] Implement authorized retry plus immutable attempt-detail/history responses in `internal/dashboard/handlers_investigations.go`, preserving retained predecessor history while returning `source_not_found` before work when its source is gone.
- [ ] T047 [US2] Publish only safe post-commit `investigation_update` attempt/status events in `internal/dashboard/broker.go`.
- [ ] T048 [US2] Add retry/detail/history and safe `investigation_update` SSE definitions in `internal/dashboard/openapi.yaml`.

**Checkpoint**: US2 independently proves immutable provenance, retry lineage, interruption recovery, queue rejection versus rate delay, independent retention, and safe failure observability without automatic replay.

---

## Phase 5: User Story 3 — Identify a deterministic sudden session-count drop (Priority: P2)

**Goal**: The central service detects and persists only deterministic lower-tail session-count drops, distinguishes drains and unknown gaps, and exposes a distinct authorized source/API/SSE surface without changing evtspike or notifications.

**Independent Test**: Feed synthetic distinct reports through both accepted-report paths for qualifying unexplained and all gap/drain/host-isolation cases; only unexplained is provider eligible and existing evtspike contracts stay unchanged.

### Tests for User Story 3 (write first)

- [ ] T049 [P] [US3] Add lower-tail model tests in `internal/sessiondrop/baseline_test.go` for per-host 96 source-local quarter-hour slots across offsets/DST, seven-local-date maturity, all-hours 20-observation/24-hour readiness, decay, stable lower CDF, and no cross-host pooling.
- [ ] T050 [P] [US3] Add detector transition tests in `internal/sessiondrop/detector_test.go` for valid zero; nil/invalid/missing/stale/duplicate/out-of-order gaps; exact `duplicate_report_epoch`; exactly-once scoring; minimum loss; 2-of-3 confirmation; cooldown; candidate/confirmed anti-poisoning; drain/post-horizon precedence; unknown context; local-date boundaries; and restart replay state.
- [ ] T051 [US3] Add accepted-report handoff/compatibility integration tests in `internal/dashboard/store_test.go` for HTTP/local common hook, one observation, distinct session-drop persistence, post-commit unexplained automatic-root handoff when automatic mode is enabled, source dedupe, zero work while disabled or for ineligible classifications, unchanged `CheckResult`/`SpikePayload`/evtspike notification behavior, and no session-drop notification.
- [ ] T052 [US3] Add authorized session-drop list/detail/SSE tests in `internal/dashboard/handlers_sessiondrop_test.go` and `internal/dashboard/broker_test.go` for authorization before host resolution, opaque host-free SSE, persisted `unexplained`/`drain_associated`/`unknown_context` classifications, drain context only, pagination, and old-client-safe additive event handling.

### Implementation for User Story 3

- [ ] T053 [US3] Implement decayed per-host source-local slot/all-hours baseline selection, local-date maturity, stable lower-tail scoring, and normal-only training in `internal/sessiondrop/baseline.go`.
- [ ] T054 [US3] Implement report validation/order watermark, gap handling, confirmation, exact duplicate epoch and drain/post-horizon classifications, cooldown, anomaly persistence, and provider eligibility in `internal/sessiondrop/detector.go`.
- [ ] T055 [US3] Extend the common accepted-report hook and lifecycle wiring in `internal/dashboard/store.go` and `internal/dashboard/subsystem_windows.go` for exactly-once observations, durable post-commit source publication, and investigation-owned automatic-root handoff only for deduped eligible unexplained sources while automatic mode is enabled.
- [ ] T056 [US3] Implement authenticated session-drop list/detail handlers in `internal/dashboard/handlers_sessiondrop.go`, resolving a registered host from deterministic source rows only after authorization.
- [ ] T057 [US3] Add host-free additive `session_drop` events in `internal/dashboard/broker.go`.
- [ ] T058 [US3] Document session-drop REST/list/detail and SSE schemas, persisted classification/drain-context-only projection, automatic-root eligibility, and unchanged evtspike compatibility in `internal/dashboard/openapi.yaml`.

**Checkpoint**: US3 independently delivers deterministic session-drop sources and authorized visibility; only eligible unexplained sources hand off post-commit to the investigation owner when automatic mode is enabled, and it never changes evtspike semantics or sends a notification.

---

## Phase 6: User Story 4 — Configure and diagnose a safe optional provider integration (Priority: P2)

**Goal**: Dashboard-group operators can configure acknowledged provider access through session-only settings, retain a write-only DPAPI credential, observe safe operating state, and see disablement/degradation without affecting deterministic operations.

**Independent Test**: Start default-disabled; perform preserve/replace/clear with synthetic credentials; enable acknowledged access while automatic stays off; induce invalid configuration and provider failure; confirm only safe session routes expose state and queued work cancels before send when disabled.

### Tests for User Story 4 (write first)

- [ ] T059 [US4] Add dedicated settings/status handler contract tests in `internal/dashboard/handlers_investigations_test.go` for safe fields only, rejection of an omitted credential command and writes to fixed detector constants, DPAPI preserve/replace/clear, acknowledgement/credential/automatic validation, fixed detector metadata, invalid bounds, and access-disable queue cancellation.
- [ ] T060 [P] [US4] Add configuration-projection regression tests in `internal/dashboard/client_test.go` for provider/session-drop field absence from machine `GET /api/v1/config` and unchanged shared `GET`/`PUT /api/v1/settings`.
- [ ] T061 [US4] Add provider status/diagnostic integration tests in `internal/investigation/worker_test.go` for disabled/configured/ready/automatic-enabled/degraded/failing states, safe counts/latest reason, reload validation, and continued detector/drain/freshness/dashboard operation under failures.
- [ ] T062 [US4] Add safe post-persistence settings/status SSE tests in `internal/dashboard/broker_test.go` with no credential, provider artifact, canonical host, or raw diagnostic disclosure.

### Implementation for User Story 4

- [ ] T063 [US4] Implement dedicated session-only settings/status reads and atomic scoped updates in `internal/dashboard/handlers_investigations.go`, requiring an explicit credential command, enforcing acknowledgement and write-only DPAPI operations, rejecting writes to fixed profile/endpoint/detector constants, allowing configurable detector bounds only, and authorizing before lookup.
- [ ] T064 [US4] Implement derived safe provider state/count/latest-reason calculation and structured non-secret diagnostics in `internal/investigation/worker.go`.
- [ ] T065 [US4] Wire current-config reload/invalidation to cancel unsent attempts while preserving terminal history in `internal/dashboard/subsystem_windows.go` and both re-enable paths in `internal/svc/service_loop.go`.
- [ ] T066 [US4] Extend safe settings/status post-commit publication in `internal/dashboard/broker.go` and route registration in `internal/dashboard/server.go` without adding provider authority to machine routes.
- [ ] T067 [US4] Document dedicated settings/status routes, errors, operating states, explicit credential commands, fixed-constant rejection, and no-machine-route rule in `internal/dashboard/openapi.yaml`.

**Checkpoint**: US4 independently proves opt-in session-only write-only provider administration and visible safe degradation while default-disabled deterministic detection continues.

---

## Phase 7: Polish & Cross-Cutting Verification, Documentation, and Release Readiness

**Purpose**: Integrate the authorized dashboard experience, document operational boundaries, and prove focused/build, injected backend, real-browser provider fixture, migration/rollback, and installed zero-network Windows smoke surfaces independently.

- [ ] T068 Add safe investigation/session-drop fetch wrappers and request DTO validation in `frontend/src/lib/api.js` for dedicated session routes only, including manual root creation only for eligible sources, retries only for failed/insufficient terminal attempts, and safe route-error handling.
- [ ] T069 Add bounded in-memory investigation/session-drop state and host-free reducers that ignore unknown additive events in `frontend/src/lib/state.svelte.js`, including live attempt/status updates for create and retry.
- [ ] T070 [P] Add `frontend/src/components/InvestigationPanel.svelte` rendering immutable host-free attempt history, hypotheses, omissions, insufficiency/human-review states, allowed manual create only for eligible sources, failed/insufficient retry only for eligible terminal attempts, safe route errors, and no remediation controls.
- [ ] T071 [P] Add `frontend/src/components/SessionDropPanel.svelte` rendering authorized deterministic source facts, persisted `unexplained`/`drain_associated`/`unknown_context` classification, eligibility, drain context only, and linked host-free attempts; do not render gap history.
- [ ] T072 Update `frontend/src/App.svelte` to dispatch actual investigation/session-drop SSE messages through the host-free state reducers while ignoring unknown additive event types.
- [ ] T073 Update `frontend/src/components/ServerDetail.svelte` to distinguish authorized deterministic host facts from host-free investigation history and expose manual creation only for eligible sources plus failed/insufficient retry with state gating.
- [ ] T074 [P] Update `frontend/src/components/ConfigModal.svelte` with acknowledgement copy, separate access/automatic controls, explicit write-only preserve/replace/clear credential commands, validated session-drop settings, fixed detector metadata, and safe provider status.
- [ ] T075 [P] Update `README.md` with opt-in setup, third-party/privacy acknowledgement, DPAPI write-only credential behavior, fixed provider boundary, no-remediation rule, default-disabled behavior, and session-drop scope.
- [ ] T076 [P] Update `docs/guide.html` with dashboard configuration/status operation, failure interpretation, immutable history/retry, authorization identity boundary, retention, and no-real-key validation guidance.
- [ ] T077 [P] Update `CHRONICLE.md` with v4 additive migration/rollback, independently anchored retention, provider-disable recovery, permanent-host cleanup, and session-drop compatibility constraints.
- [ ] T078 Run focused Go tests named in `config_test.go`, `internal/investigation/*_test.go`, `internal/sessiondrop/*_test.go`, `internal/telemetry/{schema_test.go,investigation_attempts_test.go,session_drops_test.go,retention_test.go,exclusions_test.go,event_spikes_test.go}`, `internal/dashboard/{store_test.go,interfaces_test.go,handlers_investigations_test.go,handlers_sessiondrop_test.go,broker_test.go,client_test.go,subsystem_windows_test.go}`, and `internal/svc/service_loop_test.go`; resolve all failures.
- [ ] T079 Run the production frontend build from `frontend/` for `frontend/src/lib/api.js`, `frontend/src/lib/state.svelte.js`, `frontend/src/App.svelte`, `frontend/src/components/InvestigationPanel.svelte`, `frontend/src/components/SessionDropPanel.svelte`, `frontend/src/components/ServerDetail.svelte`, and `frontend/src/components/ConfigModal.svelte`; resolve all failures.
- [ ] T080 Execute the production-frontend real-browser provider-fixture scenario in `specs/013-ai-anomaly-investigation/quickstart.md` using an in-process dashboard harness with an injected fixed-URL `RoundTripper`; exercise manual create, live attempt SSE, completed hypothesis, provider and local insufficiency, human-review suppression, failure, and failed/insufficient retry; retain only redacted fixture evidence.
- [ ] T081 Execute the injected-transport backend provider integration scenario in `specs/013-ai-anomaly-investigation/quickstart.md`, distinct from the browser fixture; retain only redacted boundary evidence.
- [ ] T082 Execute the migration/rollback fixture scenario in `specs/013-ai-anomaly-investigation/quickstart.md`; retain only redacted fixture evidence.
- [ ] T083 Execute the actual installed Windows service/dashboard zero-network smoke in `specs/013-ai-anomaly-investigation/quickstart.md`; use no real key, network, or injected transport and verify default-disabled UI behavior in a real browser.
- [ ] T084 Run `go test ./...` from the repository root as the final Go gate required by `specs/013-ai-anomaly-investigation/quickstart.md`; resolve every failure.
- [ ] T085 Run `just lint` from the repository root as required by `specs/013-ai-anomaly-investigation/quickstart.md`; resolve every warning or failure.
- [ ] T086 Run repository-configured pre-commit checks from the repository root as required by `specs/013-ai-anomaly-investigation/quickstart.md`, without bypassing hooks; resolve every failure.

---

## Dependencies & Execution Order

### Phase dependencies

1. **Phase 1** has no dependencies.
2. **Phase 2** depends on Phase 1 and blocks all story implementation. Complete T005–T028 before T029 or later.
3. **US1** depends on foundational config/store/types/wiring. T029–T032 precede T033–T039.
4. **US2** depends on foundational attempt storage and the US1 lifecycle. T040–T043 precede T044–T048.
5. **US3** detector work depends on foundational session-drop storage/types and T049–T050 may proceed alongside US1 after Phase 2; its accepted-report automatic-root integration (T051–T058) depends on the US1 investigation worker, source eligibility, and dashboard handoff in T035–T038.
6. **US4** depends on US1's investigation worker, authorized handlers, and routes (T036–T038), in addition to foundational configuration/types. T059–T062 precede T063–T067.
7. **Phase 7** depends on US1–US4. T068–T077 precede focused/build verification T078–T079; the four distinct scenarios T080–T083 precede final gates T084–T086.

### User-story dependency graph

```text
Phase 1 → Phase 2 → US1 (MVP) → US2
                    ├──────────→ US3 accepted-report automatic handoff
                    └──────────→ US4
Phase 2 ────────────→ US3 detector core
US1 + US2 + US3 + US4 → dashboard/docs/scenarios → final gates
```

### Parallel opportunities

- After T001–T004, foundational tests T005–T014 are parallel-safe; their implementations begin only after the relevant tests exist. T018 must land before T019–T021.
- T019–T023 may proceed in parallel after T018; T024–T028 serialize the shared accepted-report, interface, lifecycle, and server wiring path.
- Within US1, T029–T032 are parallel-safe. T033 and T034 can proceed together; T035–T039 follow their interfaces, with `server.go` and `openapi.yaml` serialized.
- Within US3, T049–T050 are parallel-safe after Phase 2. T051–T052 require the US1 automatic-root interfaces; T053 and T054 form the detector sequence, then T055–T058 follow durable source projections and serialize `store.go`, `subsystem_windows.go`, `broker.go`, and `openapi.yaml`.
- Within US4, T059–T062 begin only after US1's worker/handler/route contracts exist. T063–T067 serialize shared handlers, worker, lifecycle, broker, server, and OpenAPI surfaces.
- T070–T071 and T074–T077 are parallel-safe after REST/SSE contracts. `api.js`, `state.svelte.js`, `App.svelte`, and `ServerDetail.svelte` are serialized frontend integration hotspots; focused/build proof is T078–T079, and the browser fixture is separately proven by T080.

### Parallel example: US1

```text
Task: T029 — internal/investigation/evidence_test.go
Task: T030 — internal/investigation/provider_test.go
Task: T031 — internal/investigation/worker_test.go
Task: T032 — internal/dashboard/handlers_investigations_test.go
```

---

## Implementation Strategy

### MVP first (US1)

1. Complete Setup and the test-first Foundational phase, including v4 schema/config/store ownership and both accepted-report/service-loop integration paths.
2. Complete US1 tests, allowlist/provider/worker behavior, and the authorized attempt REST contract.
3. Validate US1 through the injected transport and synthetic canaries in `quickstart.md`; it must make no real provider transmission.

### Incremental delivery

1. **Foundation**: safe config, durable v4 records, independent retention, permanent-host cleanup, central report handoff, and central-only ownership.
2. **US1**: bounded manual investigation of a durable confirmed event spike.
3. **US2**: durable review, strict failure handling, restart recovery, and explicit retry.
4. **US3**: independent deterministic lower-tail session-drop detection and authorized source visibility.
5. **US4**: safe provider administration, operational diagnosis, reload/disablement handling.
6. **Cross-cutting**: dashboard rendering and actual SSE dispatch, documentation, distinct provider/migration/Windows smoke scenarios, then final quality gates.
