# Feature Specification: Runtime Resilience and Diagnostics (012)

**Feature Branch**: `012-runtime-resilience`
**Created**: 2026-09-26
**Status**: Draft
**Scope**: Windows service recovery, local crash evidence, host freshness transitions, and trustworthy RemoteFX telemetry.

## Evidence boundary

### Confirmed evidence

The following facts are the evidence base for this sprint. They are observations, not explanations.

- A v26.9.17 agent crashed while enumerating sessions. Windows Service Control Manager (SCM) recorded event IDs 7034 and 1067, and the service remained stopped.
- A v26.9.47 evidence capture contained active RemoteFX instances on 15 of 17 observed hosts. Of 1,530 FPS samples, 55 were nonzero; observed quality was 100; an old encoding value was near `2^32`.
- The released MSI service declaration already asks SCM to restart the first and second failures after five seconds, perform no third restart, and reset the failure counter after one day (`installer/LISSTech.DrainCtl.wxs`).
- The dashboard currently derives staleness as three configured heartbeat intervals (`internal/dashboard/server.go`), persists `LastSeen` on reports (`internal/dashboard/store.go`), and emits a `server_update` SSE event for each state update (`internal/dashboard/sse.go`).
- The RemoteFX collector already uses PDH arrays and directional percentile semantics: FPS and frame quality are higher-is-better service floors, while time, loss, and skip metrics are lower-is-better upper tails (`internal/perfmon/collector.go`, `internal/perfmon/snapshot.go`).

### Inferences and design responses

- **[INFERENCE]** The stopped v26.9.17 service was either not installed with the intended SCM recovery configuration, had exhausted its recovery budget, or failed outside a recoverable SCM process-exit path. The evidence does not distinguish these possibilities. This sprint makes the installed recovery policy and next-crash evidence inspectable.
- **[INFERENCE]** The near-`2^32` encoding value is a counter/provider sentinel or outlier rather than a plausible encoding duration. The sprint rejects it at ingestion rather than treating it as latency.
- **[INFERENCE]** A zero FPS or quality value in an otherwise available RemoteFX provider represents no active stream, not degraded video quality. The sprint persists a gap rather than a zero metric.

## Problem statements

1. A service process can terminate during a diagnostic path and remain unavailable without collecting the minimum artifact needed to diagnose the next failure.
2. A dashboard can show a stale host as its last health result until a later refresh, rather than emitting one explicit transition that operators and connected browsers can act on.
3. Optional RemoteFX counters can contain inactive-stream zeros, unavailable values, and invalid/sentinel numbers. Storing them indiscriminately makes charts and percentiles misleading.
4. During a mixed-version rollout, old agents must continue reporting safely and must not be mistaken for agents that support new report semantics.

## User scenarios and acceptance tests

### User Story 1 — Recover a failed service and retain local evidence (P1)

An operator needs the agent to recover from the first two unexpected process failures while retaining the next crash's memory evidence locally for authorized diagnosis.

**Independent test**: Install the service, induce three unexpected `drainctld.exe` process exits within one reset window, and inspect SCM configuration and the dump directory.

**Acceptance scenarios**:

1. **Given** the first or second unexpected service failure in a one-day failure-count window, **when** SCM observes process termination, **then** it starts the service after five seconds.
2. **Given** a third unexpected failure before the reset period elapses, **when** SCM observes it, **then** it does not restart the service automatically.
3. **Given** no failure for one day after the first counted failure, **when** a later unexpected failure occurs, **then** SCM treats it as the first failure of a new window.
4. **Given** `drainctld.exe` terminates unexpectedly and Windows Error Reporting (WER) can write its configured local dump, **when** the fault is handled, **then** WER retains at most one mini dump for that failure and no more than three dumps overall; no product component uploads, sends, or embeds their contents in telemetry, SSE, logs, or notifications.
5. **Given** a deliberate SCM stop, shutdown, uninstall, or upgrade stop, **when** the service exits normally, **then** the recovery policy is not treated as an unexpected failure and no crash dump is required.

