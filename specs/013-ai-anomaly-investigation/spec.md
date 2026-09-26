# Feature Specification: AI Anomaly Investigation (013)

**Feature Branch**: `013-ai-anomaly-investigation`
**Created**: 2026-09-26
**Status**: Draft
**Scope**: Opt-in, evidence-bounded investigation hypotheses for confirmed anomalies from existing evtspike events and a new deterministic lower-tail session-drop detector.

## Evidence boundary

This feature makes no new anomaly determination through a provider. Existing deterministic detectors remain the authority for whether an anomaly occurred. A confirmed anomaly starts an investigation only when the separately configured automatic-investigation option is enabled, or when an authenticated dashboard-group operator explicitly requests or retries it.

Only a bounded, versioned, structured allowlist DTO may leave the central service through the configured provider boundary. It is assembled field by field and may contain source anomaly facts keyed only by stable source kind/ID; retained aggregate metrics around the event; drain transitions; host freshness; detector status; and fleet temporal correlation. It MUST exclude canonical host names, FQDNs, domains, IP addresses, customer names, usernames, session identifiers, `changed_by`, free text, URLs, paths, generic maps, raw structs, Event Log messages or XML, crash dumps, credentials, notification secrets, and arbitrary files. Canonical host identity remains required in the durable deterministic source anomaly and MAY be displayed only by authenticated dashboard-group source/list/detail APIs; it MUST NOT be copied into provider-bound or provider-derived artifacts. Provider output is a typed hypothesis, never prose or an asserted root cause.

## Clarifications

### Session 2026-09-26

- **Q: What is in scope, and which component owns analysis? → A:** Session-drop detection is in scope now. The central dashboard/service instance owns provider configuration, evidence assembly, provider calls, durable attempts, and central lower-tail scoring from distinct accepted reports; agents retain their existing `CheckResult` and `SpikePayload` contracts, receive neither provider keys nor provider calls, and existing evtspike behavior remains unchanged.
- **Q: Which provider and consent model are supported? → A:** The sole initial profile is `typesafe_jev` at exactly `https://api.typesafe.ai/v1/systemone`, with normal TLS 1.2+ trust and no custom URL, redirect, userinfo, query, or fragment. Provider access and automatic analysis are separately off by default; enabling access requires persisted acknowledgement that TypeSafe AI is a third party, processes state/questions in the United States and through service providers, does not train or fine-tune models on API Input, has no fixed external retention period, and is outside DrainCtl's retention/deletion control. The existing dashboard group is both administrator and operator; machine-account routes never receive provider configuration or investigation access.
- **Q: What evidence and result data are permitted? → A:** Evidence is a deterministic versioned allowlist DTO: stable source kind/ID facts first, then freshness/drain/status, local aggregates, and anonymous fleet aggregates, over source ±30 minutes clipped at snapshot, with deterministic omissions. It contains no canonical host/FQDN/domain/IP/customer identifier. Attempt labels are `source` and `peer_n`; known-channel codes and `custom_channel` replace arbitrary channel text. System One state is at most 8,000 UTF-8 bytes and a request at most 16 KiB; decompressed responses are at most 32 KiB, headers at most 16 KiB, and discarded error bodies at most 4 KiB. Only strict schema-valid typed answers, probabilities, confidence, and safe reason enums are retained—never raw responses or prose.
- **Q: How do attempts start, retry, survive failure, and expire? → A:** A source aggregate has immutable ordered attempts, not a mutable parent. One prospective automatic root attempt may be created per source, without automatic backfill or replay; queued/running duplicates return the existing ID, completed sources create nothing, and only failed or insufficient attempts may create a linked operator retry. After authorization and predecessor lookup, a retry whose deterministic source row has expired returns `404 source_not_found` before evidence assembly, attempt creation, or provider work; stale evidence is never reused. Attempts persist evidence before their single wire transmission, use the fixed lifecycle and limits, resume queued work only after current-config validation, mark pre-crash running work failed/interrupted without re-sending, and retain independently from source data using `AuditDays` anchored to `created_at`. Session-drop source rows are separately deleted in chunks by `detected_at` and `AuditDays`; their detector state persists until permanent host removal.
- **Q: How is a session drop determined and related to drains? → A:** Score existing `TotalSessions` (`Active + Disconnected`) once for each distinct, in-order, fresh accepted report; a successful zero is valid, while nil enumeration is an unknown gap. For each host, preserve 96 local-time slots and one all-hours fallback trained only from that host’s eligible normal observations; session counts MUST NOT be pooled across hosts. A slot is ready after seven distinct eligible local days; the all-hours fallback is ready after at least 20 eligible normal observations spanning at least 24 hours. Preserve time-based decay, lower predictive-tail threshold default `1e-4`, minimum drop defaults of 3 sessions and 30% from the last eligible observation, fixed 2-of-3 consecutive confirmation, and a default 60-minute per-host cooldown. Unknown, stale, or out-of-order reports break confirmation and do not train; candidates, confirmed drops, drain overlap, and one confirmation horizon after drain end do not lower or train the baseline.

