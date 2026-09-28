# Research and Decision Record: Fleet Sessions (012)

This record turns the fixed feature contract, dashboard mockup, and existing repository patterns into implementation decisions. It contains no open design placeholders.

## Repository evidence used

| Evidence | Consequence |
|---|---|
| `sessions.go` already uses WTS enumeration, excludes service/listener sessions, and produces aggregate `SessionSummary`. | Reuse its WTS selection rules and avoid a second aggregate collector. Add detail/time/client queries in focused collector code. |
| `cmd/drainctl/sessions_cmd.go` renders only current local state. | The fleet feature is dashboard/agent transport work, not a change to CLI output semantics. |
| `internal/dashboard/client.go` has machine-authenticated dashboard reporting and parses additive pending commands in report responses. | Use a parallel machine-auth snapshot POST and the existing report-response command-delivery pattern; do not extend the 64 KiB heartbeat. |
| `internal/dashboard/server.go`, `sse.go`, and `internal/telemetry/` use durable state and additive SSE. | Put all session persistence and migrations in `internal/telemetry/`, expose dashboard-facing telemetry interfaces, and publish a metadata-only event; do not make browser SSE a private data transport or make session storage dashboard-owned. |
| `ServerTable.svelte` already implements brutalist dense table, search/filter/sort/page, expansion, keyboard handling, stale indication, confirmation, toast, and responsive conventions. | Sessions gets its own components but reuses visual/accessibility mechanisms rather than a second dashboard design language. |
| `Nav.svelte` currently has Overview/Servers/Events tabs and `App.svelte` owns REST/SSE refresh. | Add Sessions to the same primary tablist only for admins, with view routing and update invalidation in the central app shell. |
| Existing auth has SSPI session storage but no admin projection. | Add server-derived `is_admin` through AuthInfo/session/me; frontend only consumes it for visibility. |
| Existing SQLite telemetry is authoritative retained storage with bounded handler timeouts. | Schema v4 creates five dashboard telemetry tables—`session_generation_fence`, `session_snapshots`, `session_latest`, `session_action_outbox`, and `session_action_audit`—while the agent has a sixth local durable `session_action_ledger`; use existing database lifecycle/migration patterns. The service composition root constructs stores, injects interfaces into `dashboard.NewSubsystem`, and starts/stops/retains them safely. |

## Decision 1 — Use a separate snapshot endpoint, not heartbeat expansion

- **Decision**: Agents send `POST /api/v1/session-snapshot` using machine authentication, schema `drainctl.session-snapshot.v1`, and a 512 KiB decoded-body limit. It carries canonical unsigned-decimal JSON strings for `sequence:uint64` and every browser-facing byte count, a `logical_cpu_count` integer 1–1024, and UTF-8 `collector_version` of 1–64 bytes without controls. The existing report remains capped at 64 KiB.
- **Rationale**: Decimal strings preserve browser precision; server validation enforces CPU <=100×logical count and rejects byte values above SQLite `MaxInt64`. The separate endpoint contains failure: session snapshot rejection cannot block current host health reporting.
- **Alternatives considered**:
  - Add snapshots to `/api/v1/report`: rejected because it risks legacy heartbeat compatibility/size and creates a single high-sensitivity failure path.
  - Send individual session records: rejected because it cannot atomically remove logged-off sessions or represent authoritative empty state.
  - Poll agents from the dashboard: rejected because it reverses the existing machine-auth/report trust path and adds fleet fan-out.

## Decision 2 — Record every accepted attempt; replace rows only on success

