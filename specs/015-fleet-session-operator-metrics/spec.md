# Feature Specification: Fleet Session Operator Metrics

**Feature Branch**: `015-fleet-session-operator-metrics`
**Created**: 2026-09-29
**Status**: Implemented
**Input**: Replace the Overview Sessions charts' host-summary percentile story with true session-weighted fleet workload metrics: Fleet Session P95, Fleet Session AVG, and Sessions at or above 5% / 20% CPU. Reuse the current-session engine while keeping User Session History identity separate.

## Problem and Scope

The Overview Sessions charts currently compare the highest participating host's session P95 with the median participating host's session P95. Those values are mathematically valid host-distribution statistics, but they do not answer the operator's primary questions:

1. How heavy is workload for sessions near the fleet's bad end?
2. What is the average session workload across the fleet?
3. Is CPU activity broad or concentrated in a small number of sessions?

A host with three measured sessions currently has the same influence as a host with one hundred. The label `Typical host P95` can also be mistaken for a typical user or session even though it is a median of host-level tail values.

The current-session engine supplies complete bounded host snapshots containing nullable per-session CPU and working-set measurements. This feature derives anonymous, session-weighted, mergeable historical aggregates from those snapshots and changes only the Session CPU, Session Memory, and CPU-active Sessions Overview charts. Sessions Trend and Utilization retain their existing semantics.

This feature does not add per-user performance history. Usernames, domains, SIDs, session IDs, client details, process details, and User Session History episode identifiers MUST NOT enter retained session workload metrics. A future User Session History surface may navigate to host or fleet charts by host and time, but it may not claim that anonymous host/fleet telemetry belongs to an identified user.

## Metric Contract

### Eligible session observation

An eligible observation is one real user-session row from an accepted successful current-session snapshot with a valid value for the metric being calculated. Existing WTS selection rules continue to exclude listener, service, and other pseudo-sessions. Connected, active, idle, and disconnected real user sessions are eligible because disconnected sessions can retain memory and consume CPU.

Eligibility is metric-specific:

- A session with valid CPU and missing memory contributes only to CPU metrics.
- A session with valid memory and missing CPU contributes only to memory metrics.
- Valid zero CPU and zero working-set values remain observations.
- Missing, invalid, or unavailable values are excluded rather than converted to zero.
- Identity visibility does not affect anonymous metric eligibility.

### CPU normalization

Session CPU MUST be comparable across hosts with different logical processor counts. The value used by these charts is percentage of total host CPU capacity in the inclusive range `0..100`. If the underlying Windows counter represents aggregate logical-processor percentage, it MUST be divided by the host's validated logical processor count before thresholding or aggregation.

Examples:

- One fully consumed logical processor on an eight-processor host contributes `12.5%`.
- Four fully consumed logical processors on an eight-processor host contribute `50%`.
- A zero observation remains `0%`.

### Base fleet sample

For each base telemetry interval and selected host set:

- `Fleet Session CPU AVG` is the arithmetic mean of all eligible normalized session CPU observations.
- `Fleet Session CPU P95` is the numeric 95th percentile of the same eligible CPU observations.
- `Sessions ≥5% CPU` is the count of eligible CPU observations greater than or equal to `5%`.
- `Sessions ≥20% CPU` is the count of eligible CPU observations greater than or equal to `20%`.
- `Fleet Session Memory AVG` is the arithmetic mean of all eligible session working-set observations.
- `Fleet Session Memory P95` is the numeric 95th percentile of the same eligible memory observations.

Each host contributes at most one successful snapshot to a base interval. If multiple successful snapshots arrive for one host in that interval, the newest accepted snapshot replaces the earlier contribution; reporting frequency MUST NOT give a host extra weight.

### Historical rollups

Retained data MUST preserve sufficient anonymous aggregates to merge host and time buckets without averaging host averages or averaging host percentiles.

For every retained bucket:

- AVG is calculated as merged sum divided by merged observation count.
- P95 is calculated from a merged anonymous distribution of the eligible session observations.
- CPU threshold counts represent the average concurrent count across the base samples in the retained bucket; tooltips also expose the maximum concurrent count when it differs.
- Host and session coverage are retained alongside metric values.

The distribution representation MAY be approximate, but its documented error MUST be bounded to:

- CPU P95: no more than `0.5` percentage point absolute error.
- Memory P95: no more than `2%` relative error.

Raw per-session historical values and session identifiers MUST NOT be retained to satisfy percentile calculation.

### Host filtering