## Problem statements

1. Confirmed anomaly data can be distributed across telemetry, drain state, host freshness, and detector records, making a timely, reviewable investigation difficult.
2. Operators need a durable record of bounded evidence considered, typed provider assessment, and why an investigation did not run, without disclosing secrets or sensitive diagnostic content.
3. Sudden session-count losses are operationally important, but nil enumeration, stale telemetry, and report ordering must not be mistaken for zero sessions or an unexplained loss.
4. An optional external-provider integration must fail safely and visibly without altering deterministic detection, current evtspike behavior, or any operational control.

## User Scenarios & Testing

### User Story 1 — Investigate a confirmed anomaly with bounded evidence (Priority: P1)

An authenticated dashboard-group operator reviewing a confirmed evtspike event or eligible unexplained session drop can see a model-assisted, evidence-backed hypothesis while retaining underlying deterministic facts.

**Why this priority**: It supplies operator value without delegating detection or remediation to a model.

**Independent Test**: Persist one confirmed spike and permitted retained context, enable acknowledged provider access and configure its protected credential, request an investigation as a dashboard-group operator, and inspect the durable typed result and provider-bound request capture.

**Acceptance Scenarios**:

1. **Given** a durable confirmed source with valid provider configuration, **when** automatic investigation is enabled or a dashboard-group operator requests investigation, **then** one attempt is created using only the allowlisted evidence DTO.
2. **Given** the same source is requested while its attempt is queued or running, **when** the duplicate request is accepted, **then** it returns the existing attempt ID and creates no concurrent provider call; a completed source creates no new attempt.
3. **Given** the provider returns a strict typed assessment, **when** the result is stored and displayed, **then** it classifies likely cause, impact, evidence sufficiency, and human review as hypotheses; if evidence is insufficient, cause and impact are not presented as conclusions.
4. **Given** an attempt's evidence is produced, **when** its outbound DTO is inspected, **then** it contains no excluded identifier, free-form, raw diagnostic, secret, or arbitrary data category and uses deterministic omission markers.
5. **Given** provider access is disabled, **when** an anomaly is confirmed, **then** deterministic evtspike and session-drop behavior continue and no investigation request or external call is made.

---

### User Story 2 — Review provenance and retry a failed or insufficient investigation (Priority: P1)

An authenticated dashboard-group operator can review a typed result, evidence summary, provenance, and immutable lifecycle; distinguish an unavailable or insufficient result from a conclusion; and deliberately retry only a failed or insufficient attempt.

**Why this priority**: Trust requires a durable, transparent audit trail, especially when external analysis is unavailable or inconclusive.

**Independent Test**: Complete one investigation, induce one provider failure and one evidence-insufficient response, restart the service, and verify immutable history, interrupted-running behavior, and authorized retry behavior.

**Acceptance Scenarios**:

1. **Given** a completed attempt, **when** an authenticated dashboard-group operator opens its history, **then** they see a stable source link, lifecycle timestamps/state, sanitized evidence summary, closed local provider provenance (`provider_profile=typesafe_jev`, `requested_model=jev-latest`, request timing/byte counts, and `validation_outcome` of `accepted` or `insufficient_evidence`), immutable typed result, and retry lineage without excluded data, provider-controlled text, or credentials; source/list/detail views may resolve that link to the canonical host identity held by the deterministic source anomaly.
2. **Given** a result declares evidence insufficient or requires human review, **when** displayed, **then** the dashboard presents that state without presenting cause/impact as a conclusion, recommendation, or automated action.
3. **Given** an attempt failed or has insufficient evidence, **when** an operator requests a retry and its deterministic source row still exists, **then** the system records one linked immutable attempt with a fresh bounded snapshot while preserving earlier attempts unchanged; if that source row has expired, it returns `404 source_not_found` after authorization and predecessor lookup and before evidence assembly, attempt creation, or provider work.
4. **Given** a running attempt is found after restart, **when** recovery runs, **then** it is durably failed with reason `interrupted`, is never automatically re-sent, and its sanitized evidence remains auditable until expiry.
5. **Given** a request lacks existing dashboard-group authorization, **when** it attempts detailed evidence, configuration, request, or retry access, **then** it is denied before evidence or attempt creation and no provider call is made.

