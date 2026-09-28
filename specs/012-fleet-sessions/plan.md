# Implementation Plan: Fleet Sessions (012)

**Branch**: `012-fleet-sessions` | **Date**: 2026-09-26 | **Spec**: [spec.md](./spec.md)
**Input**: Fleet Sessions specification and attached dashboard mockup.

## Summary

Deliver an administrator-only current-state Sessions tab for a Windows RDS fleet. Agents collect bounded WTS metadata and live per-session performance, submit a complete machine-authenticated snapshot to a dedicated endpoint, and the dashboard transactionally keeps only the latest host/session rows for 1–168 hours. Administrators can inspect detail, launch a validated local Shadow protocol URI with an accessible command-copy fallback, and queue exactly-once confirmed disconnect/logoff/message commands that agents validate against expected logon time before WTS. No RD Broker dependency, historical session analytics, remote dashboard execution, or consent bypass is introduced.

**Technical approach**: extend the existing report/config/auth/SSE/SQLite patterns rather than the legacy heartbeat payload; schema v4 has six tables: five dashboard telemetry tables, including a durable generation fence, and the agent-local action ledger. The agent locally reserves a UUIDv7 per canonical host before reporting; the dashboard fence prevents delayed old generations from reclaiming a host. The Svelte UI follows `ServerTable.svelte` patterns but gets a separate Sessions state/view because it has different privacy and bounded-detail semantics.

## Technical context

| Area | Decision |
|---|---|
| Runtime | Go Windows service/dashboard; Svelte 5 + Vite dashboard; embedded frontend. |
| Windows target | Supported Windows Server RDSH hosts and dashboard hosts; new Go files use `//go:build windows`. |
| Existing integration points | `sessions.go`, `internal/perfmon`, `internal/dashboard/client.go`, report handlers, telemetry interfaces, `sse.go`, config, SSPI session/auth, `ServerTable.svelte`, `ConfirmDialog.svelte`, `Toast.svelte`. |
| Persistence | Existing telemetry SQLite/WAL, schema v4; `internal/telemetry/` owns all current-only session storage and dashboard handlers consume its interfaces. No Broker DB and no external service. |
| Authentication | Machine account for ingestion/delivery; authenticated dashboard admin for reads/settings/actions; `is_admin` added to existing auth/me projection. |
| Limits | 512 KiB decoded snapshot, 500 sessions/host, 5 processes/session, 0–5 configured top processes, fleet page 15/30/50, host payload max 500 rows, action lifetime 5 min. |
| Test strategy | Focused Go contract/store/handler/action/Windows-adapter tests. The frontend has no test runner configured: add pure helper tests only with Node's built-in test runner; prove interactions by deterministic browser smoke and `pnpm build` unless test infrastructure is deliberately added. |

## Architecture

```mermaid
flowchart LR
  WTS[WTS metadata] --> AG[DrainCtl agent]
  PDH[Named PDH counters] --> AG
  TH[Toolhelp process scan] --> AG
  AG -->|POST session-snapshot v1<br/>machine auth, <=512 KiB| IN[Dashboard ingest]
  IN -->|attempt + last-success transaction| DB[(Telemetry SQLite v4)]
  IN -->|metadata only| SSE[session_snapshot SSE]
  UI[Admin Sessions tab] -->|fleet summary| F[GET /sessions]
  UI -->|expanded current host| D[GET /sessions/{host}]
  F --> DB
  D --> DB
  UI -->|confirmed disconnect/logoff/message| A[POST session action]
  A --> OUT[(session_action_outbox)]
  A --> AUD[(session_action_audit)]
  AG -->|normal report response, ≤20| OUT
  AG --> LED[(agent session_action_ledger)]
  LED -->|claimed before WTS; terminal outcome| WTS
  AG -->|terminal outcome next report, ≤20| OUT
  OUT -->|append lifecycle| AUD
```

### Data lifecycle