The existing Overview host filter applies to all new metrics. Selecting hosts recomputes AVG, P95, counts, percentages, and coverage from only those hosts' anonymous aggregates. No selected hosts continues to mean all registered hosts.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Understand fleet session workload (Priority: P1)

An operator opens Overview → Sessions and sees a session-weighted fleet CPU and memory story rather than a comparison of host-level percentile summaries.

**Why this priority**: This is the primary correction. The charts must answer questions about measured sessions across the fleet, not about the median server.

**Independent Test**: Supply deterministic snapshots from hosts with unequal session counts and verify the displayed AVG and P95 equal the pooled session distribution rather than an average or percentile of host summaries.

**Acceptance Scenarios**:

1. **Given** host A has 100 eligible CPU observations at `5%` and host B has two at `80%`, **when** the fleet sample is calculated, **then** CPU AVG is approximately `6.47%`, not `42.5%`.
2. **Given** a deterministic set of eligible session measurements across several hosts, **when** the Session CPU and Session Memory charts render, **then** their P95 values match the pooled fleet distributions within the specified error bounds.
3. **Given** a session has valid CPU and missing memory, **when** a fleet sample is calculated, **then** it contributes to the CPU denominator but not the memory denominator.
4. **Given** valid zero CPU observations, **when** AVG and P95 are calculated, **then** those zeroes remain part of the distribution.
5. **Given** disconnected real user sessions consuming resources, **when** a fleet sample is calculated, **then** their valid measurements are included.

---

### User Story 2 — Distinguish broad workload from a heavy tail (Priority: P1)

An operator compares Fleet Session P95 with Fleet Session AVG and the CPU-active session counts to determine whether pressure is widespread or concentrated.

**Why this priority**: P95 alone establishes severity but not blast radius. AVG and threshold counts explain whether the tail represents a few sessions or a broad workload shift.

**Independent Test**: Feed two scenarios with the same CPU P95 but different session counts and averages; verify that the chart pair and CPU-active counts make the scenarios visibly different.

**Acceptance Scenarios**:

1. **Given** a few sessions consume high CPU while most remain near zero, **when** the charts render, **then** P95, AVG, and threshold counts show a concentrated tail.
2. **Given** CPU rises across most measured sessions, **when** the charts render, **then** AVG and the count/rate at or above `5%` rise along with P95.
3. **Given** a CPU-active count point, **when** the operator opens its tooltip, **then** it shows the `≥5%` count and percentage, the `≥20%` count and percentage, and the CPU-observed-session denominator.
4. **Given** a rolled-up bucket containing changing concurrent counts, **when** the tooltip opens, **then** it distinguishes average concurrent count from maximum concurrent count.

---

### User Story 3 — Judge data coverage before trusting a point (Priority: P1)

An operator can see how many hosts and sessions contributed to every fleet point and whether selected hosts were missing, stale, unsupported, or in collection error.

**Why this priority**: A correct statistic over an incomplete cohort can still produce a misleading operational conclusion.

**Independent Test**: Withhold or fail snapshots from selected hosts and verify that the chart shows partial coverage rather than silently presenting the remaining hosts as the whole fleet.

**Acceptance Scenarios**:

1. **Given** 14 of 17 selected hosts contribute valid snapshots, **when** a point is displayed, **then** its tooltip states `14/17 hosts` and the observed CPU and memory session counts.
2. **Given** at least one selected host is omitted, **when** the chart renders, **then** the chart remains usable but visibly marks partial coverage.
3. **Given** no selected host contributes a usable snapshot, **when** the interval is viewed, **then** the chart renders a gap and an explicit unavailable state rather than zero.
4. **Given** a host's newest attempt is a collection error, **when** a base fleet sample is formed, **then** its prior successful snapshot is not silently treated as current and the host is counted as missing/error coverage.
5. **Given** identity visibility is masked or hidden, **when** anonymous measurements remain valid, **then** the metrics continue to aggregate without exposing or depending on identity.

---

### User Story 4 — Preserve truthful history and filters (Priority: P2)

An operator can change the Overview window or selected hosts and receive the same statistical meaning at raw and retained resolutions.

**Why this priority**: A metric whose meaning changes across 5M, 1H, 1D, 3D, 5D, or 30D views cannot support incident comparison.

**Independent Test**: Build deterministic base samples, roll them into every supported tier, and compare the API results with independently merged sums, counts, distributions, threshold counts, and coverage.

**Acceptance Scenarios**:

1. **Given** unequal host session populations, **when** data is rolled up, **then** AVG remains session-observation weighted rather than host weighted.
2. **Given** a selected subset of hosts, **when** Overview refetches, **then** every Session chart and coverage value reflects only that subset.
3. **Given** history recorded before this feature, **when** it is viewed, **then** new fleet-session series show a gap rather than relabeling legacy host P95/P50 data.
4. **Given** a dashboard restart, **when** retained data is queried, **then** the new series and their coverage survive according to normal metric retention.
5. **Given** rapid pan, zoom, or host-filter changes, **when** responses complete out of order, **then** stale responses cannot replace the newest selection.

---

### User Story 5 — Correlate future User Session History safely (Priority: P3)

An authorized operator reviewing a future User Session History episode can navigate to host or fleet telemetry for the episode's time range without creating attributable performance history.

**Why this priority**: Host/time correlation is operationally useful, but permanently joining employee identity to performance telemetry expands privacy scope unnecessarily.

**Independent Test**: Open telemetry from a synthetic history episode and inspect retained metric rows, API responses, logs, and AI evidence to verify that no user/session identity crossed into anonymous workload metrics.

**Acceptance Scenarios**:

1. **Given** a history episode with host and time range, **when** the operator follows a telemetry link, **then** the destination opens that host or fleet interval.
2. **Given** the linked telemetry, **when** it is displayed, **then** it is described as host/fleet conditions during the episode, not as that user's measured performance.
3. **Given** retained workload data, **when** storage and API payloads are inspected, **then** they contain no username, domain, SID, session ID, client, process, or episode identifier.
4. **Given** AI anomaly investigation is enabled, **when** evidence is assembled, **then** User Session History identity remains excluded under the existing provider-boundary contract.

### Edge Cases