---

### User Story 3 — Identify a deterministic sudden session-count drop (Priority: P2)

An operator can review an independently determined lower-tail session-count anomaly through the same bounded investigation lifecycle, while planned drains and unknown context are distinguished from unexplained drops.

**Why this priority**: Sudden loss of sessions is a valuable anomaly source, but it must not weaken or redefine the mature evtspike contract.

**Independent Test**: Feed distinct, in-order reports through the baseline and confirmation sequence for an unexplained drop, drain-associated drop, stale report, out-of-order report, nil enumeration, and successful zero; separately train one host’s all-hours fallback and verify that reports from another host neither contribute to it nor make it ready; verify only the qualifying unexplained drop is provider-eligible.

**Acceptance Scenarios**:

1. **Given** fresh, valid, distinct, in-order reports and a mature lower-tail baseline, **when** `TotalSessions` falls below deterministic expectation and satisfies 2-of-3 confirmation, **then** the system creates a distinct unexplained session-drop anomaly eligible for investigation.
2. **Given** any non-`AllowAll` drain overlaps a confirmed drop or the confirmation horizon after that drain ends, **when** the drop is classified, **then** it is persisted as drain-associated and is not provider-eligible.
3. **Given** enumeration is nil, invalid, unavailable, stale, or out of order, **when** the detector evaluates it, **then** it records unknown, breaks confirmation, and neither trains a baseline nor creates a session-drop anomaly; a successful zero remains a valid numeric observation.
4. **Given** a candidate or confirmed drop, **when** baseline maintenance runs, **then** the candidate/confirmed value does not lower or train the baseline.
5. **Given** an existing upper-tail `SpikePayload` consumer, **when** session-drop detection is introduced, **then** evtspike semantics and contracts remain unchanged and the lower-tail anomaly has its own observable identity and additive dashboard API/SSE type, not a new notification trigger.

---

### User Story 4 — Configure and diagnose a safe optional provider integration (Priority: P2)

A dashboard-group administrator/operator can explicitly configure acknowledged provider access and optional automatic investigation through session-only configuration APIs, observe safe operating state, and diagnose degradation without exposing credentials or causing operational actions.

**Why this priority**: The external integration must be deliberately enabled, observable, and harmless when unavailable.

**Independent Test**: Start with default configuration, store a valid protected credential, acknowledge and enable provider access while automatic analysis remains off, then exercise invalid credentials, timeout, disablement, and config reload while observing status and audit records.

**Acceptance Scenarios**:

1. **Given** a default installation, **when** anomalies occur, **then** provider access and automatic investigation are disabled, no provider request is made, and deterministic detection is unaffected.
2. **Given** an administrator enables acknowledged provider access but leaves automatic investigation disabled, **when** a confirmed anomaly arrives, **then** no automatic investigation runs; an authorized operator may explicitly request one.
3. **Given** provider configuration is read or updated through a session-only API, **when** dashboard, logs, audit records, and telemetry are inspected, **then** only safe fields and `has_credential` are exposed; the DPAPI credential remains write-only with preserve, replace, and clear semantics.
4. **Given** the provider is unreachable, rejects credentials, returns malformed typed output, exceeds a limit, or storage fails, **when** an attempt runs, **then** it has the required durable or structured local failure outcome, no partial result is asserted, and product detection, drains, notifications, and other functions continue.
5. **Given** automatic investigation is enabled and provider access is disabled or configuration becomes invalid, **when** current configuration takes effect or is revalidated before first send, **then** unsent work is cancelled, completed records remain readable, and no new provider call occurs until valid acknowledged configuration is restored.

## Edge Cases