- **Decision**: An accepted attempt updates `session_snapshots` latest-attempt instance/sequence, observation/receipt/capability/status/error. For the same agent instance, acceptance requires a strictly newer sequence; equal/older attempts are successful no-ops. A different instance begins a new generation because the old process is terminated or cancelled before restart reporting. Only an accepted successful complete attempt updates nullable last-success instance/sequence, observation/receipt, `session_count`, active+connected count, idle count, disconnected count, distinct non-null projected user/domain count (zero when identity is hidden), max non-null input/connect/logon activity, and `session_latest` rows. An accepted successful empty attempt clears that host. Only WTS enumeration/metadata failures and collector timeout are fatal; a fatal attempt preserves last-good rows/counts/freshness.
- **Rationale**: WTS is a current enumeration, so successful complete replacement prevents stale ghost sessions and makes a successful empty result meaningful. A fatal collection error contains no authoritative current enumeration and must not erase useful last-good data. Separating attempt status from last-success data makes the displayed error truthful without misrepresenting stale rows as a successful empty snapshot.
- **Alternatives considered**:
  - Per-row upsert/deletion inference: rejected because interruption and missing rows leave inaccurate active sessions.
  - Clear rows on any collection error: rejected because it turns transient WTS failure into a false empty host and loses last-good diagnostics.
  - Agent wall-clock or server receive time as ordering: rejected because skew/retry/delayed delivery can overwrite a newer sequence; server receipt remains the last-success freshness authority.
  - Merge fatal-error rows into last current snapshot: rejected because consumers cannot tell which rows were enumerated. Preserve last-success rows unchanged and publish explicit attempt status instead.

## Decision 3 — Keep current state wide and bounded, with no EAV/history

- **Decision**: Schema v4 has five dashboard telemetry tables: `session_generation_fence` (one durable maximum UUIDv7 generation per host that survives snapshot privacy and retention purges), `session_snapshots` (one row per host containing latest-attempt metadata plus nullable last-success receipt/summary), `session_latest` (one wide row per last-success host/session), `session_action_outbox`, and append-only `session_action_audit`. The sixth v4 table, `session_action_ledger`, is local to the agent and records action ID, claimed/terminal state, outcome, and claim/completion/expiry times. `processes_json` holds each prevalidated bounded current list. |
- **Rationale**: Queries are host/summary-oriented, retention is short, and each process list is at most five. The distinct agent ledger makes claimed-before-WTS durable across restart without exposing local execution state as dashboard telemetry. Wide rows avoid EAV joins and history table growth while supporting explicit privacy deletion.
- **Alternatives considered**:
  - EAV/session metric rows: rejected due to write amplification, query complexity, and no chart/history requirement.
  - Per-session time series: rejected by product scope and privacy minimization.
  - Store raw snapshot JSON only: rejected because indexed fleet filtering/sorting and explicit policy projection require queryable current values.

## Decision 4 — Identity is host/session ID plus expected logon time for actions

- **Decision**: Current row identity is `(canonical_host, uint32 session_id)`. A disconnect/logoff/message additionally includes `expected_logon_at_ms`, checked by the agent immediately before WTS call.
- **Rationale**: Windows recycles session IDs. The logon precondition prevents an action approved for a departed user from applying to a replacement session.
- **Alternatives considered**:
  - Session ID only: rejected as unsafe reuse target.
  - User name comparison: rejected because names are non-unique/mutable and privacy-projected.
  - Broker GUID: rejected because RD Broker is explicitly not a dependency.

## Decision 5 — WTS owns metadata; agent owns live performance

- **Decision**: Use WTS enumeration/information APIs for user/domain/state/station/client/times. Use agent-local named PDH counters for CPU/working set/input delay/available RemoteFX, and Toolhelp for process accounting.
- **Rationale**: It follows locality and authority: WTS exposes session lifecycle metadata; the machine can measure real performance at the time of snapshot. It works on standalone RDSH hosts without Broker.
- **Alternatives considered**:
  - RD Broker database: rejected as non-required external dependency, unavailable on many deployments, and possibly stale.
  - Infer per-session load from aggregate host performance: rejected as not actionable or accurate.
  - Process counters alone for all metrics: rejected because input delay/RemoteFX are session/RDS counter domains.

## Decision 6 — Strict numeric PDH instance mapping