### User Story 2 — See one fresh-to-offline and one recovery transition (P1)

An operator monitoring the dashboard needs a host to turn offline exactly after three missed effective heartbeat intervals, receive exactly one transition event, and see it recover immediately on the next accepted report.

**Independent test**: Register a host, set an effective interval, advance a deterministic clock over and under `3 × interval`, and observe health, server view, and SSE transition records.

**Acceptance scenarios**:

1. **Given** a registered host with a last accepted report at `t0`, **when** `now < t0 + 3 × effectiveHeartbeatInterval`, **then** the host remains fresh and retains its reported health status.
2. **Given** that host, **when** `now >= t0 + 3 × effectiveHeartbeatInterval`, **then** its derived state becomes offline and the dashboard emits exactly one `host_offline` SSE transition for that freshness epoch.
3. **Given** an already-offline host with no report, **when** status is polled repeatedly, **then** it remains offline and no duplicate offline SSE transition is emitted.
4. **Given** an offline host, **when** the dashboard accepts a new report, **then** it becomes fresh, its reported health is visible, and the dashboard emits exactly one `host_recovered` SSE transition for that recovery.
5. **Given** a hot-reloaded configured poll interval, **when** the new effective interval takes effect, **then** the freshness worker is woken immediately, its prior wait is reset, and staleness is recalculated from that interval without restarting the dashboard listener.

### User Story 3 — Trust RemoteFX charts in sparse or invalid telemetry (P1)

An operator needs RemoteFX charts to distinguish an inactive stream from poor quality and to prevent invalid PDH values from skewing fleet results.

**Independent test**: Feed a RemoteFX sample set containing valid values, zeros, missing values, NaN, infinities, negative values, and boundary/outlier values through report ingestion and query retained metrics.

**Acceptance scenarios**:

1. **Given** an available RemoteFX provider with FPS or quality equal to zero, **when** metrics are persisted, **then** FPS and quality are omitted for that timestamp so charts render a gap.
2. **Given** a RemoteFX metric that is absent, NaN, infinite, negative, or outside its allowed range, **when** the report is accepted, **then** that metric is dropped in place while other valid metrics in the same report remain eligible for storage.
3. **Given** valid FPS and quality values across sessions, **when** a service P95 is displayed, **then** it is the numeric P5 service floor: 95% of contributing sessions are at or above it; P50 remains the numeric median.
4. **Given** valid encode time, RTT, loss, or skip-rate values across sessions, **when** a P95 is displayed, **then** it is the conventional numeric P95 upper tail; P50 remains the numeric median.

### User Story 4 — Roll out without breaking mixed fleets (P2)

An operator can deploy this release while older agents continue to report and newer agents expose the stronger diagnostics and transition semantics.

**Acceptance scenarios**:

1. **Given** an older agent whose report lacks new optional diagnostics fields, **when** the dashboard receives it, **then** core host state remains usable and unavailable optional fields render as absent data, not zero or failure.
2. **Given** a newer agent reporting valid optional fields to an older dashboard, **when** the dashboard does not understand them, **then** the report remains compatible because additions are optional and unknown JSON fields are ignored.
3. **Given** a service upgraded in place, **when** MSI stops and starts it, **then** installer service control remains the owner of stop/install/start ordering; recovery is not used as an upgrade mechanism.

## Requirements

### Service recovery and crash diagnostics