- A durable evtspike event is replayed after restart or redelivered: its durable `event_spikes` ID plus denormalized source facts identifies the source and prevents a duplicate prospective automatic root attempt; canonical host identity remains on the source anomaly rather than being duplicated into the attempt's provider artifacts; no automatic backfill or replay occurs.
- A provider returns prose, an unsupported enum, invalid probabilities/confidence, or asks for more data: raw response/prose is discarded; only a strict schema-valid typed assessment and safe reason enums may persist, and the product takes no action or discloses no more data.
- A provider response `model` is an untrusted bounded field used only to validate response shape, then discarded. It is never persisted, logged, projected, or included in provenance, even when it contains credential, host/IP, URL/path, or prose echo canaries.
- Permitted evidence is absent because retention elapsed: the attempt is persisted as insufficient evidence when SQLite can write; it never substitutes arbitrary historical data or files.
- Evidence exceeds source ±30 minutes, snapshot clipping, state 8,000 UTF-8 bytes, or total request 16 KiB: field-order truncation is deterministic, records omission markers, and does not exceed the egress limit.
- A provider response has headers over 16 KiB, decompressed body over 32 KiB, or an error body over 4 KiB: bounded data is discarded and the attempt fails safely without retaining raw content.
- Configuration changes while queued: queued work may resume only after current configuration validation immediately before first send; disabling provider access cancels unsent work and a single request never mixes configuration.
- SQLite storage fails: no egress occurs. A structured local diagnostic is emitted; an attempt failure becomes durable only if SQLite can write.
- Fleet correlation lacks enough eligible fresh hosts: it is represented as unavailable/insufficient, never no correlation.
- A session count rises, remains stable, lacks seven eligible-day slot maturity, is within threshold, or fails the 2-of-3 confirmation: it creates no lower-tail anomaly.
- Pre-upgrade freshness, drain, or status context is unavailable: it remains unavailable; this feature does not introduce global historical context merely to backfill investigation evidence.

## Requirements

### Investigation lifecycle and provider boundary

- **FR-001**: The system MUST treat deterministic anomaly detection as independent from model investigation. A provider MUST NOT decide whether an anomaly occurred, alter detector baselines/confirmation, suppress a detector result, or initiate remediation.
- **FR-002**: The sole initial provider profile MUST be `typesafe_jev` at exactly `https://api.typesafe.ai/v1/systemone` over normal TLS 1.2+ trust. The central dashboard/service instance alone MUST perform provider configuration, evidence assembly, calls, and persistence; no custom URL, redirect, userinfo, query, fragment, provider key, or provider call is permitted on an agent.
- **FR-003**: Provider access and automatic investigation MUST both be disabled by default and enabled separately. Enabling provider access MUST persist acknowledgement that TypeSafe AI is a third party; processes state/questions in the United States and through service providers; does not train or fine-tune models on API Input; has no fixed external retention period; and is outside DrainCtl's external-retention/deletion control. `AuditDays` governs local copies only.
- **FR-004**: Existing dashboard-group sessions MUST authorize administrator/operator UI APIs. Machine-account routes MUST receive neither provider configuration nor investigation access. Authorized operators MAY request an eligible durable anomaly and retry only a failed or insufficient attempt; unauthorized work MUST stop before evidence or attempt creation.
- **FR-005**: The system MUST consume durable confirmed evtspike events idempotently using durable `event_spikes` identity plus denormalized source facts. It MUST NOT change existing evtspike detection, notification, payload semantics, or consumers.
- **FR-006**: A source aggregate MUST expose immutable ordered attempts, not a mutable parent lifecycle. Attempt state MUST progress only `queued` → `running` → `completed`, `insufficient_evidence`, or `failed`. Each attempt MUST persist a stable source kind/ID link, initiation type, state, timestamps, sanitized evidence snapshot, immutable result or failure record, and retry predecessor where applicable before its single wire transmission. Only a valid decoded provider response may persist the closed local provenance record; rejected, malformed, failed, and local pre-send insufficient outcomes MUST have no provenance row. The attempt and all provider-bound/provider-derived records MUST NOT duplicate the source anomaly's canonical host identity. Terminal rows MUST be immutable.
- **FR-007**: A successful provider result MUST be strict schema-valid typed answers for exactly these four fixed questions: likely cause (`resource_pressure`, `identity_or_authentication`, `service_or_os_failure`, `network_or_dependency`, `planned_drain_or_maintenance`, `fleet_correlated_event`, `unknown`); impact (`low`, `moderate`, `high`, `critical`, `unknown`); evidence sufficiency (`insufficient`, `partial`, `sufficient`); and human review (`required`, `not_required`). It MAY retain typed probabilities, confidence, and safe reason enums, but MUST retain neither raw responses nor prose. The response `model` MUST be bounded only to validate response shape and then discarded. All classifications MUST be labeled hypotheses; insufficient evidence MUST suppress cause/impact conclusions.
- **FR-008**: The product MUST NOT autonomously run commands, PowerShell, tools, configuration changes, drains, restarts, notifications, or any remediation because of investigation evidence or provider output.