- **Decision**: Parse each applicable named PDH instance and accept it only when the documented numeric instance/session component equals the `uint32` WTS session ID exactly. Unavailable/malformed/ambiguous instances result in missing values.
- **Rationale**: A substring match could confuse session 12 with 112 or an unrelated label. Missing is safer than a misattributed performance number.
- **Alternatives considered**:
  - `strings.Contains(instance, id)`: rejected due to collisions.
  - Assume PDH enumeration order matches WTS: rejected as undocumented/unstable.
  - Fail entire snapshot for counter provider failure: rejected because WTS data remains useful.


## Decision 6a — Accept and render `unknown` session state last

- **Decision**: The validated session-state set includes `unknown`. Session tables, API payloads, and UI accept it; state ordering renders it after all known states.
- **Rationale**: WTS may not provide a recognized state for every enumerated session. Rejecting the host snapshot or coercing that session to a known state would lose or misrepresent current data.
- **Alternatives considered**:
  - Reject unknown state: rejected because one unfamiliar WTS value should not discard a bounded complete snapshot.
  - Coerce it to active/idle/disconnected: rejected because it invents actionability and aggregate meaning.

## Decision 7 — Toolhelp deltas with fixed-heap top N

- **Decision**: Enumerate processes through Toolhelp, map PIDs with `ProcessIdToSessionId`, use query-limited process handles and creation-time CPU deltas normalized by transmitted logical CPU count, close handles each cycle, and select top N with a deterministic fixed-size heap. Canonical agent processes have numeric `uint32` PID, nullable CPU, and `uint64` bytes; server ingest rejects bytes above SQLite `MaxInt64`. Per-process CPU is nullable for a first or inaccessible delta; order is CPU descending (null last), working set descending, image name case-insensitive ascending, then PID ascending.
- **Rationale**: It bounds CPU/memory and avoids retained OS handles; process identity can recycle too, so creation time participates in delta identity. N is at most five, making heap selection effectively constant-space per session.
- **Alternatives considered**:
  - Keep open handles between samples: rejected for handle leaks, stale process identity, and service resilience.
  - Sort all processes per session: rejected as avoidable allocation/work when only top 0–5 are displayed.
  - WMI process queries: rejected for added provider overhead/operational dependency where Toolhelp is direct.

## Decision 8 — Project privacy before persistence and redact every broad channel

- **Decision**: Apply visibility configuration (`full|masked|hidden`) to identity, client, and process fields before SQLite insert. Fleet summaries, SSE, audits, server logs, and safe errors carry none of these values; messages are protected at rest and deleted terminally. Hidden processes are `[]`; masked processes retain PID and metrics but set only `image_name` to `***`. Any visibility-policy change immediately privacy-deletes `session_snapshots` and cascading `session_latest`, safely cancels or expires active actions, and a later looser policy cannot reconstruct prior data.
- **Rationale**: Later redaction leaves sensitive values at rest and risks accidental propagation. Admin-only access reduces audience, but does not remove retention/minimization obligations.
- **Alternatives considered**:
  - Full data in storage, mask only UI: rejected because SQL backups/debug paths retain unneeded data.
  - Hide all values by default: rejected by shared product default (admin-only full) and operator diagnostic requirements.
  - Include PII in SSE for instant detail: rejected because subscribers are broader/longer-lived and cached browser events are not a safe detail channel.

**Mask algorithm**: a nonempty identity/client value becomes first Unicode scalar + `•••`; a one-scalar value becomes `•`; empty stays absent. Process masked `image_name` is exactly `***`. Apply identity/client masking after UTF-8 validation/truncation at a scalar boundary.

## Decision 9 — Instance/sequence acceptance; last-success receipt determines freshness

