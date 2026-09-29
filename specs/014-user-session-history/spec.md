# Feature Specification: User Session History (014)

**Feature Branch**: `014-user-session-history`  
**Created**: 2026-09-28  
**Status**: Draft  
**Input**: An operator needs to answer “Which RDS host was this user on at 8:55 p.m.?” after the user logs off, resets a session, and reconnects successfully elsewhere. The deployment has 16 hosts in groups of 6, 4, 2, 3, and 1; maximum daily active users are about 614, with up to about 70 users per host.

## Problem and Scope

The current dashboard retains host-level session counts, but an operator cannot search a historical user-to-host assignment. Once a user has logged off and entered a new session, current WTS enumeration or Connection Broker state cannot reliably identify the earlier host. Manual reconstruction requires gateway, broker, DNS, and RDS host log searches.

This feature records host-observed session transitions and presents a time-based user-to-host timeline. The Windows service receives live WTS session-change notifications. Capture and dashboard delivery do not run on the user’s login-script path, and slow or failed monitoring work must never be a prerequisite for completing an RDS logon. The result is an operational history of **observed** session activity; it must disclose gaps rather than claim complete coverage during service stoppage or loss.

The feature is separate from the 013 session-count-drop detector and AI investigation pipeline. Usernames, SIDs, session IDs, client details, and this timeline MUST NOT enter investigation evidence, attempts, reports, provider requests, or provider diagnostics. ProxyLauncher instrumentation, UDP, direct Connection Broker database reads, and event-log replay are outside the initial scope.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Find the host at an incident time (Priority: P1)

An authenticated dashboard operator enters an account and an approximate incident time. The dashboard shows the host and session observed around that time, plus the nearby logoff and subsequent logon, even if the user is now on another host.

**Why this priority**: This directly replaces the manual gateway-to-broker-to-host investigation.

**Independent Test**: Observe a user on host A, log off, create a new session on host B, then search the earlier incident time after both sessions have ended. The result identifies A for the incident and B for the later session.

**Acceptance Scenarios**:

1. **Given** a recorded logon on RDS7 at 20:41 and logoff at 20:57, **when** an operator searches that user at 20:55, **then** RDS7 and its session ID appear as the observed placement spanning the incident time.
2. **Given** the same user logs on to RDS3 at 21:02, **when** the operator reviews the timeline, **then** the RDS7 and RDS3 episodes remain separate with their own event times and session IDs.
3. **Given** a reconnect to an existing session, **when** the timeline is viewed, **then** the reconnect is attributed to the existing host/session and is not misrepresented as a new session on another host.
4. **Given** no observation covers the requested time, **when** the operator searches, **then** the UI says “no observed placement” and shows any known coverage gap; it does not assert that the user was absent.
5. **Given** identically named accounts in different domains, **when** the operator searches, **then** the results preserve and distinguish the domain-qualified identities rather than silently merge them.

---

### User Story 2 — Capture without delaying logon (Priority: P1)

DrainCtl observes session logon, remote connect, remote disconnect, and logoff transitions on each agent-mode RDS host. A dashboard outage, a slow dashboard report, or a busy DrainCtl check does not make user logon depend on recording or delivery.

**Why this priority**: The monitoring feature cannot become part of the login critical path or make an already troubled host harder to use.

**Independent Test**: Artificially block dashboard reporting and a regular DrainCtl check while initiating user sessions. Session changes are accepted or a coverage gap is reported, and users can complete logon without waiting for any DrainCtl database or network operation.

**Acceptance Scenarios**:

1. **Given** dashboard reporting is delayed or never responds, **when** a user logs on, **then** the session-change intake performs no synchronous network request, database write, or user-identity lookup on the service-control callback path.
2. **Given** the periodic check loop is stalled, **when** Windows delivers a session-change control, **then** the service-control handler returns promptly without waiting for that loop’s report or check to finish.
3. **Given** a burst of 70 logons on one host within one minute, **when** notifications arrive, **then** DrainCtl maintains bounded capture resources and remains responsive to stop and shutdown controls.
4. **Given** a logoff arrives after the session can no longer be queried, **when** the event is processed, **then** DrainCtl uses its captured session identity when available; it never attaches a different user merely because a numeric session ID was reused.
5. **Given** the service runs in dashboard-only mode, **when** users log on to that management server, **then** no RDS-host session-history capture is started.

---

### User Story 3 — Preserve and diagnose history through delivery failures (Priority: P2)

A captured transition is retained locally until the dashboard acknowledges it. The operator can see whether a host’s session-history feed is current, delayed, or contains a known gap.

**Why this priority**: A silent missing event would lead an operator to investigate the wrong host.

**Independent Test**: Capture transitions while the dashboard is unavailable, restart the agent, restore the dashboard, and inspect both the eventual timeline and feed-health indicators.

**Acceptance Scenarios**:

1. **Given** a transition is durably captured locally and the dashboard is unavailable, **when** delivery resumes, **then** the dashboard receives it with its original occurrence time and displays it in chronological context.
2. **Given** the dashboard accepts an event but its acknowledgement is lost, **when** the agent retries, **then** only one durable central event exists.
3. **Given** events arrive out of order, **when** the timeline is queried, **then** occurrence order is used for display and receipt time remains available for diagnosing delay.
4. **Given** the service was stopped, its bounded capture queue overflowed, or a transition could not be identified, **when** the operator queries an affected interval, **then** the affected host/interval is marked as incomplete or uncertain; the product makes no claim to have reconstructed events it did not observe.
5. **Given** DrainCtl restarts with users already connected, **when** it resumes, **then** it reconciles current WTS sessions, labels them as startup observations, and does not invent original logon timestamps or missing logoff events.
6. **Given** a host is permanently removed from the dashboard, **when** an old agent retries, **then** its session-event intake follows the existing tombstone policy and does not resurrect the host.

---

### User Story 4 — Control access and retention (Priority: P2)

Only an authenticated dashboard-group operator can search identifiable user-session history. Agents may submit records only for their authenticated host, and retained records expire under a documented policy.

**Why this priority**: The timeline contains employee activity and should have the same operator boundary as other sensitive dashboard details.

**Independent Test**: Exercise valid and invalid dashboard sessions, another host’s machine identity, retention expiry, and the AI evidence assembly path.

**Acceptance Scenarios**:

1. **Given** an unauthenticated, expired, or non-dashboard-group browser session, **when** it requests user history, **then** the request is denied before a user search or record disclosure.
2. **Given** an authenticated machine account for host A, **when** it submits an event claiming host B, **then** the dashboard rejects it.
3. **Given** an event is older than the configured history retention, **when** maintenance runs, **then** its central record and expired local delivery copy are removed without changing unrelated audit or metrics retention.
4. **Given** the AI investigation feature is enabled, **when** evidence and provider requests are assembled, **then** no user-session-history field or identity is included.

### Edge Cases

