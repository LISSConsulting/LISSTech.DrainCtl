# Requirements Audit Checklist: Fleet-wide Sessions Tab (012)

**Purpose:** implementation-readiness and release-completeness audit for privacy-sensitive fleet sessions. Mark an item complete only with linked implementation, targeted test, or numbered quickstart evidence. A green build alone is not UI, WTS, privacy, or action evidence.

**Feature:** [spec](../spec.md) · [plan](../plan.md) · [data model](../data-model.md) · [tasks](../tasks.md) · [quickstart](../quickstart.md)

## A. Baseline, boundaries, and configuration

- [ ] Baseline patch `C:/Users/mwisniowski/.omp/sessions-preexisting-20260926.patch` hashes to `452f860fa213d9157cc51038c82b7a0d41a09de2cfa51f960eae686766ac4de5`; final feature diff compares it hunk-by-hunk and records intentional overlaps.
- [ ] Identity is exactly `(canonical_host, uint32 session_id)` and every action additionally binds `expected_logon_at_ms`.
- [ ] Current-only scope is preserved: no RD Broker dependency, EAV, history table/chart, compatibility alias, or server-side shadow launch.
- [ ] Telemetry ownership is `internal/telemetry`; dashboard reaches it only through interfaces; stores are created at composition root, injected into `NewSubsystem`, and lifecycle-managed by the service loop.
- [ ] Defaults/bounds are enabled true, collect_processes true, top-N 3 (0–5), retention 24h (1–168), actions false, and full/masked/hidden visibility; settings updates are atomic.
- [ ] Authenticated users GET nonsecret config; admins alone mutate all session/privacy/action settings; machine config pull contains collector settings but no dashboard authorization data.

## B. Collector, contract, and transport

- [ ] WTS supplies metadata; Services/listener are excluded; unsupported state is valid `unknown` and sorts after recognized states.
- [ ] PDH instances attach only by strict numeric session match. PDH/process/RemoteFX failures are successful null/capability-false data; only WTS enumeration/metadata and collector timeout are fatal (not `pdh_unavailable`).
- [ ] Processes are exact-session attributed, use closing limited-query handles, creation-safe CPU deltas/logical-CPU normalization, and deterministic fixed-space top-N ordering.
- [ ] Agent emits 1–1024 `logical_cpu_count`; server rejects CPU > `100 * count` and persists last-success count.
- [ ] Agent process type has numeric PID, nullable CPU, uint64 bytes; projection preserves PID/metrics, masks only image name as `***`, and hidden yields empty process list.
- [ ] `agent_instance_id` is service-start UUID and sequence is uint64 monotonic per synchronous post attempt; restart creates a generation only after old loop termination/cancellation.
- [ ] Browser-facing `sequence` and all session/process working-set byte fields in JSON/API/SSE are canonical unsigned decimal strings; max uint64 round-trips; SQLite bytes reject above MaxInt64.
- [ ] `collector_version` is valid UTF-8, 1–64 bytes, without controls.
- [ ] Snapshot endpoint is machine-only, canonical-host bound, strict JSON (content type/encoding/keys/trailing data), 512 KiB pre-decode capped, and accepts schema `drainctl.session-snapshot.v1` only.
- [ ] Valid and stale snapshots both return 202 `{accepted:true|false}`; malformed input is deterministic non-mutating 4xx; storage error is 503; legacy 64 KiB heartbeat remains independent.

## C. v4 data, aggregates, privacy, freshness, and retention

