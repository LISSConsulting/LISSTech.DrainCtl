# Tasks: Fleet-wide Sessions Tab (012)

**Input**: `spec.md`, `plan.md`, `data-model.md`, `contracts/`, and `research.md` in this directory.

**Legend**: `[P]` can proceed in parallel once its dependencies are met. Every acceptance condition is executable evidence, not an implementation assertion. “Windows” means a Windows Session Host test, not a syscall mock.

## Phase 0 — baseline and seams

- [ ] T001 Capture `C:/Users/mwisniowski/.omp/sessions-preexisting-20260926.patch` before session work and verify its SHA-256 is `452f860fa213d9157cc51038c82b7a0d41a09de2cfa51f960eae686766ac4de5`. **Acceptance:** final review compares the feature diff with this patch hunk-by-hunk; intentional overlaps are identified and all unrelated baseline hunks survive byte-for-byte.
- [ ] T002 [P] Add deterministic telemetry/dashboard fixtures for successful, empty, fatal-WTS, stale, offline, and invalid snapshots. **Acceptance:** fixtures freeze receipt time and assert database state, response status/content type, and SSE payloads.
- [ ] T003 [P] Add Windows seams for WTS, PDH, Toolhelp, process handles/creation times, clock, and logical CPU count. **Acceptance:** tests simulate every collector path and prove no OS handle survives an interval.
- [ ] T004 [P] Add agent action-ledger seams for durable database open/restart, WTS execution, completion acknowledgement, clock, and cleanup. **Acceptance:** a test can restart between durable claim and completion without calling WTS twice.

## Phase 1 — contracts, configuration, and wire safety

- [ ] T010 Define canonical session/domain types and validation in `sessions.go` and tests. **Acceptance:** identity is `(canonical_host, uint32 session_id)`; state includes valid final-sort `unknown`; snapshots require UUIDv7 `agent_instance_id`, uint64 sequence, and bounded rows/strings/times/metrics.
- [ ] T011 Define canonical agent process types and JSON conversion. **Acceptance:** agent type uses numeric PID, nullable CPU, uint64 working-set bytes; browser/API/storage projection retains numeric PID/metrics, masks only `image_name` as `***`, and hidden emits an empty process list.
- [ ] T012 Make every browser-facing sequence and byte count a canonical unsigned decimal JSON string: `sequence`, session `working_set_bytes`, and process `working_set_bytes`, including API/SSE equivalents. **Acceptance:** `0`, values above JavaScript safe integer, and `math.MaxUint64` round-trip without precision loss; invalid/non-canonical/non-string forms reject; SQLite byte storage rejects values above `MaxInt64`.
- [ ] T013 Add `logical_cpu_count` to the agent snapshot contract. **Acceptance:** agent sends integer 1–1024, server rejects CPU above `100 * logical_cpu_count`, and successful persistence records the last-success count.
- [ ] T014 Add/validate `collector_version` as UTF-8 1–64 bytes with no control characters. **Acceptance:** invalid UTF-8, empty, controls, and 65-byte values reject before logging/storage.
- [ ] T015 Add `SessionsConfig` defaults/bounds and atomic settings update tests. **Acceptance:** enabled/processes/top-N/retention/actions/visibility defaults and bounds match the spec; authenticated users GET nonsecret config; only admins mutate it; failure changes no active or persisted setting.
- [ ] T016 Extend agent remote settings/config pull. **Acceptance:** collector receives only collector settings and applies changes next cycle; no dashboard role/authorization data crosses to the machine.

## Phase 2 — collector and snapshot transport

- [ ] T020 Implement WTS metadata collection/state mapping and Services/listener exclusion. **Acceptance:** WTS enumeration/metadata failure is fatal; unknown state is `unknown`; fatal error sends no clearing snapshot.
- [ ] T021 [P] Implement strict numeric PDH instance matching and optional counter collection. **Acceptance:** `pdh_unavailable` and process/PDH failures create successful rows with null/unavailable metrics and capability false; they are never fatal and never fabricate zero.
- [ ] T022 [P] Implement Toolhelp process collection, limited-query handles, creation-safe CPU deltas, logical-CPU normalization, and fixed-space top-N. **Acceptance:** exact session attribution; first/inaccessible CPU is null; deterministic CPU/bytes/name/PID ordering; all handles close.
- [ ] T023 Assemble complete successful snapshots and bounded fatal attempts. **Acceptance:** only WTS enumeration/metadata and collector timeout are fatal codes; optional failures do not convert replacement into partial semantics.
- [ ] T024 Generate and durably reserve one strictly increasing UUIDv7 per canonical host at agent service startup, then use its uint64 sequence in the synchronous reporting loop; post snapshots separately from the 64 KiB heartbeat. **Acceptance:** local telemetry `session_generation_fence` reservation commits before the first post, uses parsed UUIDv7 bytes rather than text collation, every post attempt advances sequence, restart resets only sequence after a greater reservation, disabled sessions posts nothing, and old dashboard failure leaves heartbeat healthy.
- [ ] T025 Apply identity/client/process projection before serialization. **Acceptance:** masked data is deterministic; hidden identity is absent and hidden process list is empty; discarded raw data cannot be reconstructed downstream.
- [ ] T026 Implement machine-authenticated `POST /api/v1/session-snapshot` with 512 KiB pre-decode limit and strict JSON/schema/UUIDv7/host binding. **Acceptance:** malformed/encoded/duplicate-key/unknown-key/trailing/over-limit/non-UUIDv7 input has deterministic 4xx and no mutation; valid and stale snapshots both return 202 `{accepted:true|false}`; storage failure returns 503.