1. A configured agent durably reserves a UUIDv7 `agent_instance_id` in its local telemetry DB before it posts: for its canonical host, parsed 16-byte UUIDv7 order must strictly advance `session_generation_fence.max_instance_id`. It posts synchronously from one reporting loop, incrementing `sequence:uint64` for every attempt. It sends sequence and every browser-facing byte count as canonical unsigned decimal strings, plus `logical_cpu_count` 1–1024. It collects WTS session metadata first; only WTS enumeration/metadata failures and collector timeout are fatal and emit an attempt with capability/error status and no replacement rows, never a false empty snapshot.
2. It enriches successful WTS sessions with strict-ID PDH values and optional bounded deterministic top processes. A failed optional PDH/process source leaves null/missing values and capability false, not a fatal error; per-process CPU is null for a first or inaccessible delta. Canonical agent processes have numeric PID, nullable CPU, and uint64 bytes. Process order is CPU descending (null last), working set descending, image name case-insensitive ascending, then PID ascending.
3. It projects visibility policy before serializing, validates hard bounds (including CPU <=100×logical CPU count and bytes <= SQLite MaxInt64), and independently POSTs the schema-v1 snapshot. Masked processes retain PID/metrics but have `image_name:"***"`; hidden produces `processes:[]`. Existing report cadence remains alive regardless of result.
4. The dashboard authenticates the machine, limits/decode-validates the body, canonicalizes host, and compares parsed UUIDv7 16-byte values in one transaction. For the same instance only a strictly newer sequence is accepted. A different UUIDv7 starts a new generation only when it is strictly greater than that host's durable `session_generation_fence`; the transaction advances the fence before accepting its reset/lower sequence. Lower/equal different UUIDv7s are stale, including delayed old snapshots after the newer generation commits. UUID strings are never locale-compared; `observed_at_ms` is diagnostic only.
5. For a successful complete attempt, that transaction stores the same instance/sequence, observation/receipt as last success; atomically persists `session_count`, active+connected, idle, disconnected, distinct non-null projected user/domain count (zero for hidden identity), and max last-input/connect/logon activity; replaces `session_latest`; and emits metadata-only SSE. For a fatal collection-error attempt, it updates only latest-attempt identity/receipt/status/error and preserves all last-success rows/counts/freshness.
6. Host detail is fetched only after explicit expansion. Freshness is unknown without last success; otherwise offline if host registry status is off or age exceeds 10×heartbeat, stale if over 3×heartbeat, fresh otherwise. Latest fatal error is separately `collection_status:error`; display precedence is offline, error, stale, fresh/unknown. Snapshot retention deletes last-success rows at its configured cutoff but never deletes the generation fence. Terminal outbox/audit/ledger retention is independently action-relative.
7. Confirmed actions write an idempotent outbox record and append audit row. The next report response offers at most 20 non-expired queued or delivered commands; the same command ID remains eligible for redelivery until terminal/expiry. The local ledger durably inserts `claimed` before WTS and never re-executes a claimed ID after restart, returning a safe failed/duplicate outcome; it retains claimed/terminal/outcome/times through dashboard acknowledgement or expiry plus its own retention. Message text is protected, never emitted in event/audit/log response data, and deleted terminally.

## Wire contracts

### `POST /api/v1/session-snapshot`

**Auth**: `requireMachineAccount`; host principal authorization follows existing report registration rules.
**Limit**: 512 KiB decoded request body; reject trailing JSON.
**Content type**: `application/json`.

```json
{
  "schema":"drainctl.session-snapshot.v1",
  "host":"rdsh-01.example.com",
  "agent_instance_id":"f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "sequence":"42",
  "observed_at_ms":1780000000000,
  "collector_version":"1.8.0",
  "logical_cpu_count":8,
  "capabilities":{"session_actions":true,"processes":true,"input_delay":true,"remotefx":false},
  "collection_error":null,
  "sessions":[{"session_id":12,"state":"active","cpu_percent":4.2,"working_set_bytes":"104857600","processes":[{"pid":100,"image_name":"app.exe","cpu_percent":null,"working_set_bytes":"52428800"}]}]
}
```

Exact fields, finite numeric ranges, canonical decimal strings, nullable metrics, and bounded string constants are defined by [agent-session-snapshot.md](./contracts/agent-session-snapshot.md). Numeric/session-state validation occurs before persistence; `collector_version` is UTF-8 1–64 bytes with no controls; the accepted states include `unknown`, which sorts/renders after known states. Valid successful empty `sessions: []` is the only authoritative clear; fatal collection-error attempts contain no replacement rows. Fatal codes are WTS enumeration/metadata failure and collector timeout only.