- **Decision**: The agent generates a UUID `agent_instance_id` at service start, posts synchronously from one reporting loop, and increments `sequence:uint64` for each attempt. SQLite stores latest-attempt and nullable last-success instance IDs/sequences alongside observation/receipt timestamps. For the same instance, only a strictly newer sequence is accepted; equal/older returns `202 {"accepted":false}` without mutation; accepted writes return `202 {"accepted":true}`. A different instance begins a new generation because the old process is terminated or cancelled before restart reporting. `observed_at_ms` is display/diagnostic only. Freshness is unknown with no last success; otherwise offline if registry status is off or age exceeds 10×heartbeat, stale if age exceeds 3×heartbeat, fresh otherwise. Latest fatal error is separate `collection_status=error`; display precedence is offline, error, stale, fresh/unknown.
- **Rationale**: Agent clocks can drift/spoof, while instance-scoped sequence establishes deterministic retry ordering. A fatal attempt receipt must not make old data appear newly collected. Exact thresholds separate host availability from collection quality.
- **Alternatives considered**:
  - Agent wall clock for ordering or freshness: rejected for skew and delayed/retried delivery.
  - Latest attempt receipt for freshness: rejected because an error attempt contains no successful current data.
  - Treating collection error as a freshness state: rejected because offline/stale are independent availability facts and require explicit display precedence.

## Decision 10 — SSE invalidates; it does not carry detail

- **Decision**: Publish additive `session_snapshot` with host, decimal-string latest-attempt and nullable last-success instance/sequence plus observation/receipt, exact freshness, persisted aggregate summary, and latest capability/status only. Publish privacy-safe `session_action` status events with action ID/status/result/timestamps only. The current broker broadcasts both metadata-only events to authenticated stream subscribers; non-admin clients ignore them and cannot call Sessions APIs. Expanded host panels refetch detail. The frontend keeps action status in `sessionStorage`, polls `GET /api/v1/session-actions/{action_id}` every two seconds until terminal/navigation, recovers after reload, and maps terminal state to a toast.
- **Rationale**: This limits PII/process fan-out, makes REST the one detail contract, and reduces event size under fleet load.
- **Alternatives considered**:
  - Push complete session rows: rejected for privacy, 512 KiB-scale event fan-out, and dual cache authority.
  - Role-aware broker filtering: rejected because it does not match the current broker architecture; metadata is safe for authenticated subscribers.
  - Do not publish SSE: rejected because the fleet list and open panel would wait for polling.

## Decision 11 — Actions are durable, confirmed, idempotent, and agent-executed

- **Decision**: Admin action routes are always mounted; disabled Sessions/actions produce `409 sessions_disabled`. The body has exactly `type`, `expected_logon_at_ms`, and optional `message`; unknown keys reject, message is required/nonempty only for message type, and disconnect/logoff permit only absent/empty message. The telemetry outbox persists `requested_by`, idempotency endpoint/key, and normalized fingerprint/hash under unique `(principal, endpoint, key)`. Same key plus same fingerprint replays; changed request conflicts. Protected message fields exist only for message actions.
- **Rationale**: UI retry, service restart, and report-response loss are normal. Durable request identity prevents duplicate disruptive queueing; redelivery reconciles lost delivery acknowledgements without a repeat WTS operation. The dashboard cannot safely execute a remote WTS action and should never own a server-side shadow launch.
- **Alternatives considered**:
  - Direct dashboard RPC to agent: rejected because it adds a listener/reachability/security surface and loses report retry semantics.
  - In-memory queue or idempotency ledger: rejected due to replay/loss on restart.
  - Mark delivered commands permanently ineligible: rejected because a lost response can strand a command without a terminal outcome.
  - Browser directly invokes host APIs: rejected because authentication/authorization/cross-host topology would be unsafe.
  - Action immediately without confirmation: rejected as consequential external side effect.

## Decision 12 — Shadow launches through a strictly validated local protocol