- [ ] T030 Add canonical transactional v4 migration in `internal/telemetry/` from supported schemas. **Acceptance:** canonical v4 has six tables: five dashboard telemetry tables (`session_generation_fence`, `session_snapshots`, `session_latest`, `session_action_outbox`, `session_action_audit`) and agent-local `session_action_ledger`; the agent also uses its local fence for startup reservation; migration reruns idempotently.
- [ ] T031 Implement transactional UUIDv7-fenced snapshot attempt/replacement storage. **Acceptance:** same-instance equal/older sequence makes no change; a different instance only starts a generation when its parsed 16-byte UUIDv7 is greater than the durable host fence and then advances it; lower/equal different instances are stale, including a delayed old generation after a newer commit; successful empty replaces rows atomically; write failure rolls back everything.
- [ ] T032 Persist fleet aggregates transactionally with successful replacement. **Acceptance:** `session_count`, active (active+connected), idle, disconnected, distinct non-null projected `(user,domain)` count (0 when identity hidden), and max non-null input/connect/logon activity are exact; fleet query uses its required indexes rather than scanning session JSON.
- [ ] T033 Implement projection-before-storage and privacy change purge. **Acceptance:** raw protected data never reaches SQLite; every visibility change purges snapshots/latest and safely cancels/expires applicable actions while retaining `session_generation_fence`; loosening cannot restore prior data or let an old generation reclaim the host.
- [ ] T034 Implement exact receipt-time freshness and independent retention. **Acceptance:** no success => unknown; host registry off **or** age `>10*heartbeat` => offline; otherwise age `>3*heartbeat` => stale; otherwise fresh. Latest fatal error is separate `collection_status=error`; UI precedence is offline, error, stale, fresh/unknown. Session snapshots purge at snapshot cutoff while `session_generation_fence` survives; terminal outbox/audit and agent ledger survive until their own action-relative acknowledgement/expiry-plus-retention cutoffs.
- [ ] T035 Construct telemetry stores at the service composition root, inject dashboard interfaces into `NewSubsystem`, and start/stop/retain all stores safely with the service loop. **Acceptance:** an integration test creates the service, handles an ingest/action/report cycle, shuts down, restarts, and observes durable state without package globals or dashboard store ownership.

## Phase 4 — read APIs and realtime

- [ ] T040 Add admin identity propagation, `/api/v1/me`, and route wrapper. **Acceptance:** non-admins are 403 before lookup/mutation and UI/navigation is absent; admins retain all sessions routes even when actions are disabled.
- [ ] T041 Implement bounded fleet/detail reads with no-store and SQLite timeout. **Acceptance:** fleet returns persisted aggregate-only summaries with no PII/process data; detail returns <=500 projected current rows; pagination/filter/sort are validated and deterministic; unavailable store is typed, not partial.
- [ ] T042 Add committed metadata-only `session_snapshot` SSE. **Acceptance:** payload uses decimal strings for sequences/bytes, has attempt/last-success summary/capabilities/freshness/error only, emits after newer commit, and contains no PII/client/process/logon/action/message data.
- [ ] T043 Implement frontend snapshot reconciliation. **Acceptance:** SSE cues summary refresh and expanded-detail GET only; instance/sequence ordering prevents stale response overwrite; latest error does not clear retained detail; reconnect/poll reconciles it.
- [ ] T044 Add privacy-safe `session_action` SSE status event and frontend recovery. **Acceptance:** event has action ID, safe lifecycle/outcome/timestamps only; frontend persists pending action tracking in `sessionStorage`, polls `GET /api/v1/session-actions/{id}` every 2 seconds, aborts polling on terminal/navigation, resumes after reload, and maps terminal outcome to an accessible toast.