### Evidence protection, storage, and retention

- **FR-009**: Before egress, the central service MUST build a versioned allowlist DTO field by field in this order: stable source kind/ID facts; freshness, drain, and detector status; local aggregates; anonymous fleet aggregates. Its source window MUST be ±30 minutes clipped at evidence snapshot. Attempt labels MUST be `source` and `peer_n`; channels MUST use known-channel codes or `custom_channel`; omissions MUST be deterministic.
- **FR-010**: Provider-bound and provider-derived artifacts—outbound DTOs, persisted sanitized evidence snapshots, results, provenance, and provider error/diagnostic payloads—MUST exclude canonical host/FQDN/domain/IP/customer identifiers, usernames, session IDs, `changed_by`, free text, URLs, paths, generic maps, raw structs, Event Log messages/XML, dumps, credentials, notification secrets, and arbitrary files. Canonical host identity MUST remain on the durable deterministic source anomaly (`event_spikes` already has it; session-drop anomalies MUST have it) and MAY be returned only by authenticated dashboard-group source/list/detail APIs. Provider investigation records link to that source only by stable kind/ID. This restriction MUST NOT remove or alter existing operational telemetry, audit, or dashboard host-identity behavior, and canonical host identity MUST NOT be stored in browser-local durable state.
- **FR-011**: Requests MUST use the mandatory `Authorization: Bearer <credential>` header required by TypeSafe AI. That credential MUST use existing DPAPI write-only `config.json` semantics and MUST NOT be returned by APIs, logged, telemetered, persisted as evidence/audit data, or treated as request-body evidence.
- **FR-012**: Sanitized evidence, investigation lifecycle, immutable results, provenance, and attempt retention MUST be stored in existing SQLite. Attempt retention MUST be independently anchored to attempt `created_at` and bounded by `AuditDays`, without changing evtspike source retention. Session-drop source rows MUST be independently deleted in chunks where `detected_at < now - AuditDays`; session-drop baseline/detector state MUST persist until permanent host removal.
- **FR-013**: The system MUST persist sanitized evidence before its single wire transmission. If storage fails, it MUST make zero egress and emit a structured local diagnostic; it MUST record a durable failure only when SQLite can write.
- **FR-014**: Authenticated dashboard-group operator views MUST distinguish observed evidence, deterministic anomaly facts, provider hypotheses, omitted categories, failure/insufficiency states, and unavailable pre-upgrade context. Source/list/detail views MAY show the canonical host identity held by the deterministic source anomaly, but evidence, result, provenance, and provider diagnostics MUST remain sanitized.
- **FR-015**: System One state MUST be at most 8,000 UTF-8 bytes and total request at most 16 KiB. Request/response headers MUST be at most 16 KiB, decompressed response bodies at most 32 KiB, and discarded error bodies at most 4 KiB.

### Session-drop anomaly detection

- **FR-016**: The central service MUST add a separately testable lower-tail detector that scores existing `TotalSessions` (`Active + Disconnected`) exactly once per distinct, in-order, fresh accepted report. It MUST preserve a distinct representation from the existing upper-tail evtspike `SpikePayload` contract.
- **FR-017**: A successful numeric zero MUST be a valid observation. Nil enumeration, invalid/unavailable/stale telemetry, and out-of-order reports MUST be unknown gaps that break confirmation, do not train, and are never persisted or aggregated as numeric zero.
- **FR-018**: For each host, the detector MUST preserve 96 local-time slots and one all-hours fallback trained only from that host’s eligible normal observations; it MUST NOT pool session counts across hosts. A slot MUST be ready only after seven distinct eligible local days. The all-hours fallback MUST be ready only after at least 20 eligible normal observations spanning at least 24 hours. The detector MUST preserve time-based decay, default lower predictive-tail threshold `1e-4`, default minimum drop of 3 sessions and 30% from last eligible observation, fixed 2-of-3 consecutive confirmation, and default 60-minute per-host cooldown. These limits MUST be validated configuration.
- **FR-019**: Candidate and confirmed drops, any non-`AllowAll` drain overlap, and one confirmation horizon after drain end MUST neither lower nor train the baseline. Confirmed drops MUST be persisted with their canonical registered-host identity as `unexplained`, `drain_associated`, or `unknown_context`; only `unexplained` is provider-eligible.
- **FR-020**: An unexplained confirmed session-drop anomaly MUST use the same durable, opt-in investigation lifecycle as evtspike. It MUST add distinct authenticated dashboard-group source/list/detail API/SSE types that can identify the affected registered host, and MUST NOT add a notification trigger. The detector MUST function when investigation is disabled or unavailable.