- **Decision**: The fresh-session endpoint returns both `drainctl-shadow://shadow?host=<RFC1123-host>&session=<numeric-id>` and the exact command fallback `mstsc.exe /v:<RFC1123-host> /shadow:<numeric-id> /control`. The UI never constructs the URI, verifies the endpoint value exactly, and navigates only after explicit Shadow activation. The installer-registered GUI helper strictly parses the complete URI and invokes local mstsc with fixed separate arguments; it never uses a shell or includes `/noConsentPrompt`.
- **Rationale**: Browser navigation starts mstsc in the browser user's local context without a dashboard-side process launch. Because any site or local app can invoke the registered URI, strict helper parsing is the security boundary. Browser/Windows external-protocol confirmation and normal mstsc consent remain.
- **Alternatives considered**:
  - Server launch: rejected for unauthorized remote execution/desktop context ambiguity.
  - Add `/noConsentPrompt`: rejected by explicit safety boundary.
  - Copy arbitrary raw host string or accept arbitrary URI query values: rejected for command/protocol injection.

## Decision 13 — Reuse ServerTable behavior without conflating data models

- **Decision**: Build dedicated Sessions components/state and borrow the current brutalist table patterns: filter pills, deterministic sort/page, expandable keyboard rows, ConfirmDialog/Toast, status styles, and responsive inner scroll.
- **Rationale**: Session data is bounded current detail with stricter privacy and fetch lifecycle, unlike host aggregate server state. Directly adding it to `ServerTable` would combine unrelated screen responsibilities and grow its already large component.
- **Alternatives considered**:
  - New visual design: rejected because mockup follows current dashboard style.
  - Put sessions into existing `appState.servers`: rejected because host polling/cache invalidation and PII detail lifetime differ.
  - Client-side fleet filtering of all session rows: rejected for PII, scale, and pagination requirements.


## Decision 13a — Verify frontend according to repository capability

- **Decision**: The frontend currently has Vite build scripts and no dedicated test runner. Pure deterministic helpers may use Node's built-in test runner. Interaction evidence is the fully enumerated deterministic OMP/browser-driver procedure in quickstart plus production `pnpm build`; it is not represented as a nonexistent checked-in browser test command.
- **Rationale**: Requiring a nonexistent Svelte interaction-test convention creates un-runnable work. Browser proof exercises the rendered controls and accessibility states while the production build catches integration/compiler errors.
- **Alternatives considered**:
  - Require Vitest/Playwright tests: rejected because neither is configured.
  - Test Svelte interaction with Node alone: rejected because component interaction requires a browser surface.

## Scale and resource budget rationale

- A maximum snapshot is 500 sessions × five process records. This bound plus 512 KiB body limit prevents a compromised/misconfigured agent from consuming unbounded decode, database, or browser resources.
- Fleet queries return at most 50 summaries and never PII. A detailed host query is at most 500 rows and is on explicit expansion only.
- Snapshot replacement is one indexed transaction, not N network requests. A single metadata SSE event replaces mass private broadcasts.
- Fixed-heap process selection uses O(top_processes) additional memory per active collection group, not an O(processes) sorted allocation. Process handles are closed per collection round.
- Retention at 24 hours default bounds sensitive current snapshot data. Cleanup uses fixed batches and a resumable schedule so it cannot lock SQLite for the whole fleet. Terminal outbox/audit/ledger records have independent action-relative retention and are not deleted merely because a snapshot is purged.

## Privacy/security rationale

- WTS user/client/process fields can be personal/sensitive. The feature defaults to full only because reads are admin-only, but policy mode is applied before storage to enforce minimization at every export boundary.
- The only broad live stream (`session_snapshot` SSE) is intentionally metadata-only. Browser network traces/event replay cannot reveal users/processes through it.
- Auditability needs who requested a disruptive action and whether it happened, not what user/process/message was involved. Audit fields are structured codes/IDs/times only.
- Message text must remain available only until agent delivery/execution; protect it with existing secret-at-rest infrastructure, omit it from all response/event/log views, and delete it on terminal state/expiry.
- Machine identity prevents arbitrary dashboard users/hosts from claiming or replacing snapshots. Admin role is decided server-side from authenticated identity, not client input.

## Compatibility and migration strategy