## Phase 5 — guarded durable actions

- [ ] T050 Implement action validation/routes/status route. **Acceptance:** body permits exactly `type`, `expected_logon_at_ms`, and optional `message` only for message type; unknown keys reject; message type requires nonempty bounded message; logoff may contain only an empty/omitted message; admin routes exist even when disabled and return `409 sessions_disabled`.
- [ ] T051 Implement dashboard outbox, idempotency, protected messages, deterministic delivery, completion and audit. **Acceptance:** principal+endpoint key/fingerprint replay/conflict works; pending expiry is five minutes; at most 20 queued/delivered commands are selected in stable order; delivered commands redeliver until terminal/expiry; audit/SSE/logs never include message content.
- [ ] T052 Implement durable local `session_action_ledger` and Windows execution. **Acceptance:** insert `claimed` transactionally before WTS; a post-claim restart never re-executes and reports safe duplicate/failed completion; terminal completion is durable; cleanup is bounded only after dashboard acknowledgement or expiry plus retention.
- [ ] T053 Enforce exact logon guard, expiry-before-WTS, and strict local Shadow protocol handling. **Acceptance:** ID reuse refuses; no action executes after expiry; the endpoint returns `drainctl-shadow://shadow?host=<RFC1123-host>&session=<numeric-id>` with exact command fallback; the GUI helper rejects every other URI shape and launches mstsc only with fixed arguments, never `/noConsentPrompt`.

## Phase 6 — UI and focused tests

- [ ] T060 Build admin Sessions nav/table/detail with search/filter/sort/page/lazy detail and responsive accessible table. **Acceptance:** no history UI; 500-session host remains bounded; unknown sorts last; unavailable differs from zero; hidden values cannot be recovered from DOM/ARIA/title.
- [ ] T061 Build confirmation/copy/action state UI. **Acceptance:** dialog displays host/numeric ID/logon/action, traps/restores focus, prevents duplicate submission; safe toast states include pending/success/refused/expired/failed without sensitive data.
- [ ] T062 Add focused unit/API/storage/Windows tests. **Acceptance:** covers all contract bounds, UUIDv7-only validation, durable reservation, parsed-byte generation comparison, delayed old-generation rejection after newer commit, fence retention across privacy/retention purge, string safe integers, CPU count envelope, aggregate calculations, exact freshness boundaries, v4/six-table lifecycle, atomicity, retention split, action ledger restart, redelivery, SSE, polling/reload recovery, and privacy absence.

## Phase 7 — one final documentation task and release gates

- [ ] T070 Update OpenAPI, operator docs, chronicle, this quickstart, and requirements checklist once all surfaces are final. **Acceptance:** documents exact bounds/status codes/freshness/actions/privacy/six-table distinction, UUIDv7 reservation/fence semantics, and a deterministic browser-driver procedure; no earlier documentation task duplicates this work.
- [ ] T071 Run targeted Go/Windows tests, Node pure-helper tests, and production frontend build. **Acceptance:** all new focused tests pass.
- [ ] T072 Execute the deterministic mock/SQLite gate and Windows RDS walkthrough in `quickstart.md`. **Acceptance:** record every numbered observation, including baseline hunk comparison, UUIDv7 reservation/fence behavior, delayed old-generation rejection after newer commit, fence survival after privacy/retention purge, aggregate/index query plan, CPU/count validation, ledger restart, exact freshness boundaries, and independent retention.
- [ ] T073 Execute the enumerated browser-driver smoke in `quickstart.md`. **Acceptance:** exact browser actions/observations prove admin/viewer gating, metadata-only SSE, action status SSE plus 2s reload recovery, privacy-safe DOM, detail invalidation, action confirmation/copy, and keyboard flow.
- [ ] T074 Final clean-cutover review. **Acceptance:** compare against T001 patch/hash hunk-by-hunk; no compatibility aliases/EAV/history/duplicate DDL/role-aware SSE/server shadow launch remains, and all intentional baseline overlaps are listed in release evidence.

## Dependency map

- T010–T016 gate T020–T026; T020–T022 run in parallel, T023 follows them, and T024–T026 follow the resulting contract.
- T030–T035 gate all dashboard reads/actions; T032–T034 follow T031; T035 follows all telemetry interfaces.
- T040–T044 require T035; T042 follows T031; T044 requires T050–T052 as well as T040.
- T050–T053 require T035 and T026; T052 follows T051.
- T060–T061 require T040–T044 and T050–T053; T062 follows their implementations.
- T070 occurs only after all implementation surfaces; T071–T074 are sequential release gates, with T073 following the production build in T071.