- One host reports hundreds of sessions while another reports one; every eligible session observation receives equal statistical weight.
- Hosts report at different phases within the base interval; each host contributes at most its newest successful snapshot once.
- A host reports a successful empty session snapshot; it contributes host coverage and zero observed sessions, not a missing-host error.
- CPU is available but logical processor count is absent or invalid; CPU observations from that snapshot are excluded and coverage identifies the capability problem.
- An old agent has no current-session snapshot capability; it remains an expected host but contributes no observations and is shown as unsupported coverage.
- A host becomes stale or offline between successful snapshots; stale values are not carried forward as current measurements.
- A session transitions between connected and disconnected during a retained bucket; each valid base observation contributes according to its observation time without attempting episode reconstruction.
- A single session remains logged on for many intervals; it contributes once per base interval, intentionally representing session workload over time.
- Snapshot compaction omits optional CPU or memory measurements; denominators and coverage reflect only values actually received.
- CPU normalization produces a fractional value around `5%` or `20%`; threshold comparison uses the normalized unrounded value and display rounding occurs afterward.
- A percentile bucket contains one observation; P95 and AVG both equal that observation.
- Host filtering selects only unsupported or erroring hosts; all workload charts show explicit unavailable state.
- Metric retention deletes old anonymous aggregates independently of User Session History audit retention.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST replace the Overview Session CPU `Peak host P95` / `Typical host P95` pair with `Fleet Session P95` / `Fleet Session AVG`.
- **FR-002**: The system MUST replace the Overview Session Memory `Peak host P95` / `Typical host P95` pair with `Fleet Session P95` / `Fleet Session AVG`.
- **FR-003**: The system MUST calculate fleet AVG from pooled eligible session observations, never from an unweighted average of host averages, medians, or percentiles.
- **FR-004**: The system MUST calculate fleet P95 from a mergeable distribution of pooled eligible session observations, never from host P95/P50 values.
- **FR-005**: Session CPU MUST be normalized to percentage of total host CPU capacity before AVG, P95, or threshold evaluation.
- **FR-006**: The system MUST retain valid zero measurements and MUST exclude missing, invalid, non-finite, or unavailable measurements rather than coercing them to zero.
- **FR-007**: Every host MUST contribute at most one successful snapshot per base interval; the newest accepted successful snapshot wins within that interval.
- **FR-008**: The CPU-active Sessions chart MUST continue to show `≥5% CPU` and `≥20% CPU`, calculated from the same normalized CPU cohort used by CPU AVG and P95.
- **FR-009**: CPU-active tooltips MUST show count, percentage of CPU-observed sessions, observation denominator, and average/max concurrent count semantics at rolled-up resolutions.
- **FR-010**: The system MUST retain anonymous per-host sufficient aggregates—sum, count, mergeable distribution, threshold counts, and coverage—so selected-host queries can be merged without session identity or host-level average-of-averages errors.
- **FR-011**: Historical P95 error MUST remain within `0.5` percentage point for CPU and `2%` relative error for memory at every supported retention tier.
- **FR-012**: The existing Overview host filter MUST apply consistently to Session CPU, Session Memory, CPU-active Sessions, coverage, Sessions Trend, and Utilization.
- **FR-013**: Every workload tooltip MUST show contributing hosts versus expected selected hosts and the metric-specific observed-session count.
- **FR-014**: Partial host coverage MUST remain visible; zero contributing hosts MUST produce a gap/unavailable state, not a numeric zero.
- **FR-015**: Hosts with stale, offline, unsupported, or currently failed session collection MUST NOT silently contribute an older snapshot as current data.
- **FR-016**: A successful empty snapshot MUST count as successful host coverage with zero measured sessions.
- **FR-017**: Identity visibility settings MUST NOT alter valid anonymous workload calculations, but disabled session collection MUST produce unavailable data.
- **FR-018**: Retained workload records and APIs MUST NOT contain username, domain, SID, session ID, station, client data, process data, action data, or User Session History episode identity.
- **FR-019**: Existing host-summary percentile history MUST NOT be relabeled or transformed into the new fleet-session series; new history begins when sufficient aggregates are first collected.
- **FR-020**: Legacy Overview adapters, labels, help text, and tests for `Peak host P95` / `Typical host P95` MUST be removed when the new series ships; no compatibility alias may preserve the misleading chart contract.
- **FR-021**: Session CPU help text MUST state that P95 and AVG are calculated across measured real user sessions, CPU is normalized to host capacity, disconnected sessions are included when measured, and coverage appears in the tooltip.
- **FR-022**: Session Memory help text MUST state that P95 and AVG are calculated across measured real user sessions and include disconnected sessions retaining working set.
- **FR-023**: CPU-active Sessions help text MUST explain that counts measure breadth, percentages use CPU-observed sessions, and disconnected sessions with valid CPU are included.
- **FR-024**: Threshold bands for Session CPU and Session Memory MUST apply to `Fleet Session P95`; the AVG series is context and MUST NOT independently change point severity.
- **FR-025**: The new series MUST use the existing shared Overview window, pan, zoom, host-filter, loading, error, retry, and stale-response protections.
- **FR-026**: Anonymous workload retention MUST follow metric retention, independently of User Session History audit retention and current-session snapshot retention.
- **FR-027**: User Session History integration MAY navigate by host and time but MUST describe the destination as surrounding host/fleet conditions and MUST NOT attribute anonymous metrics to the identified user.
- **FR-028**: The API contract and documentation MUST define base-sample, retained-rollup, normalization, eligibility, missing-data, coverage, and approximation semantics exactly.
- **FR-029**: Rolling upgrade MUST remain additive: old agents continue normal heartbeat behavior, capable hosts contribute data, unsupported hosts appear in coverage, and no old field is interpreted as a new metric.
- **FR-030**: The implementation MUST preserve existing Sessions Trend and Utilization meanings unless separately specified.

### Key Entities

- **Session workload observation**: One valid CPU or memory measurement for a real user session in an accepted successful current-session snapshot. It carries no retained identity.
- **Host session aggregate**: One host's anonymous contribution for one base interval: metric sums, counts, mergeable distributions, CPU threshold counts, snapshot/capability status, and observation coverage.
- **Fleet session bucket**: The merge of eligible host aggregates for one selected host set and time bucket, containing AVG, P95, CPU-active counts/rates, and coverage.
- **Coverage summary**: Expected selected hosts, contributing hosts, successful-empty hosts, unsupported/stale/offline/error hosts, and CPU/memory observation counts.

## Chart Contract

### Session CPU

**Legend**:

- `Fleet Session P95` — solid primary line
- `Fleet Session AVG` — dotted secondary line

**Help text**:

> CPU across measured real user sessions, normalized to each host's total CPU capacity. Fleet Session P95 shows the high-use tail; Fleet Session AVG shows overall session workload. Disconnected sessions are included when they have valid measurements. Hover for measured-session and host coverage.

### Session Memory

**Legend**:

- `Fleet Session P95` — solid primary line
- `Fleet Session AVG` — dotted secondary line

**Help text**:

> Working-set memory across measured real user sessions. Fleet Session P95 shows the high-memory tail; Fleet Session AVG shows the session-weighted fleet average. Disconnected sessions retaining memory are included. Hover for measured-session and host coverage.

### CPU-active Sessions

**Legend**:

- `≥5% CPU`
- `≥20% CPU`

**Help text**:

> Real user sessions consuming at least each share of total host CPU capacity. Counts show workload breadth; tooltips also show percentages of CPU-observed sessions and host coverage. Disconnected sessions with valid CPU measurements are included.

### Required tooltip fields

CPU and memory tooltips MUST include:

- timestamp or retained bucket range
- Fleet Session P95
- Fleet Session AVG
- observed sessions for that metric
- contributing hosts / expected selected hosts
- partial-coverage state and omitted-host category counts when applicable

CPU-active tooltips MUST additionally include:

- `≥5%` count and rate
- `≥20%` count and rate
- average concurrent count for retained buckets
- maximum concurrent count when different

## Constitution Alignment *(mandatory)*

### Operator Surface Impact

- **Affected surfaces**: Windows session performance collection/normalization, dashboard session-snapshot ingestion, SQLite telemetry retention/aggregation, fleet metrics API, Overview Sessions charts, OpenAPI, README, guide, and chronicle.
- **Public behavior changes**: Overview Session CPU and Session Memory labels and semantics change from host-summary percentiles to session-weighted fleet P95/AVG. CPU-active tooltips gain rates, denominators, and coverage. Metrics APIs gain additive anonymous aggregate/coverage series. No CLI, PowerShell, installer, session action, or User Session History identity contract changes.
- **Compatibility / migration**: Additive telemetry schema migration and rolling-agent compatibility. Historical host-summary series are not migrated; new chart history begins at first supported collection. Existing current-session snapshot and User Session History tables retain independent ownership and retention.

### Quality and Observability Impact

- **Required tests**: deterministic pooled-distribution and unequal-host-population tests; CPU normalization boundaries; valid-zero/missing-value tests; one-host-per-interval replacement; merge/rollup accuracy at every tier; selected-host recomputation; partial/zero coverage; old-agent mixed-version behavior; identity-absence/privacy tests; API contract tests; frontend chart/tooltip/empty/error/filter tests; browser smoke across all supported windows.
- **Operational signals**: chart tooltip coverage, explicit partial/unavailable states, bounded structured diagnostics for aggregate persistence/query failures, and metric observation counts. Logs MUST not contain per-session identity or values from aggregate internals.
- **Configuration / data impact**: no new operator setting by default. Add anonymous per-host sufficient aggregate storage under metric retention. Do not retain historical raw session rows or alter `Retention.AuditDays`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: For deterministic fixtures with unequal host session counts, CPU and memory AVG exactly match pooled session-observation arithmetic at every supported tier.
- **SC-002**: CPU P95 differs from the exact pooled percentile by no more than `0.5` percentage point and memory P95 by no more than `2%` at every supported tier.
- **SC-003**: Every non-gap chart point exposes contributing/expected host coverage and metric-specific observed-session count.
- **SC-004**: Selected-host results match an independent merge of only the selected hosts for AVG, P95, counts, rates, and coverage.
- **SC-005**: Old, unsupported, stale, offline, and erroring hosts never silently contribute carried-forward data; injected missing coverage is visible in every tested window.
- **SC-006**: Storage, API, SSE, logs, diagnostics, and AI evidence contain no user/session identity from the new retained workload aggregates.
- **SC-007**: Overview initial retained render remains under two seconds on the documented fleet scale of 16 hosts and approximately 614 daily active users; window/filter changes complete within one second on a LAN client.
- **SC-008**: Operators can distinguish synthetic concentrated-tail and broad-load scenarios with the same P95 by reading AVG and CPU-active counts/rates without opening host detail.

## Assumptions

- The current-session engine is available and supplies bounded complete snapshots with per-session CPU, working set, logical processor count, capability, success/error, and freshness information.
- Current-session snapshots remain the source of observations; User Session History transitions are not required for aggregation.
- Metric history represents session workload over time, so a long-lived session intentionally contributes once per base interval when measured.
- Anonymous sufficient aggregates are acceptable under existing telemetry retention because they cannot be joined back to a session or user.
- Sessions Trend and Utilization already answer population and capacity questions and remain separate from CPU/memory distribution charts.
- Historical data before deployment cannot be reconstructed truthfully from host P95/P50 counters.
- UI implementation follows the existing primary/secondary chart visual grammar and shared Overview interaction model.