**Responses**: `202 {"accepted":true}` for an accepted/replaced attempt and `202 {"accepted":false}` for stale/idempotent; `400` malformed/bounds/schema, `401`/`403` auth, `413` body limit, `503` bounded storage failure. Every error body is code-only/safe and does not echo payload PII.

### Admin REST

| Endpoint | Contract |
|---|---|
| `GET /api/v1/me` | Adds `is_admin:boolean`; existing fields remain compatible. |
| `GET /api/v1/sessions?q=&state=&sort=&dir=&page=&page_size=` | Admin-only fleet summary page. Validate all query values; maximum page size 50; no PII/client/process fields. |
| `GET /api/v1/sessions/{host}?q=&page=` | Admin-only latest one-host payload, <=500 current rows, policy-projected values, explicit snapshot/capability/status/error/freshness. |
| `POST /api/v1/sessions/{host}/{uint32}/actions` | Always-mounted admin route. JSON has exactly `type`, `expected_logon_at_ms`, and optional `message`; unknown keys reject. Disconnect/logoff messages must be absent/empty; message requires nonempty bounded text. `202` queued; `200` replay; disabled sessions/actions is `409 sessions_disabled`. |
| `GET /api/v1/session-actions/{action_id}` | Admin-only privacy-safe action status for reload recovery and two-second polling. |

The existing settings GET may expose nonsecret `SessionsConfig` to authenticated users; mutation of `allow_actions`, privacy, or any Sessions setting is dashboard-admin-only. All session GETs are `Cache-Control: no-store`. The action route does not accept host/session identifiers only in JSON, avoiding a path/body disagreement. Authorization wraps routes before any database lookup, preventing non-admin existence disclosure.

### Snapshot detail fields and string bounds

Implement constants and validation once; server revalidates all agent claims. Maximum UTF-8 lengths: host 253 bytes RFC 1123, user/domain/station/client 256 bytes each, image/app 260 bytes, `error_code` 64 ASCII token, process PID `uint32`, session ID `uint32`, action message 256 Unicode scalar values. Neither agent nor server logs a rejected raw field.

### SSE

`session_snapshot` is metadata-only and uses the current broker's all-authenticated broadcast. It contains canonical host; latest-attempt and last-success instance IDs/sequences (decimal strings), observation/receipt timestamps; freshness; persisted aggregate summary; capability/status; and bounded error code. `session_action` contains only action ID/status/result/timestamps. Neither event contains username/domain, station, client, process, action message, or raw error text. Non-admin subscribers ignore events and cannot call admin-only Sessions APIs. The frontend records action status in `sessionStorage`, polls the status endpoint every two seconds while nonterminal, stops on navigation/terminal, recovers after reload, and maps terminal state to a toast. An expanded admin host view refetches detail.

## SQLite schema v4


The telemetry migration runner creates five dashboard telemetry-owned tables—`session_generation_fence`, `session_snapshots`, `session_latest`, `session_action_outbox`, and `session_action_audit`—and the agent creates the sixth local v4 `session_action_ledger` table. The agent-local telemetry DB also uses `session_generation_fence` to durably reserve its UUIDv7 before reporting. The dashboard fence is never removed by snapshot privacy or retention purges. The exact canonical DDL, indexes, constraints, parsed-byte UUIDv7 ordering, replacement ordering, and retention behavior are defined only in [data-model.md](./data-model.md); this plan intentionally does not duplicate illustrative schema. The service composition root constructs telemetry stores, injects their interfaces into `dashboard.NewSubsystem`, and starts/stops/retains them safely; dashboard handlers consume the interfaces and never own storage.


## Frontend architecture and mockup mapping

