# Phase 0 Research: AI Anomaly Investigation

## Purpose

Resolve the implementation decisions for the optional `typesafe_jev` investigation path and the deterministic central session-drop detector. This research is based on the clarified feature specification, the DrainCtl constitution, the current repository, TypeSafe's [System One OpenAPI](https://api.typesafe.ai/openapi.json), and TypeSafe's [privacy policy](https://typesafe.ai/legal/privacy-policy). Every decision below is implementation-binding; no inherited 006 default is treated as current truth.

---

## R1. Fixed TypeSafe HTTP integration and consent

**Decision**: Implement one direct `net/http` JSON client for the fixed `typesafe_jev` profile: `POST https://api.typesafe.ai/v1/systemone`, normal platform TLS trust (TLS 1.2+), and `Authorization: Bearer <credential>`. Do not make the endpoint, redirect policy, URL components, or provider profile configurable. Send exactly four fixed System One questions—likely cause, impact, evidence sufficiency, and human review—and require one `choice` answer for each. Although the generic provider protocol supports `score` and `noul` answer discriminators, this feature deliberately never sends or accepts either; it sends no dynamic question types or prose. The OpenAPI documents bearer authentication and `POST /v1/systemone`; it documents no idempotency header, so each durable attempt has exactly one wire transmission.

Provider access and automatic investigation remain independently disabled by default. Enabling provider access requires persisted acknowledgement of the third-party transfer, United States/service-provider processing, no model training or fine tuning on API Input, no fixed public external retention period, and the fact that DrainCtl cannot control third-party deletion. Local `AuditDays` applies only to DrainCtl copies.

**Rationale**:
- The authoritative [OpenAPI](https://api.typesafe.ai/openapi.json) defines this endpoint, HTTP bearer security, a `state` request, and typed `choice`, `score`, and `noul` answer discriminators. The latter two are generic provider capabilities intentionally unused by this fixed four-`choice`-question product contract. A direct client avoids an SDK dependency and makes byte, redirect, decompression, header, and response-validation limits explicit.
- The [privacy policy](https://typesafe.ai/legal/privacy-policy) says API Input is collected, is not used to train or fine tune models, may be disclosed to service providers, is hosted/processed in the United States, and is retained as reasonably necessary rather than under a fixed published period.
- One profile gives operators a bounded egress destination and makes an acknowledgement meaningful. It also satisfies the specification's prohibition on a fallback provider.

**Alternatives considered**:
- **Provider SDK**: rejected — adds a dependency and hides transport limits without adding a supported capability.
- **Custom endpoint or multi-provider registry**: rejected — expands egress and consent scope, and contradicts the sole-provider requirement.
- **Automatic retries or an invented idempotency header**: rejected — the official contract does not document one; retries could duplicate a provider request. Only a linked, explicit operator retry may make another transmission.
- **Implied consent from entering a credential**: rejected — the privacy boundary and automatic-analysis choice must be separately explicit and durable.

---

## R2. Central ownership and unchanged agent contracts

**Decision**: The dashboard/service instance owns provider configuration, DPAPI credential use, evidence assembly, attempt persistence, System One calls, and session-drop scoring. Agents keep reporting their existing `CheckResult`; existing `SpikePayload`, evtspike notification behavior, and agent report flow stay unchanged. The dashboard consumes persisted `event_spikes` plus accepted reports and emits additive central APIs/SSE only.

**Rationale**:
- `internal/dashboard/handlers_servers.go` accepts and validates agent reports before `ServerState.Update`; `internal/dashboard/store.go` owns central server state.
- `internal/telemetry/event_spikes.go` deliberately keeps `EventSpike` as plain telemetry data and maps to/from root `SpikePayload` only at the dashboard boundary, avoiding an import cycle. Its durable identity and `ON CONFLICT` handling are the correct evtspike source input.
- `internal/dashboard/client.go` defines `RemoteSettings` for the machine-account `GET /api/v1/config` pull. Provider authority in that DTO would distribute a secret and an external-call capability to agents, which the specification forbids.

**Alternatives considered**:
- **Agent-side provider calls**: rejected — leaks credentials and evidence assembly to every host, multiplies egress, and violates the central-only boundary.
- **Extend `SpikePayload` or `CheckResult` with investigation data**: rejected — changes mature agent and notification contracts; additive central records do not require it.
- **A separate browser-local investigation store**: rejected — cannot provide durable audit/recovery semantics and would make a browser authoritative.

---

## R3. Strict evidence and result schemas

**Decision**: Define versioned Go DTOs with no maps, interfaces, raw JSON, or custom free-text fields. Canonical registered host remains in the durable deterministic source record and may be returned only by authenticated dashboard-group source/list/detail APIs; it is not an evidence field. Assemble provider-bound evidence field-by-field in fixed order: anonymized source facts; freshness/drain/detector status; local aggregates; anonymous fleet aggregates. Replace the canonical source host with `source` and peers with `peer_n`; channel values are known codes or `custom_channel`; unavailable and omitted fields use fixed enums. Serialize once, measure UTF-8 bytes, deterministically omit trailing permitted fields until System One `state` is at most 8,000 bytes and the whole JSON request is at most 16 KiB.

Send exactly four static `choice` questions with fixed criteria: likely cause, impact, evidence sufficiency, and human review. Validate that a response contains exactly one answer for each fixed question name; reject missing, duplicate, or fifth answers, every non-`choice` discriminator, values outside that question's closed enum, and prose. Retain only those schema-valid typed values and non-secret provenance; discard raw response bodies. The hostname is forbidden from the provider request, persisted sanitized evidence snapshot, result, provenance, provider error, and provider diagnostic payload. Bound response headers at 16 KiB, decompressed success bodies at 32 KiB, and discarded error bodies at 4 KiB.

**Rationale**:
- The TypeSafe OpenAPI's flexible `instructions`, criteria objects, and answer properties are provider capabilities, not permission to send arbitrary evidence. DrainCtl's own DTO must be narrower than that API.
- Fixed serialization and limits make the egress boundary testable by byte capture rather than by best-effort redaction.
- The spec requires provider output to remain a hypothesis. A closed result schema makes the dashboard capable of suppressing cause/impact whenever evidence is insufficient, and prevents prose from becoming an asserted diagnosis.

**Alternatives considered**:
- **Marshal existing `CheckResult`, `SpikePayload`, telemetry rows, or structs directly**: rejected — they can acquire excluded fields and are not a stable privacy allowlist.
- **Generic `map[string]any` evidence/result storage**: rejected — permits schema drift and bypasses compile-time review.
- **Persist raw response for support**: rejected — violates the no-prose/no-raw-response requirement and creates a new retention/privacy problem.
- **Ask the provider for a narrative root cause or remediation**: rejected — violates hypothesis-only and no-autonomous-action requirements.

---

## R4. DPAPI credential lifecycle and route separation

**Decision**: Add a provider configuration block to root `Config` in `config.json`, using the existing DPAPI encrypted-on-disk/plaintext-runtime model. Its credential is write-only. Every provider settings `PUT` MUST include one explicit credential command: `preserve` retains the current credential, `replace` supplies a non-empty replacement, and `clear` removes it. A missing, null, empty, unknown, or conflicting credential command is invalid; it MUST NOT preserve or otherwise mutate the credential. Safe session-only configuration views expose provider enabled/automatic/acknowledged state and `has_credential`, never the credential or encrypted `dpapi:` value. The provider block is deliberately absent from `RemoteSettings`, `GET /api/v1/config`, and all machine-account routes.

**Rationale**:
- `config.go` already implements the required lifecycle: `DecryptSecrets` decrypts `dpapi:` material only for runtime use, `configForPersistence` encrypts a cloned copy, and `saveConfigToFile` uses the named mutex plus atomic replacement. This preserves in-memory runtime secrets while persisting a protected copy.
- `internal/dashboard/handlers_settings.go` already distinguishes values and uses scoped updates; its notification secret policy supplies the local model for explicit preserve/replace/clear semantics, while the provider route requires a command on every `PUT`.
- `internal/dashboard/server.go` deliberately separates session-authenticated management routes from SSPI machine-account `GET /api/v1/config`; `internal/dashboard/client.go:RemoteSettings` is exactly the projection consumed by agents.

**Alternatives considered**:
- **Allow an omitted credential command to preserve the current value**: rejected — every provider settings `PUT` must make credential intent explicit, so omission is invalid rather than an implicit preserve.
- **New credential file, environment variable, or registry key**: rejected — violates the constitution's single JSON configuration flow and creates another rotation/ACL surface.
- **Return masked ciphertext or a reusable browser secret token**: rejected — encrypted material is still sensitive and a token could become an exfiltration capability.
- **Put safe provider fields in machine config so agents can score locally**: rejected — even non-secret provider authority violates central ownership and risks future credential/call leakage.

---

## R5. SQLite v4 attempt, source, and baseline persistence

**Decision**: Advance `internal/telemetry/schema.go` from schema version 3 to additive version 4 in its existing idempotent DDL transaction. Add focused telemetry stores for:

1. immutable investigation attempts, including only stable source kind/ID links, initiation type, retry predecessor, state/timestamps, sanitized evidence snapshot/version/hash, strict result or safe failure, and non-secret provenance; the linked deterministic source rows retain canonical registered host, without copying it into attempts, evidence/result/provenance, or provider error/diagnostic fields;
2. distinct session-drop anomalies, including canonical registered host and source facts, classification (`unexplained`, `drain_associated`, or `unknown_context`), confirmation/baseline summary, and provider eligibility; and
3. per-host session baseline state: 96 source-local slots plus one all-hours fallback trained only by that host, last eligible observation, confirmation state, drain horizon, cooldown, and report-order watermark.

Use foreign keys/check constraints/unique indexes to enforce attempt links by stable source kind/ID, host-keyed session-drop uniqueness/querying, retry lineage, terminal immutability at the store API, and query order. Store timestamps as UTC epoch milliseconds, matching existing telemetry. Persist the host-free evidence snapshot and its host-free attempt row in a successful SQLite write before the worker can transmit. Canonical host is resolved from the linked deterministic source row only for authenticated dashboard-group source/list/detail APIs and is never copied into browser-local durable state. Use the existing DB's one writer pool, WAL, full-synchronous audit pool only where the new durable investigation record requires it, bounded reader pool, and local-fixed-drive restriction.

**Rationale**:
- `internal/telemetry/schema.go` already applies `CREATE ... IF NOT EXISTS` inside one transaction and advances `PRAGMA user_version` without downgrading a future schema; v4 is the established additive migration mechanism.
- `internal/telemetry/db.go` has a single writer connection, WAL pragma setup on every pooled connection, and separate `auditDB` with `synchronous=FULL`; durable audit-style attempts must reuse that ownership rather than introduce a second SQLite database or writer.
- `event_spikes` supplies a durable evtspike ID and idempotent identity. Session drops need their own representation because the specification explicitly prohibits changing `SpikePayload` semantics.
- Baseline state must survive restart; recomputing it from a short-retention metrics tier could silently change detection after expiry and cannot accurately reconstruct unknown/out-of-order gaps.

**Alternatives considered**:
- **A mutable investigation parent with changing status**: rejected — the required audit model is immutable ordered attempts; a retry must not overwrite historical evidence/result.
- **JSON files or an in-memory baseline**: rejected — lacks atomic source/attempt linkage, crash recovery, retention, and central visibility.
- **New database or per-host database**: rejected — duplicates migration/ACL/WAL ownership and breaks the single central audit surface.
- **Reuse `event_spikes` for lower-tail anomalies**: rejected — would overload an upper-tail event-log payload contract and confuse existing consumers.

---

## R6. Worker, idempotency, and crash lifecycle

**Decision**: Run one dashboard-owned worker. It selects at most one queued attempt at a time, keeps at most 100 nonterminal rows, enforces 10 requests/minute with burst 2, and applies a 30-second request context. A source's prospective automatic root attempt is uniquely claimed from its durable identity; queued/running duplicate requests return that existing ID, and a completed source creates no new attempt. A failed or insufficient terminal attempt can produce only an explicitly authorized linked retry with a fresh snapshot.

The lifecycle is `queued → running → completed|insufficient_evidence|failed`. The worker validates current enabled/acknowledged/credential configuration immediately before its first send; it marks the row running durably, then sends once. On startup, transactionally mark all leftover `running` rows `failed/interrupted`; do not resend them. Disablement or invalid current configuration cancels queued rows before send. Storage failure before evidence persistence emits a structured local diagnostic and makes zero egress.

**Rationale**:
- The TypeSafe contract has no documented idempotency mechanism, so local durable state is the only correct protection against duplicate transmissions.
- `internal/telemetry/event_spikes.go` demonstrates durable deduplication with an SQLite uniqueness constraint and returning the established ID; the new source-claim query follows the same database-first pattern, rather than in-memory locks.
- The existing subsystem architecture documents cooperative `Start`/`Stop` ownership (`internal/evtspike/subsystem.go` and `internal/lifecycle`); a single worker can be stopped/drained with the dashboard service without a detached retry engine.

**Alternatives considered**:
- **Concurrent workers or a generic queue package**: rejected — adds ordering/race complexity without a throughput requirement beyond ten requests/minute.
- **HTTP-client automatic retries**: rejected — each retry is another undocumented provider transmission.
- **Requeue running work after a crash**: rejected — a crash can occur after bytes reached the provider but before a response was stored.
- **Mark running before evidence persistence**: rejected — would leave an unauditable transmission candidate and violates the no-egress-before-storage rule.

---

## R7. Session lower-tail model and poisoning prevention

**Decision**: Implement a separately testable central lower-tail detector that receives each distinct accepted report after dashboard validation. It carries the canonical registered host as the durable session-drop source identity and scores `TotalSessions = ActiveSessions + DisconnectedSessions` once only when enumeration is non-nil/valid, the report is fresh, and its report epoch is strictly in order. Numeric zero is valid. Nil enumeration, invalid/unavailable/stale telemetry, and out-of-order reports create an unknown gap: break confirmation and neither train nor lower the baseline.

Maintain 96 canonical-host-local quarter-hour slots plus one all-hours fallback trained only by that host. Require seven distinct eligible local days for slot maturity and at least 20 eligible normal observations spanning at least 24 hours for all-hours maturity, with time-based decay, a validated default lower predictive-tail threshold of `1e-4`, an absolute drop of at least 3, a relative drop of at least 30% from the last eligible observation, fixed two-of-three consecutive confirmation, and a 60-minute host cooldown. Candidates and confirmed values never train or lower either baseline. Any non-`AllowAll` drain overlap and one confirmation horizon after drain end also block training/lowering; persist the confirmed anomaly as drain-associated rather than provider eligible. Only an unexplained confirmation is eligible for investigation, which links attempts by stable source kind/ID without copying the canonical host.

**Rationale**:
- `check.go` already treats a successful `GetSessionSummary()` as data and absence as nil; preserving that distinction prevents a collection failure from being converted to zero.
- Existing evtspike uses a mature, slot-aware detector and robust anti-poisoning behavior, but its old 006 bucket/default values are not adopted: this feature's current clarified requirements are source-local 96 slots, seven eligible days, lower tail, and session-specific thresholds.
- Central scoring sees the accepted report sequence and dashboard drain/freshness state, which an individual agent cannot reliably correlate fleet-wide.

**Alternatives considered**:
- **Treat missing sessions as zero**: rejected — turns telemetry failure into a false operational loss.
- **Train candidate/confirmed drops, drain-period values, or post-drain-horizon values**: rejected — adapts the baseline downward to the behavior the detector must retain sensitivity to.
- **Use a shared cross-host baseline or a simple fixed threshold**: rejected — violates the per-host model, loses source-local daily seasonality, and produces materially weaker detection.
- **Use evtspike `SpikePayload` or trigger notifications**: rejected — lower-tail session anomalies require a distinct identity and explicitly add no notification trigger.

---

## R8. Retention, migration, and rollback

**Decision**: Extend `telemetry.Retention.RunOnce` with chunked deletion of investigation attempts, their evidence/results/provenance, and session-drop records using `attempt.created_at_ms < now - AuditDays`; source anomaly retention remains independent and unchanged. Delete dependent rows through foreign keys/cascade or ordered child-first queries in the same tier. Keep baseline state while its host remains known; remove it only as part of an explicit server-removal retention policy, not attempt expiry. Record the work in the existing `maintenance_jobs` retention result.

The v4 migration is additive: no existing table, index, source record, audit, telemetry, or configuration value is rewritten. Rollback to an older binary leaves v4 tables in place; old code may ignore them. If an older binary rewrites `config.json` and drops unknown provider fields, a re-upgrade must default provider access and acknowledgement to disabled until explicitly restored. Do not attempt to reconstruct pre-upgrade freshness, status, baseline, or provider evidence from unbounded historical data.

**Rationale**:
- `internal/telemetry/retention.go` already applies chunked deletion and records partial failures; its existing `event_spikes` retention must remain based on its own established cutoff rather than becoming coupled to investigation rows.
- `internal/telemetry/schema.go` is additive and deliberately never lowers a future `user_version`, so an old version need not understand v4 tables to preserve its known data.
- The constitution requires durable data changes to state retention, migration, rollback, and concurrent ownership; this design does so without an irreversible data conversion.

**Alternatives considered**:
- **Anchor investigation retention to source event time/window**: rejected — delayed manual retries must retain their own audit horizon from creation.
- **Delete the source spike/session anomaly when its last attempt expires**: rejected — changes existing source retention and loses detector history.
- **Downgrade or drop v4 tables during rollback**: rejected — destructive and unnecessary; older versions can ignore additive data.
- **Backfill attempts or session baselines on upgrade**: rejected — would create egress/history based on unavailable context and violates no automatic replay/backfill.

---

## R9. Dashboard API and SSE compatibility

**Decision**: Add session-authenticated REST endpoints for safe provider configuration/status, source/attempt list and detail, explicit attempt request, and authorized retry; add distinct dashboard REST/SSE types for session-drop anomalies and investigation attempt changes. Dashboard-group source/list/detail APIs may resolve and display the canonical registered host from the linked deterministic source row alongside stable source kind/ID. An investigation attempt itself stores and exposes only its stable source kind/ID link. SSE, provider evidence/results/provenance/errors/diagnostics, and browser-local durable state use no hostname. Register the routes beside the existing management routes in `internal/dashboard/server.go` under the existing session middleware. Do not add them to machine-account routes, `RemoteSettings`, the agent report response, or root config output.

Use the existing `SSEEvent` envelope and broker: introduce new `type` values with additive `data` objects, publish only after durable writes, and have Svelte ignore unknown event types as it does today. Add front-end state/UI only for the new types. Preserve `/api/evtspike/*`, `recent_spike`, `detector_status`, existing polling, and all `SpikePayload` consumers unchanged.

**Rationale**:
- `internal/dashboard/server.go` clearly divides session routes from the SSPI machine `GET /api/v1/config` route.
- `internal/dashboard/broker.go` has nonblocking broadcast/slow-subscriber eviction, and `internal/dashboard/sse.go` already sets SSE headers, disables the write deadline, emits 25-second keepalives, and revalidates sessions. Reusing it maintains the established availability/security behavior.
- `frontend/src/App.svelte` dispatches by `event.type` and safely ignores unrecognized events, so additive types preserve older dashboard clients during rolling upgrades.

**Alternatives considered**:
- **Change existing `recent_spike` payloads to carry investigations/session drops**: rejected — breaks evtspike consumers and conflates anomaly identities.
- **Send provider configuration or attempt evidence through SSE settings updates**: rejected — SSE is broadcast to browsers and must remain safe/redacted.
- **Polling-only UI**: rejected — loses established immediate operator visibility; REST remains the recovery/initial-load source.

---

## R10. Security, observability, and test strategy

**Decision**: Treat provider credentials, provider-bound evidence, raw response, provider artifacts, and arbitrary diagnostics as secrets at every provider boundary. The canonical registered host remains required in the durable deterministic source record and authorized source/list/detail operator view, while existing operational telemetry/audit/dashboard host identity remains unchanged. Investigation attempts retain only stable source kind/ID links; authorized views resolve the host from the linked deterministic source row. Do not copy it into attempt columns, evidence snapshots, result/provenance, provider error/diagnostic payloads, SSE, or browser-local durable state. Limit, validate, and redact before logging, provider persistence, API response, SSE publication, audit, or telemetry. Emit structured non-secret diagnostics for disabled, ready/configured, automatic-enabled, degraded, and failing provider states without secrets, evidence, provider prose, or hostname. Do not use provider output for notification, remediation, or authorization.

Implement unit tests for allowlist order/omission/byte limits and canonical-host substitution; the exact four-question `choice` contract, including rejection of dynamic question names/types, `score`, `noul`, prose, and missing, duplicate, or fifth answers; configuration acknowledgement and credential preserve/replace/clear; lifecycle/idempotency; rate limiting; canonical-host-keyed session-baseline maturity/decay/confirmation; zero-vs-nil; stale/order gaps; drain horizon; and anti-poisoning. Add storage/integration tests for schema-v3-to-v4 migration, host-keyed session-drop persistence/query and authenticated source/list/detail host resolution, host-free attempt columns and evidence/result/provenance/provider errors/diagnostics/SSE/browser-durable state, attempt persistence/immutability/retry linkage/crash recovery, no-egress-before-persistence, and source-key idempotency. Exercise dashboard authorization and SSE compatibility, HTTP request/response bounds, and no agent credential/config/provider-call path.

**Rationale**:
- `config.go` and `internal/dashboard/handlers_settings.go` already distinguish safe operator views from persisted secrets; `internal/dashboard/openapi.yaml` explicitly documents that SSE settings broadcasts contain empty secret fields.
- `internal/telemetry/db.go` applies ACLs to database, WAL, and SHM files and rejects non-local storage, so new sensitive local records inherit an established protected persistence boundary.
- The constitution requires boundary-level automated verification for storage, service, dashboard, and configuration changes, plus observable operator-facing diagnosis.

**Alternatives considered**:
- **Log raw HTTP failures or retain captures for diagnosis**: rejected — they can contain evidence or provider prose and violate the data boundary.
- **Only unit-test serialization**: rejected — authorization, DPAPI, SQLite recovery, HTTP bounds, SSE, and migration are cross-boundary behaviors.
- **Use provider output to trigger existing notification/remediation paths**: rejected — output is non-authoritative hypothesis data and this feature explicitly forbids autonomous action.
- **Add a separate observability database or telemetry pipeline**: rejected — the existing SQLite audit/maintenance/dashboard surfaces provide the durable local diagnostics required.

---

## Constitution impact and Phase 1 recheck

**Decision**: Proceed with the design only if Phase 1 reconfirms all five constitutional principles against the concrete data model, contracts, source tree, and tasks.

**Rationale**:
- **Windows-first delivery**: every new Go file must carry `//go:build windows`; affected surfaces are central Windows service/dashboard and `config.json`. Root package configuration is updated; CLI, PowerShell, installer, DLL/cshared, machine agents, and notifications are intentionally unchanged.
- **Stable operator surfaces**: all additions are session-only dashboard APIs/SSE and safe config fields. Existing agent config projection, `CheckResult`, `SpikePayload`, evtspike APIs/SSE, and notification contracts remain unchanged.
- **Tests and zero-noise verification**: the feature crosses provider HTTP, SQLite, DPAPI/configuration, service, dashboard, and SSE boundaries, so both isolated and boundary tests are mandatory.
- **Config and release discipline**: configuration stays in `config.json` through atomic scoped updates; v4 is additive; no release-version mechanism changes.
- **Operational observability**: durable attempts, safe provider state, structured diagnostics, dashboard history, REST, and SSE make background work inspectable.

**Alternatives considered**:
- **Defer constitutional review until coding**: rejected — the constitution requires the plan gate before implementation and a post-design recheck.
- **Create new packages/tables without justification**: rejected — telemetry store additions are necessary for independent durable attempt/session-baseline lifecycles; a new provider package is justified only if it keeps strict HTTP/schema logic out of dashboard route handlers. Phase 1 must record the final source-tree choice and why an in-package implementation would be less maintainable.