- WTS notification arrives before username/domain information is queryable; identity resolution is retried away from the service-control path within a bounded interval and unresolved events remain explicitly incomplete.
- Logoff follows service restart, session ID reuse, host reboot, or rapid reset; episode correlation must use host and a local session-instance identity, not a bare session ID across time.
- Remote reconnect to a disconnected session differs from a new logon; both must be recorded without duplicating an episode.
- Multiple simultaneous sessions for one user and two hosts at the same time remain visible as separate observations; the UI cannot choose one without evidence.
- Host and dashboard clocks differ; preserve occurrence and receipt times and expose material clock skew rather than silently reorder by receipt time.
- A queue or database failure prevents durable capture; an explicit gap/health signal is retained or reported when possible. If the process is entirely unresponsive, the UI shows stale feed state rather than implying complete coverage.
- A dashboard upgrade precedes some agents or vice versa; older heartbeats and existing APIs continue to work. Unknown new event types are rejected or ignored without corrupting history.
- No session-history observation is allowed to alter session-count-drop scoring, alert triggers, drain control, RDS broker routing, or login-script behavior.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Agent-mode DrainCtl MUST capture live Windows WTS logon, remote connect, remote disconnect, and logoff notifications for RDS sessions with event type, host, session ID, and UTC occurrence time.
- **FR-002**: The Windows service-control callback MUST return promptly after copying only bounded notification data. It MUST NOT perform WTS identity lookup, SQLite I/O, HTTP, DNS, or wait for the periodic check loop, dashboard, or an unbounded queue. The plan MUST verify notification-data lifetime and safe copying at the Go service-control boundary.
- **FR-003**: Session identity resolution and persistence MUST run independently of user logon, ProxyLauncher, the existing periodic check, and dashboard response processing.
- **FR-004**: Each observed session episode MUST distinguish canonical host, host boot/session-instance context, numeric session ID, domain-qualified account, SID when obtainable, logon/reconnect/disconnect/logoff events, occurrence time, and dashboard acceptance time. Missing fields MUST be represented as unknown, not inferred from another user or host.
- **FR-005**: Session ID reuse, reconnection, multiple concurrent sessions, and startup observations MUST NOT merge unrelated episodes.
- **FR-006**: The agent MUST persist accepted events in a bounded local outbox before treating them as deliverable. Delivery MUST be asynchronous, retried with bounded backoff, and idempotent after an uncertain acknowledgement. A dashboard failure MUST NOT prevent user logon or block the service-control callback.
- **FR-007**: The dashboard MUST accept a bounded batch only from an authenticated, registered, non-excluded machine host matching the claimed host, reusing the existing pinned TLS and SSPI trust boundary. A dashboard-local agent MUST have an equivalent in-process path.
- **FR-008**: Central storage MUST preserve occurrence and receipt times, orderable event identity, per-host source identity, user identity, session episode, and event type. Retries MUST NOT duplicate records.
- **FR-009**: An authenticated dashboard-group operator MUST be able to search by domain-qualified username or SID and incident time, view nearby events across all registered hosts, and identify an observed placement spanning the requested time when the event sequence supports it.
- **FR-010**: Search responses MUST distinguish direct event observations, inferred intervals between observed transitions, startup snapshots, stale feeds, and intervals of unknown coverage. The UI MUST NOT claim a definitive absence or placement across a gap.
- **FR-011**: On agent startup, DrainCtl MUST enumerate current sessions and reconcile them without fabricating past transitions; it MUST record/report a coverage gap for service downtime or lost notifications that cannot be reconstructed.
- **FR-012**: Queue overflow, identity-resolution failure, persistence failure, delivery lag, clock skew, and stale feed MUST have bounded structured diagnostics and operator-visible health state. Health events MUST avoid logging full user-session histories.
- **FR-013**: Session-history data MUST remain separate from `CheckResult` session counts, the 013 session-drop observation inbox, evtspike, and AI investigation evidence, attempts, results, provider traffic, and diagnostics. No user or session identity may cross the AI provider boundary.
- **FR-014**: User-history read APIs MUST enforce the current dashboard-session and dashboard-group authorization boundary before search or disclosure. Machine accounts MUST have no user-history read route.
- **FR-015**: History retention MUST use the existing `Retention.AuditDays` setting (default 365 days) for central records; local acknowledged copies MUST be pruned promptly, and undelivered records MUST be bounded with explicit overflow/gap reporting. Retention MUST not change existing audit or metrics policies.
- **FR-016**: Feature rollout MUST be additive: old agents and dashboards continue their existing heartbeat behavior, and the new endpoint must not alter or overwrite `servers.last_result_json` with sparse event batches.
- **FR-017**: The feature MUST perform no session reset, logoff, routing, drain-mode change, notification trigger, or other user-affecting action.

### Key Entities