| Mockup surface | Implementation | Testable behavior |
|---|---|---|
| Primary Sessions navigation | `Nav.svelte`, `App.svelte`, auth state | `is_admin` gates tab visibility and route; keyboard tab semantics include Sessions. |
| Sticky header/status counts/footer | existing Nav/Footer/app shell styles | Sessions retains dashboard chrome and responsive focus order. |
| Search and All/Active/Disconnected/Idle pills | `SessionsTable.svelte` local UI state + summary endpoint query | URL/state/query preserve selected sort/filter/page; selected pill has `aria-pressed`. |
| Host columns: Host, Status, Mode, Sessions, Active, Idle, Disc., Users, Last Activity | `SessionsTable.svelte` | table caption, scoped headers, `aria-sort`, deterministic rows. |
| Expandable host panel plus TOTAL/ACTIVE/IDLE/DISCONNECTED | `SessionHostDetail.svelte` | Enter/Space toggles; detail REST only after expand; explicit loading/error/empty/stale/capability state. |
| Per-host search and ID/USER/STATE/CLIENT/LOGON/IDLE/CPU/MEM/APP/PROCESS/Shadow | `SessionHostDetail.svelte` | bounded current payload, visibility modes, null marker; no fleet PII fetch. |
| Log Off / Send Message dialogs | existing `ConfirmDialog.svelte` extended or session-specific dialog | values named in confirmation; focus trap/restore; only confirm POSTs. |
| Shadow | Endpoint-owned local protocol launch plus Clipboard API fallback | The UI requests and exactly validates `drainctl-shadow://shadow?host=<host>&session=<id>` before browser navigation; adjacent Copy preserves `mstsc.exe /v:<host> /shadow:<id> /control`. The installed helper strictly validates the URI before fixed-argument mstsc launch; browser/Windows and mstsc consent remain. |
| Offline/grace/error state and mobile scroll hint | component styles + existing tokens | persistent state labels; horizontal inner table scroll/no body overflow; accessible hint. |

State belongs in new `sessions.svelte.js` or an explicitly named slice of `state.svelte.js`: summary page/query/loading/error, per-host detail cache keyed by canonical host plus latest instance/sequence, action-status `sessionStorage`/poll lifecycle, and in-flight abort controllers. It does not join session details into `appState.servers`, localStorage, or historical buffers. A session SSE event updates/invalidates only the relevant summary/detail cache; an expanded host refetch uses `AbortController` and ignores only an equal/older response from the same instance.

## Project/file map

| File/area | Change |
|---|---|
| `config.go`, config tests, remote settings view/update plumbing | Add validated `SessionsConfig`, defaults, remote collector projection, and no-secret admin settings projection. |
| `sessions.go` and new focused Windows collector files | Keep existing aggregate behavior; add WTS detail/time/client queries and collection types without duplicate enumeration. |
| `internal/perfmon/` | Add strict named-instance session metric adapter; reuse PDH lifecycle, null on unavailable. |
| New Windows process collector file near performance/session collector | Toolhelp/ProcessIdToSessionId/query-handle deltas/fixed heap; no retained handles. |
| `internal/dashboard/client.go` | Durably reserve UUIDv7 `agent_instance_id` per canonical host before snapshot sending, then parse report-response actions; preserve current report API. |
| `internal/dashboard/server.go`, report handlers, telemetry interfaces | Register independent snapshot endpoint; add transactionally fenced UUIDv7 snapshot apply, action response delivery/completion/status handling and SSE wiring without dashboard-owned session storage. At service composition root create stores, inject interfaces into `dashboard.NewSubsystem`, and start/stop/retain safely. |
| `internal/dashboard/auth_*`, `session.go`, login handler | Carry `is_admin` through auth/session/me and admin middleware. |
| `internal/dashboard/handlers_sessions.go` + focused tests | Summary/detail/action/status REST validation, UUIDv7-only snapshot validation, fenced apply behavior, projection, cache headers, safe errors. |
| `internal/dashboard/sse.go`, `broker.go` | Add metadata-only snapshot/action event types. |
| `internal/telemetry/` migration/store files + tests | Own the five dashboard schema-v4 tables, including `session_generation_fence`, and transactional replacement/aggregate/query/retention/outbox/audit operations exposed through dashboard-facing interfaces. |
| Agent session store/action files + tests | Own local UUIDv7 reservation through `session_generation_fence` plus the sixth local `session_action_ledger`, durable claimed-before-WTS/terminal outcomes, restart-safe duplicate suppression, acknowledgement/expiry-relative cleanup. |
| `frontend/src/lib/api.js`, `auth.svelte.js`, `state.svelte.js` | Types/fetchers/auth gate/session cache/action-status persistence and polling. |
| `frontend/src/App.svelte`, `components/Nav.svelte` | Admin gated view and refresh/SSE integration. |
| New `components/SessionsTable.svelte`, `SessionHostDetail.svelte`, action dialog/helper | Mockup layout and interaction using existing brutalist styles/components. |
| `frontend/src/app.css` | Scoped table/detail/responsive/accessibility styles only; reuse existing tokens. |
| `internal/dashboard/openapi.yaml` | Document all API/error/SSE-adjacent API semantics consistent with implementation. |
| `specs/012-fleet-sessions/*` | This implementation-driving specification set; tasks/quickstart owned separately. |