- **FR-001**: The installer MUST configure the `DrainCtl` own-process LocalSystem service with SCM restart actions: first failure restart after 5 seconds, second failure restart after 5 seconds, third failure no action, and reset period one day.
- **FR-002**: The service runtime MUST allow an unexpected process failure to reach SCM. Panic logging MAY record metadata and a stack, but MUST re-panic or otherwise return a failing process exit; it MUST NOT convert a corrupted runtime condition into an apparently clean service stop.
- **FR-003**: Installation or repair MUST configure WER LocalDumps specifically for `drainctld.exe`, with `DumpType=1` (mini dump), `DumpCount=3`, and a product-local ProgramData `dumps` directory.
- **FR-004**: The dump directory ACL MUST grant full control only to LocalSystem and local Administrators; inherited broad read/write access MUST be removed. The installer/service MUST create the directory before configuring WER and MUST fail the diagnostic setup visibly if either directory creation or ACL hardening fails.
- **FR-005**: Local dumps are sensitive memory artifacts. The product MUST NOT automatically upload, attach, transmit, index into SQLite, serialize into dashboard APIs/SSE, or include dump bytes in logs or notifications. The default scheduled inventory MUST contain only filename, size, and local creation time for an authorized local administrator; it MUST NOT read bytes or calculate hashes. With explicit, approved `DRAINCTL_INCLUDE_CRASH_DUMPS=1`, only the newest dump MAY be hashed and gzip-copied inside the protected `dumps` directory with its hash sidecar; neither artifact may be copied to `diags` or uploaded.
- **FR-006**: Retention MUST be bounded by WER `DumpCount=3`. The product MUST NOT add a competing deletion routine that can race WER; manual deletion by an authorized administrator remains permitted. Seven-day task cleanup MAY delete only task-created opt-in gzip artifacts and hash sidecars, never WER-managed `.dmp` files.
- **FR-007**: A structured service-start log MUST record the effective recovery/dump diagnostic configuration without dump contents or customer-specific paths. Failure to configure crash diagnostics MUST log an actionable error and be independently observable.

### Freshness and SSE

- **FR-008**: The effective heartbeat interval is the currently configured dashboard heartbeat interval; if unset or invalid, it MUST fall back to the product default poll interval. The offline threshold MUST equal exactly `3 × effectiveHeartbeatInterval`. Reloading that interval MUST immediately wake and reset the freshness worker so its next evaluation uses the new interval rather than waiting for the old cadence.
- **FR-009**: Freshness is derived from the server-side accepted-report timestamp, not agent-provided wall time. A host with no accepted report remains `unknown`, not `offline`.
- **FR-010**: The dashboard MUST persist enough freshness-transition state in telemetry SQLite schema v3 to deduplicate a fresh→offline event across status requests and dashboard process restarts. `host_freshness` MUST be keyed by canonical host identity and contain the accepted-report epoch plus a nullable offline-emitted marker. Its transactions MUST prevent an older evaluator from overwriting a newer accepted epoch; offline transitions MUST NOT rewrite `last_seen` or `last_result`.
- **FR-011**: On the first atomic fresh→offline transition for an epoch, the dashboard MUST emit one `host_offline` SSE event. On the first newer accepted report after an offline epoch, it MUST emit one `host_recovered` SSE event. Both events MUST contain canonical host identity, observed timestamp, last accepted-report timestamp, and effective threshold; neither includes report secrets or dump data.
- **FR-012**: Existing `server_update` behavior MUST remain available for live result updates. Transition events supplement it; clients that do not recognize them MUST remain functional.

### RemoteFX validity and directionality

- **FR-013**: RemoteFX collection remains opt-in. Provider absence, unavailable counters, or missing values MUST be represented as missing data and MUST NOT be treated as an error that disables core performance collection.
- **FR-014**: Before percentile calculation, persistence, aggregation, or chart serialization, every RemoteFX number MUST be finite and within these inclusive ranges: FPS `(0, 240]`; quality `(0, 100]`; encode time and RTT `[0, 60000]` ms; loss `[0, 100]` percent; server/network skip rates `[0, 1000000]` per second.
- **FR-015**: FPS and quality values of zero mean inactive/no stream and MUST be omitted. Zero is valid for encode time, RTT, loss, and skip rates. Missing remains a gap in all metrics.
- **FR-016**: Invalid RemoteFX values MUST be dropped individually in place. A report with one invalid RemoteFX field MUST preserve valid core, session, and other valid RemoteFX fields.
- **FR-017**: FPS and quality P95 labels MUST use the numeric P5 service floor. Encode time, RTT, loss, and skip-rate P95 labels MUST use numeric P95. API descriptions and chart labels MUST state this directional meaning.
- **FR-018**: Fleet and retained-window aggregation MUST not synthesize zeros for absent/inactive values. It MUST use only valid contributing observations.