| Transition | Behavior |
|---|---|
| New dashboard + old agent | Existing report works. Session list shows unsupported/no current data rather than inventing empty sessions. |
| New agent + old dashboard | Snapshot endpoint absence/rejection is isolated from heartbeat; agent logs safe capability code and continues. |
| New agent + new dashboard | Independent snapshot posts begin after remote settings enable collection; dashboard uses v4 current state. |
| Dashboard upgrade | Transactional additive v4 migration; existing host/telemetry/auth data remains unchanged. |
| Dashboard rollback | Older binary ignores the five dashboard v4 tables; an older agent ignores its local ledger. Existing behavior remains and rollback does not restore private data. |
| Config change | Next remote settings pull changes collector behavior. Settings validation prevents invalid limits/modes; a later valid complete snapshot replaces prior projected data. |

## Required failure semantics

1. **Malformed/oversized input**: reject before mutation, code-only safe diagnostic; preserve last complete data. Reject noncanonical decimal strings, collector-version controls, CPU above 100×logical CPUs, and bytes above SQLite `MaxInt64`.
2. **Fatal WTS collection error**: only WTS enumeration/metadata failure or collector timeout records a newer latest attempt/status/error; preserve last-success receipt/final aggregates and `session_latest` rows. Optional PDH/process failures produce successful rows with null metric/capability false. The UI applies offline, error, stale, fresh/unknown display precedence; only a newer successful complete empty snapshot clears rows.
3. **Snapshot storage outage**: no partial replace, no SSE; return 503 and retry naturally.
4. **Query outage**: return typed unavailable, UI leaves explicit error rather than stale local data masquerading as fresh.
5. **Stale/offline host**: unknown has no last success; offline is registry-off or age >10 heartbeats; stale is age >3; otherwise fresh. Preserve snapshot only within snapshot retention, disable actions unless fresh/error-free, and never auto-logoff.
6. **Action duplicate/retry/restart**: same request key/fingerprint replays; changed request conflicts; at most 20 are delivered/reported; outbox plus claimed/terminal ledger ensures no repeat WTS call while allowing same-ID redelivery.
7. **Action mismatch/expiry**: agent does not execute; terminal result/audit code explains `session_changed` or `expired` without PII. Terminal outbox/audit/ledger cleanup remains action-relative after snapshot purge.
8. **Clipboard unavailable**: show accessible copy failure; do not substitute server launch or an unsafe fallback.

## External API decisions

| API | Use | Safety detail |
|---|---|---|
| `WTSEnumerateSessionsW` | Enumerate current local RDS sessions | Free returned buffer with `WTSFreeMemory`; preserve existing exclusion of pseudo-sessions. |
| `WTSQuerySessionInformationW` | User/domain/state/station/client/logon/idle metadata | Query each documented class; absence is null, not an invented value. |
| `WTSDisconnectSession` | Execute confirmed disconnect | Only after current session/logon timestamp match; wait is false and no message exists. |
| `WTSLogoffSession` | Execute confirmed logoff | Only after current session/logon timestamp match; report success/failure code. |
| `WTSSendMessageW` | Execute confirmed message | Only after match; text bounded/protected/no audit echo. |
| PDH APIs | Named per-session CPU/memory/input delay/RemoteFX counters | Exact numeric instance mapping; missing provider/value remains null. |
| Toolhelp (`CreateToolhelp32Snapshot`, `Process32First/Next`) | Enumerate candidate processes | Close snapshot; map PID to session before process inspection. |
| `ProcessIdToSessionId` | Assign PID to session | Skip inaccessible/unmapped PIDs. |
| Query-limited process handle + process times/memory info | CPU delta/working set | Close every handle; include process creation time in delta identity; normalize CPU by logical processors. |

## Non-goal confirmation

The implementation must not add Broker DB calls, historical session analytics, a generic remote command channel, direct remote WTS API use from the dashboard, bulk/disconnect/reset/session-management features, server-side process launches, consent bypass shadow flags, unbounded process/session storage, browser-local data as current truth, or any PII/process/message data in logs/audits/SSE/fleet summaries.