## Delivery phases and dependencies

1. **Contract foundation (dependency: none)**
   - Define shared snapshot/action data types, constants, enum validation, canonical host helper reuse, privacy projector, and `SessionsConfig` validation/defaults.
   - Extend auth/me with `is_admin` and implement server-side admin middleware before mounting any session route.
   - Add contract tests for bounds/visibility/auth. No UI assumes unimplemented routes.

2. **Current-state storage (depends on 1)**
   - Add telemetry-owned schema v4, its five dashboard tables including the durable generation fence, and dashboard-facing interfaces/migration; add agent-local UUIDv7 reservation/fence and sixth ledger table with separate lifecycle.
   - Implement UUIDv7-fenced instance/sequence transactional writes: same-instance sequence ordering; only a greater parsed UUIDv7 advances a generation; lower/equal different instances are stale. Successful complete replacement persists final aggregate semantics and last success/rows; fatal collection error updates only latest attempt. Add summary/detail readers, required indexes, deterministic queries, and resumable independent snapshot/action retention that preserves fences.
   - Prove empty clear, fatal-error preservation of last-good rows/counts/freshness, same-instance old/equal idempotence, delayed old-generation rejection after a greater new-generation commit, fence survival through privacy/retention purge, `unknown` state ordering, decimal-string sequence/bytes, CPU/logical-count bounds, nullable process CPU/order, process masking, and privacy deletion.

3. **Agent collection and transport (depends on 1; storage tests can run independently)**
   - Implement WTS detail collection, PDH strict matching, logical CPU count, process fixed-heap accounting, visibility projection, snapshot validation/send, and config refresh.
   - Add machine snapshot ingest handler after phase 2 store is ready. Confirm legacy report stays ≤64 KiB and operational during endpoint absence.

4. **Dashboard queries and live updates (depends on 2 and 3)**
   - Implement fleet/detail handlers, no-store/error shaping, snapshot/action SSE, action status endpoint/poll/reload recovery, and handler/store/load tests.
   - Ensure dashboard has an explicit unavailable/stale/unsupported projection; do not collapse collection failure to empty.

5. **Actions (depends on 1, 2, 3)**
   - Add durable enqueue/delivery/complete/expiry flow, protected message handling, append-only audit records, and normalized request fingerprint idempotency scoped to principal/endpoint/key.
   - Deliver/report at most 20 per response. Keep delivered commands eligible for same-ID redelivery until terminal/expiry; add claimed-before-WTS local ledger that never re-executes after restart and has acknowledgement/expiry-relative cleanup.
   - Keep admin action routes mounted even when disabled; strict action body validation and API/dialog behavior ship only with admin/config/freshness/capability gates complete.

6. **Sessions UI and accessibility (depends on 1 and 4; action controls additionally depend on 5)**
   - Add nav/routing/API/state/table/detail/cache invalidation/action-status sessionStorage and poll lifecycle, then mockup controls and responsive styling.
   - Use existing confirm/toast/table patterns; validate keyboard and ARIA after DOM structure settles. Test pure helpers with Node built-in only; prove interactions through the deterministic browser smoke and production build.

7. **Integrated operational proof and release documentation (depends on 2–6)**
   - Run focused contracts/store/handler/Windows tests, Node pure-helper tests where added, a deterministic browser smoke, production frontend build, and isolated Windows smoke collection/action tests.
   - Verify rolling upgrade/downgrade behavior, privacy inspection, size bound, SSE payload inspection, fatal-error last-good preservation, stale/offline display, and docs/OpenAPI/config migration.

## Failure containment and compatibility