- [ ] Dashboard migration creates exactly five canonical v4 tables: `session_generation_fence`, `session_snapshots`, `session_latest`, `session_action_outbox`, `session_action_audit`; local agent migration creates separate sixth `session_action_ledger`; all are transactional/idempotent.
- [ ] Required indexes support bounded fleet/detail/action queries, including aggregate fleet query and principal+endpoint idempotency; fleet query does not parse process JSON.
- [ ] Successful replacement atomically stores exact aggregates: session count; active+connected count; idle count; disconnected count; distinct non-null projected `(user,domain)` count (zero when identity hidden); max non-null input/connect/logon activity.
- [ ] Same-instance equal/older sequence changes neither attempt nor last-success data; only a strictly greater UUIDv7 instance advances the durable generation fence and accepts a reset sequence; lower/equal different instances are stale. The fence survives dashboard restart, privacy and retention purges, and failed replacement writes; fatal attempts preserve last success; successful empty snapshots atomically clear rows.
- [ ] Projection occurs before persistence and response. Hidden data is absent from SQLite, HTTP, DOM, ARIA, title, SSE, logs, errors, metrics, and audit; visibility changes immediately purge data/cancel relevant actions and loosening cannot restore it.
- [ ] Freshness derives only from successful server receipt: no last success unknown; registry off or age >10 heartbeats offline; otherwise age >3 heartbeats stale; otherwise fresh. Fatal latest error is separate `collection_status=error`; display precedence is offline, error, stale, fresh/unknown.
- [ ] Snapshot/current data purges at snapshot retention but never delete or lower `session_generation_fence`. Terminal outbox/audit/agent-ledger records instead use independently action-relative dashboard acknowledgement or expiry-plus-retention cutoff; no snapshot purge deletes them early.

## D. APIs, SSE, actions, and UI

- [ ] `/api/v1/me` yields `is_admin`; every Sessions API/action route returns 403 to non-admin before lookup/mutation; non-admin UI/nav and session requests are absent.
- [ ] Fleet list is bounded, indexed, aggregate-only, no-store; detail is bounded to 500 projected rows, deterministic, no-store, and uses a typed timeout/unavailable response.
- [ ] `session_snapshot` SSE is post-commit metadata-only: receipt/attempt/last-success summary/capabilities/freshness/error, safe decimal strings, no PII/client/process/logon/action/message; non-admin browser ignores it.
- [ ] Frontend treats snapshot SSE as summary cue only, refetches expanded detail, orders by instance/sequence, and does not erase last-good detail on latest error.
- [ ] Admin action routes always exist. Disabled actions return `409 sessions_disabled`; stale/capability/logon/idempotency conflicts are typed safe outcomes.
- [ ] Disconnect/logoff bodies permit exactly type/expected logon and empty-or-omitted message; message type requires nonempty bounded message; unknown keys reject.
- [ ] Dashboard queue is protected, idempotent by principal+endpoint/fingerprint, expires pending work at five minutes, delivers <=20 queued/delivered commands deterministically, and redelivers same ID until terminal/expiry.
- [ ] Agent ledger inserts durable `claimed` before WTS; restart after claim never repeats WTS and reports a safe failed/duplicate outcome; terminal completion is durable; cleanup waits for dashboard acknowledgement or expiry-plus-retention. Disconnect re-enumerates the exact logon time and calls `WTSDisconnectSession(..., false)` without message content.
- [ ] `session_action` SSE status carries only safe action ID/lifecycle/outcome/timestamps. Frontend saves pending IDs in sessionStorage, polls status every 2 seconds, stops on terminal/navigation, resumes after reload, and announces terminal safe toast.
- [ ] UI is admin-only, accessible/keyboard-operable/responsive, differentiates unavailable from zero, exposes no hidden DOM value, has confirmation focus safety, launches only the exact endpoint-provided Shadow URI, and offers the exact command as a copy fallback; the local protocol helper validates all invocation input before fixed-argument mstsc execution.

## E. Required executable evidence

- [ ] Targeted tests cover contract bounds (including decimal uint64), WTS/PDH/process handling, logical CPU validation, aggregates/index query plan, transactional sequencing/replacement, six-table lifecycle, generation-fence survival, independent retention, exact freshness boundary/precedence, visibility purge, and API role/privacy/SSE behavior.
- [ ] Action tests cover disabled route, exact body shape, idempotency/replay/conflict, protected deletion, 20 delivery cap/redelivery, expiry/reuse guard, durable claim/restart/no-double-WTS, acknowledgement/expiry cleanup, status SSE, and 2-second reload recovery.
- [ ] Production frontend build, Node pure helper tests, deterministic browser-driver procedure, deterministic mock/SQLite procedure, and Windows RDS procedure in quickstart all pass with every numbered observation recorded.
- [ ] Final release review verifies baseline hunks, clean cutover, OpenAPI/operator documentation, accessibility/500-row scale/512 KiB boundary, and absence of sensitive data in restricted surfaces.

## Audit result

- [ ] No unchecked blocker remains.
- [ ] Evidence links/commands/results are attached to the implementation PR or release record.
- [ ] Feature is ready only when every applicable item is complete.