### Configuration, observability, compatibility, and degraded operation

- **FR-021**: Session-only configuration APIs MUST expose only safe provider fields and `has_credential`. Every provider-settings `PUT` MUST include an explicit credential command of `preserve`, `replace`, or `clear`; omission is invalid and `preserve` is never implicit. Credentials MUST retain existing DPAPI write-only semantics. Unknown provider configuration fields MUST round-trip only where supported; an older version that rewrites configuration MAY drop them.
- **FR-022**: The diagnostic surface MUST expose disabled, configured/ready, automatically enabled, degraded, or failing provider states; attempt counts; and safe failure reasons, without credentials, raw evidence, or excluded data.
- **FR-023**: The system MUST use one worker, at most 100 nonterminal attempts, 10 requests/minute with burst 2, and a 30-second request timeout. Each attempt MUST make at most one wire transmission; timeout, rate-limit, network, and upstream failures are terminal failed attempts. Only an explicit linked operator retry may transmit again.
- **FR-024**: Immediately before first send, the worker MUST validate current configuration. Queued rows MAY resume only after that validation; provider disablement MUST cancel unsent work. Pre-crash running work MUST become failed with `interrupted` and MUST NOT be automatically re-sent.
- **FR-025**: Provider unavailability, authentication failure, malformed output, bounded-request failure, timeout, or storage failure MUST NOT prevent anomaly detection, evtspike notifications, drain operations, freshness evaluation, or normal dashboard use.
- **FR-026**: Existing dashboard consumers and evtspike clients that do not recognize investigation/session-drop additions MUST continue to function. Additions MUST NOT require existing `SpikePayload` consumers to interpret a lower-tail anomaly.
- **FR-027**: Upgrade and rollback MUST preserve pre-existing known configuration/data, audit, telemetry, and source records. An older version MAY ignore new investigation records and MAY drop unknown provider fields if it rewrites configuration. After re-upgrade, provider access MUST remain disabled until those fields and the required acknowledgement are restored; no global historical freshness/status data is added to backfill evidence.

### Key Entities

- **Investigation Source Aggregate**: An immutable stable-kind/ID link to a deterministic evtspike source—its durable `event_spikes` ID plus denormalized source facts—or to a distinct deterministic session-drop source; the linked durable deterministic source anomaly, not the aggregate or its attempts, retains canonical host identity. It exposes ordered immutable attempts without a mutable parent lifecycle.
- **Sanitized Evidence Snapshot**: A bounded versioned allowlist DTO for one attempt, ordered stable source kind/ID facts through anonymous fleet aggregates, with deterministic unavailable/omitted markers and no canonical host/FQDN/domain/IP/customer identifier.
- **Investigation Attempt**: A durable `queued`, `running`, `completed`, `insufficient_evidence`, or `failed` attempt linked to a source only by stable kind/ID and optional retry predecessor. Terminal attempts are immutable.
- **Investigation Result**: Immutable strict typed provider answers, typed probabilities/confidence, and safe reason enums; explicitly a hypothesis with no raw response, prose, or canonical host/FQDN/domain/IP/customer identifier.
- **Provider Provenance**: Closed local provenance only: `provider_profile=typesafe_jev`, `requested_model=jev-latest`, request timing/byte counts, and `validation_outcome=accepted|insufficient_evidence`. The provider response `model` is used only as untrusted bounded response-shape input and is discarded; it is never persisted, logged, projected, or included in provenance.
- **Session-Drop Anomaly**: A distinct deterministic lower-tail condition with the canonical registered-host identity, source facts, baseline/confirmation context, freshness state, and one of `unexplained`, `drain_associated`, or `unknown_context`.

## Failure modes and required behavior