- **New agent → old dashboard**: `POST /session-snapshot` 404/405/413 is logged as a snapshot capability failure and never interrupts heartbeat; old dashboard ignores optional report response command fields.
- **Old agent → new dashboard**: it has no snapshot; fleet summary displays unsupported/no data, not an empty live host. Existing host pages remain unchanged.
- **Dashboard restart**: the five dashboard telemetry tables preserve last-good detail, latest-attempt error status, generation fences, and idempotency. The sixth agent-local ledger preserves claimed/terminal actions and prevents re-executing a claimed command; the local fence preserves UUIDv7 reservation. Action expiry/retention is evaluated from persisted action timestamps independently of snapshot retention; delivered commands may be redelivered by ID until terminal/expiry.
- **Rollback**: v4 tables are additive. An older binary ignores dashboard session tables and agent ledger and continues existing report behavior. Rollback does not restore/emit private session data elsewhere; migration must not alter existing telemetry/host tables.
- **Delayed old generation**: after a greater UUIDv7 generation commits, any different lower/equal UUIDv7 is acknowledged stale without changing the fence, last-success rows, latest attempt, aggregates, or SSE state. This remains true after dashboard restart, snapshot retention cleanup, or privacy purge.
- **Fatal WTS collection error**: persist the newer latest-attempt status/error but retain nullable last-success timestamps, final aggregates, and `session_latest` rows; render collection error while applying freshness from last success and display precedence offline/error/stale/fresh-or-unknown. Only a newer successful complete empty snapshot clears rows.
- **Partial WTS/PDH/process collection**: valid WTS state survives optional data failure; null metrics/capability false explain limited detail without a fatal attempt.
- **Storage failure**: don't clear prior snapshot or accept action as queued. Snapshot returns 503; rely on next report/retry.

## Verification matrix

| Layer | Required proof |
|---|---|
| Data/wire | Table-driven valid/invalid body sizes, schema, UUIDv7-only identity, UTF-8/string/counts, canonical decimal sequence/byte strings, collector-version controls, logical CPU and CPU bounds, SQLite byte ceiling, uint32 IDs, known and `unknown` states, same-instance sequence monotonicity, parsed-byte UUIDv7 fenced generations, delayed old-generation rejection after a newer commit, fatal-error preservation, nullable process CPU/order/masking, and successful empty replacement. |
| Store | Commit atomicity; persisted final aggregates; exact unknown/offline/stale/fresh thresholds and display precedence; fence retention through snapshot retention/privacy purge; independent snapshot/action retention; immediate privacy-policy purge/cascading detail removal/action cancellation; outbox same-request replay/changed-request conflict/redelivery/expiry/terminal transitions; and audit redaction. |
| Auth/API | 401/403 distinction, always-mounted action route/`sessions_disabled` 409, strict action body, machine mismatch, no-store headers, `202 accepted:true|false`, at-most-20 delivery/completion, action status endpoint, and no PII in summary/SSE/errors. |
| Windows | WTS selection/times, strict PDH instance ID matcher, optional metric null/capability false, Toolhelp mapping/handle close, logical-CPU normalization, fixed heap nullable-CPU tie-break, expected-logon WTS action guard, and durable claimed-ledger restart suppression. |
| UI | Node built-in tests only for pure helpers; fully enumerated deterministic browser-driver smoke plus production build for every mockup control, server page/query changes, expanded refetch/out-of-order discard, freshness/error precedence, action sessionStorage/poll/reload/terminal toast, confirmation, copy command, keyboard/ARIA/narrow scroll. |
| Smoke | Isolated Windows host sends a bounded decimal-string snapshot; dashboard shows persisted aggregates; fatal collection error retains last-good detail while surfacing status; successful update clears to empty; disabled data/action route is explicit; synthetic/review test proves command outcome/restart suppression and no server-side shadow launch. |

## Complexity decisions

| Necessary complexity | Reason | Rejected simpler option |
|---|---|---|
| Complete replacement transaction | Prevents a mixed-time host detail after failed or partial transport. | Per-row upsert leaves ghost sessions and cannot express valid empty clear. |
| Wide rows + JSON process list | Current-only query shape and bounded process list are fast/private. | EAV adds joins/volume; history table violates scope. |
| Durable action outbox/idempotency | At-most-once session side effects must survive dashboard/agent/browser retry. | In-memory queue loses/replays actions after restart. |
| Logon timestamp precondition | Windows reuses numeric session IDs. | Session ID alone can target another user. |
| Metadata-only SSE + detail refetch | Keeps PII/process data out of broad subscriptions. | Push full detail leaks private data and duplicates cache authority. |