- **Session transition**: One host-observed WTS event, with stable local identity, type, occurrence time, session ID, resolved or unresolved user identity, and provenance.
- **Session episode**: The sequence of observations belonging to one user’s session instance on one host; reconnects may extend it, while a new logon creates a separate episode.
- **Delivery outbox entry**: A bounded durable local transition waiting for central acknowledgement, with retry state and an idempotency key.
- **Coverage gap**: A host/time interval or point at which complete observation cannot be established, with cause and detection time.
- **History feed status**: Per-host last captured and last accepted times, backlog/oldest age, and whether coverage is current, delayed, or uncertain.

## Constitution Alignment *(mandatory)*

### Operator Surface Impact

- **Affected surfaces**: Windows service and WTS interop; SQLite telemetry on agent and dashboard; authenticated agent intake and dashboard search APIs; Svelte dashboard; OpenAPI, README, guide, and operational diagnostics.
- **Public behavior changes**: Additive agent event intake, user-history read API, and dashboard search. Existing CLI, PowerShell module, installer, `CheckResult`, `SpikePayload`, session-count detector, and drain commands retain their current behavior.
- **Compatibility / migration**: Additive schema migration and rolling upgrade. Earlier history cannot be reconstructed from current session counts. No new credential, firewall port, or required operator setting is introduced.

### Quality and Observability Impact

- **Required tests**: Session transition/episode unit tests; Windows service-control integration under blocked check/report conditions; WTS notification data-lifetime test; 70-logon-per-minute burst; outbox crash/retry/dedup and mixed-version contract tests; dashboard auth and tombstone tests; retention/gap/clock-skew tests; browser search test. Before merge, run the repository-required Windows `go test ./...`, `just lint`, and pre-commit checks.
- **Operational signals**: Per-host feed freshness, capture-to-acceptance latency, outbox depth/oldest age, dropped or unresolved event count, coverage gaps, and bounded structured errors. Search displays source and gap context.
- **Configuration / data impact**: Additive SQLite tables and retention work under existing `Retention.AuditDays`; no separate config store or hand-maintained release version. Service identity remains LocalSystem; transport reuses the existing machine-account authentication and dashboard certificate pinning.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In an end-to-end two-host reset scenario, an operator can identify the earlier host and later host from the dashboard in under 30 seconds without consulting gateway, broker, DNS, or host logs.
- **SC-002**: During a blocked dashboard/report test, 100% of tested user logons complete without waiting for event delivery; the session-control callback has p95 under 10 ms in the 70-logon-per-minute host burst test. These are acceptance targets, not current measured performance.
- **SC-003**: After durable local capture and a recoverable dashboard outage, 100% of queued test events are eventually represented once centrally, preserving original occurrence time.
- **SC-004**: In a healthy 16-host pilot, capture-to-dashboard visibility reaches p50 within 2 seconds and p95 within 10 seconds, measured from WTS notification to accepted record. Slower deliveries are visible as feed lag.
- **SC-005**: Every injected service downtime, queue overflow, and unrecoverable capture failure in validation produces a visible coverage warning or stale-feed indication; no test query presents an unsupported definitive placement.
- **SC-006**: No user/session identity appears in 013 investigation evidence, persisted attempts/results/provenance, or provider-bound request captures in contract tests.

## Assumptions

- All 16 RDS hosts run the DrainCtl Windows service under LocalSystem and can reach the existing dashboard endpoint when healthy.
- The WTS service notification is the live event source. Windows Event Log replay is not required in this increment; service downtime is represented as a coverage gap plus a current-session startup snapshot.
- The existing dashboard group is the operator authorization boundary; there is no new user-role model.
- The existing `Retention.AuditDays` setting is appropriate for identifiable operational session history. The feature will document that these records are retained locally and centrally under that policy.
- The workload reference is 614 maximum daily active users, groups of 6/4/2/3/1 hosts, and up to roughly 70 users on a host. DAU is not treated as peak concurrency; burst and latency targets must be measured in an MDS-like pilot.