| Failure | Required behavior |
|---|---|
| Provider access or automatic investigation disabled | Make no automatic external request; explicit request is available only to authorized dashboard-group operators when acknowledged provider access is configured. Disabling access cancels unsent work. |
| Unauthorized UI or machine-account route | Deny before evidence/attempt creation; disclose no provider configuration or investigation data and make no provider call. |
| Provider endpoint, redirect, or transport differs from `typesafe_jev` | Reject configuration/request locally; make no external request. |
| Provider unreachable, timeout, rate limit, or upstream/network failure | Persist terminal failed attempt when SQLite can write; make no autonomous HTTP retry; preserve all detector/dashboard operation. |
| Provider credentials rejected | Persist safe authentication failure without logging/returning credential; show degraded configuration. |
| Provider returns malformed, oversized, prose, or nonconforming result | Reject/discard it, persist failed attempt when possible, and display no partial classification as fact. |
| Evidence cannot persist or SQLite is unavailable | Make zero provider egress; emit structured local diagnostic and make attempt failure durable only if SQLite is writable; keep detection operating. |
| Required retained evidence expired/missing | Persist insufficient-evidence attempt when possible; never retrieve arbitrary files or unbounded history. |
| Retry source expired | After authorization and predecessor lookup, return `404 source_not_found` before evidence assembly, attempt creation, or provider work; do not reuse stale evidence. |
| Duplicate source request/delivery | Queued/running duplicate returns existing ID; completed source creates nothing; only failed/insufficient source may create a linked retry. |
| Running work at crash/restart | Mark failed as `interrupted`; never automatically re-send. |
| Session enumeration/freshness/order unknown | Record unknown, not zero; break confirmation; do not train or create unexplained session-drop anomaly. |
| Drain overlaps drop or post-drain horizon | Persist `drain_associated`; do not train/lower baseline or make provider eligible. |
| Session-drop source retention boundary reached | Delete session-drop source rows independently in chunks where `detected_at < now - AuditDays`; baseline/detector state remains until permanent host removal. |

## Constitution Alignment

### Operator Surface Impact

- **Affected surfaces**: Central Windows service/dashboard, existing `config.json` administration flow, authenticated dashboard investigation/status/source/list/detail views and APIs, existing SQLite audit/telemetry retention, existing evtspike durable-event path, and additive lower-tail session-drop API/SSE. Agents, root package, CLI, PowerShell, installer, and notification behavior remain unchanged.
- **Public behavior changes**: Additive dashboard-group investigation visibility/request/retry behavior and authenticated source/list/detail identification of the affected registered host; session-only safe configuration for optional acknowledged `typesafe_jev` access and automatic investigation; and a distinct lower-tail session-drop anomaly. Existing evtspike payload/notification contracts and operational telemetry/audit/dashboard host-identity behavior remain unchanged.
- **Compatibility / migration**: Provider access is off by default. Existing installations need no provider setup to retain current behavior. New records are additive and retained by `AuditDays`. On rollback, old versions preserve pre-existing known configuration/data but may drop unknown provider fields if they rewrite config; after re-upgrade, provider remains disabled until configuration and acknowledgement are restored.

### Quality and Observability Impact

- **Required tests**: Unit tests for allowlist construction/bounds, typed-result validation, provider-model echo discard canaries (credential, host/IP, URL/path, and prose), idempotency/lifecycle including missing-source retry, lower-tail baseline/confirmation including per-host all-hours fallback readiness and isolation, zero-vs-nil handling, stale/order handling, drain correlation, and session-drop source retention; integration tests for explicit credential-command omission rejection, DPAPI/session-only configuration, SQLite lifecycle/retention/restart, provider failures/limits, dashboard authorization, authenticated source/list/detail host identification, and evtspike/session-drop handoff; boundary contracts proving canonical host/FQDN/domain/IP/customer identifiers and all other excluded data cannot leave in evidence/body or persist in sanitized evidence, provider results/provenance, or provider diagnostics, while validating mandatory Bearer-header handling and closed local provenance.
- **Operational signals**: Authenticated lifecycle/history and provider status, durable audit records, sanitized evidence summaries, safe failure reasons, stable source links, and structured non-secret diagnostics. Existing operational telemetry/audit/dashboard host identity remains unchanged.
- **Configuration / data impact**: Additive provider fields use existing protected-secret flow; additive SQLite attempts are server-owned and retained independently from `created_at` by `AuditDays`. Canonical host identity remains on durable deterministic source anomalies, never browser-local durable state; no browser-local authority or agent provider configuration is introduced.