### Rollout, observability, and rollback

- **FR-019**: All schema/API additions for dumps and transition observability MUST be additive and optional. A dashboard MUST accept reports from agents predating this sprint; an old dashboard MUST ignore unknown newer report fields.
- **FR-020**: Operators MUST be able to distinguish recovery-policy configuration failure, WER setup failure, dump creation absence, report rejection, offline transition, recovery transition, and RemoteFX-invalid-field drops through structured logs and/or an administrative diagnostic surface.
- **FR-021**: A release rollback MUST preserve existing config, audit, and telemetry data. Rolling back removes no dumps automatically. If rollout introduced a durable transition-dedup record, older versions MAY ignore it safely; the record must not change old host-health results.

## Failure modes and required behavior

| Failure | Required behavior |
|---|---|
| WER registry write or dump-directory ACL fails during install/repair | Fail the diagnostic-setup action visibly; do not claim crash diagnostics are enabled; leave core service install behavior explicit in MSI logs. |
| WER cannot create a dump because of OS policy, storage, or access | Service recovery remains governed by SCM; log/diagnostic inventory reports the absence without retry-uploading or exposing memory. |
| Dashboard restarts while host is stale | Persistent schema-v3 epoch state prevents duplicate `host_offline` for the same last-report epoch. |
| Report arrives at offline boundary | Accepted report establishes a newer fresh epoch; atomic serialization produces one coherent recovery/update sequence, not both an offline and recovery event for the same epoch. |
| Heartbeat interval is hot-reloaded | The freshness worker is signaled immediately, discards its former wait, and evaluates the new `3 × interval` boundary without listener restart. |
| Counter provider absent or PDH returns no instances | Set RemoteFX unavailable/missing; do not emit zeros. |
| NaN, infinity, negative, or out-of-range RemoteFX field | Drop only the field, increment an invalid-field diagnostic counter/log rate, and preserve remaining valid fields. |
| New dashboard receives old agent report | Preserve current report behavior; optional diagnostics remain absent. |
| Old dashboard receives new agent report | Ignore additive fields under normal JSON compatibility; core report remains accepted. |

## Success criteria

- **SC-001**: An SCM inspection after install reports exactly two five-second restart actions, then no action, with a one-day reset period.
- **SC-002**: Three induced unexpected exits demonstrate two restart attempts and no third automatic restart; no deliberate stop is counted as a crash recovery action.
- **SC-003**: A crashing `drainctld.exe` produces at most three protected local mini dumps, and a network/log/SSE inspection shows zero automatic dump exfiltration paths.
- **SC-004**: For each host freshness epoch, there is at most one `host_offline` SSE event and, following a later accepted report, at most one `host_recovered` SSE event.
- **SC-005**: The offline boundary is exactly three effective heartbeat intervals in deterministic-clock tests, including after a heartbeat interval hot reload.
- **SC-006**: A boundary matrix covering every RemoteFX field rejects NaN, infinities, negatives, and values above its stated maximum; it retains valid neighboring fields and never persists inactive FPS/quality zeroes.
- **SC-007**: Directional percentile tests prove FPS/quality P95 is numeric P5 and all lower-is-better RemoteFX P95s are numeric P95.
- **SC-008**: Mixed-version report tests demonstrate an older report and an additive newer report both preserve core host status.

## Assumptions and explicit non-goals

- The Windows Error Reporting service and LocalDumps policy are available on supported Windows Server installations. If organizational policy disables them, this feature reports the resulting absence; it does not bypass policy.
- Dumps are protected local diagnostics, not a telemetry feature. WER may create them for an unexpected crash; only optional newest-dump gzip/hash handling requires incident-approved local administrator action.
- This sprint does not add automatic crash reporting, remote dump retrieval, a new cloud endpoint, customer-specific host routing, or a customer identifier to any artifact.
- This sprint does not change force-update eligibility rules. Existing compatibility rules, including the v26.9.17 minimum for force update, remain independent of crash diagnostics.