## Success Criteria

### Measurable Outcomes

- **SC-001**: A boundary capture verifies that every request sends the mandatory `Authorization: Bearer <credential>` header, while its request body/evidence contains zero instances of canonical host/FQDN/domain/IP/customer identifiers, usernames, session IDs, `changed_by`, free text, URLs, paths, generic maps, raw structs, Event Log messages/XML, crash-dump bytes, credentials, notification secrets, or arbitrary file content. The same identifier exclusions hold for persisted sanitized evidence, provider results/provenance, and provider error/diagnostic payloads. Provider-model echo canaries for credential, host/IP, URL/path, and prose validate response shape only and are discarded from all persistence, logging, projection, and provenance. Header credentials are validated/redacted separately and are never body/evidence data, logs, telemetry, or persisted audit content.
- **SC-002**: With default configuration, 100% of confirmed anomalies produce zero provider requests while deterministic evtspike and session-drop detection remain observable.
- **SC-003**: In an idempotency test that redelivers the same durable spike ten times, at most one prospective automatic root attempt and one provider request are created for that source; queued/running duplicates return its ID and completed delivery creates nothing.
- **SC-004**: In a restart test, 100% of terminal attempts retain immutable evidence summary/source link and any applicable immutable result/provenance until attempt `AuditDays` expiry, while pre-crash running attempts become `failed/interrupted` with zero automatic re-sends. A failed/insufficient retry whose session-drop source is expired returns `404 source_not_found` before evidence, new attempt, or provider work.
- **SC-005**: A lower-tail matrix of valid confirmed unexplained drop, drain overlap/post-drain horizon, stale host, out-of-order report, nil enumeration, and successful zero creates an investigation-eligible anomaly only for the valid confirmed unexplained drop; its authenticated dashboard-group source/list/detail view identifies the affected registered host without placing that identity in provider artifacts.
- **SC-006**: A failure matrix covering timeout, authentication rejection, malformed/oversized response, storage failure, and disablement records the required safe durable or local diagnostic outcome and causes zero autonomous drains, restarts, notifications, commands, configuration changes, or remediation actions.
- **SC-007**: Authorization tests demonstrate that unauthorized or machine-account detailed-view, configuration, request, and retry calls result in zero evidence disclosure, zero attempt creation, and zero provider requests.
- **SC-008**: Compatibility tests demonstrate that existing evtspike consumers retain current payload semantics and notification behavior when both investigation and session-drop features are enabled.

## Assumptions and explicit non-goals

- TypeSafe AI System One at the fixed `typesafe_jev` endpoint accepts bounded structured evidence and returns the required typed classifications. Per its privacy policy, TypeSafe AI does not train or fine-tune models on API Input, may process Input in the United States or through service providers, and gives no fixed external retention period. Its output may be unavailable or insufficient and is never authoritative.
- The existing dashboard group supplies both administrator and operator authorization for this specification; machine accounts are excluded from provider configuration and investigation access.
- `AuditDays` is authoritative only for local investigation copies. DrainCtl cannot enforce a third party's retention/deletion of data sent to TypeSafe AI.
- Existing telemetry retains aggregate, non-sensitive context needed for permitted evidence. Its existing operational telemetry, audit, and dashboard host-identity behavior remains unchanged. Missing retained or pre-upgrade context remains unavailable rather than being reconstructed from unbounded sources or new global history.
- This feature does not change evtspike detector semantics, `SpikePayload`, notification rules, channel subscriptions, or baseline behavior.
- This feature does not put provider credentials/calls on agents, add a notification trigger for session drops, add autonomous remediation, incident closure, commands, PowerShell execution, tools, configuration changes, drains, restarts, notifications, or provider-driven human-action recommendations.
- This feature does not send raw event data, per-session data, canonical host/FQDN/domain/IP/customer identifiers, free text, URLs/paths, generic/raw structures, dumps, arbitrary files, credentials, notification secrets, or customer URLs as evidence to a provider; it retains canonical host identity only on durable deterministic source anomalies and exposes it only through authenticated dashboard-group source/list/detail APIs, never browser-local durable state.
- This feature does not create a general-purpose chat interface, browser-local investigation store, provider fallback that changes egress, model-driven anomaly detector, automatic backfill/replay, or global historical freshness/status store for backfilled evidence.
