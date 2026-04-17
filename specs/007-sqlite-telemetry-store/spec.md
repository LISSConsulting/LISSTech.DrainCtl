# Feature Specification: Unified SQLite Telemetry Store

**Feature Branch**: `007-sqlite-telemetry-store`
**Created**: 2026-04-16
**Status**: Draft
**Input**: User description: "Consolidate audit records and per-host metrics history into a single on-disk SQL database with write-ahead logging, downsampled resolution tiers (raw / 5-minute / 1-hour), configurable retention, and a multi-resolution chart that zooms across 5 days of history. Migrate existing audit.jsonl on first start, then retire the in-memory ring buffer and the file-only audit store."

## Problem Statement & Motivation

Today DrainCtl keeps two parallel, short-lived histories:

- **Audit log** — drain-mode changes are appended to `audit.jsonl`, but the whole file is also held in memory (`MemAuditStore`). Scales linearly with uptime; there is no query API, so operators grep JSONL to answer any question.
- **Metrics history** — per-host performance counters are stored in an in-memory ring buffer capped at 100 samples (≈ 25–30 minutes of chart data). The buffer is lost on every service restart.

Operators routinely want to answer questions that the current design cannot answer:

- *"Show me CPU on host RDSH-07 for the last 3 days."*
- *"Who drained RDSH-12 last Tuesday afternoon, and what was the machine doing at the time?"*
- *"Did the farm's utilization trend up or down over the weekend?"*

Rebooting the service today silently erases visibility into the last hour of farm behaviour. This feature replaces both stores with a single durable, SQL-queryable telemetry file that survives restarts, supports 5 days of metrics history, and lets the dashboard zoom smoothly from a 5-day overview down to a 1-hour detail view.

## Clarifications

### Session 2026-04-16

- Q: If the service is stopped when drain mode is changed via CLI (registry write), how should the audit gap be handled? → A: On service start, reconcile current registry state vs. last-known audit state and emit a "drift reconciliation" audit record to fill the gap.
- Q: Are audit records immutable after insertion, or can they be edited/annotated? → A: Fully immutable — audit records cannot be modified or deleted except by the retention job.
- Q: How should operators observe the health of background maintenance jobs (aggregation and retention)? → A: Dashboard widget showing last-run timestamp, duration, and status for each background job (log events still emitted, but a UI surface exists).
- Q: Is portable export of audit records (CSV / JSON) in scope for this feature? → A: Out of scope — operators query the store file directly with SQL for archive; a focused follow-up feature can add export later if operator feedback demands it.
- Q: What does the dashboard display on a fresh install before any samples have been collected? → A: Empty chart with a "Collecting data…" status message; chart populates as samples arrive.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Five-Day Metrics History Survives Restart (Priority: P1)

An RDSH farm operator opens the dashboard and sees performance-counter trends for every host across the last five days, even though the DrainCtl service was restarted twice during that window for patching.

**Why this priority**: This is the headline value of the feature. Without durable metrics history, operators cannot correlate user-reported slowness with the farm's actual state yesterday, last night, or over the weekend. Every other story builds on the persistent store being in place.

**Independent Test**: Install the service, let it collect samples for at least two days, restart the service, then open the dashboard — the chart must still show the pre-restart samples as a continuous line. Can be validated with a scripted service restart in a staging farm.

**Acceptance Scenarios**:

1. **Given** the service has been running for at least five days with N healthy hosts, **When** the operator opens the default dashboard view, **Then** each host's chart shows data points spanning the full five-day window.
2. **Given** metrics exist on disk for a host, **When** the service is stopped, restarted, and the operator refreshes the dashboard, **Then** pre-restart samples are still visible in the chart alongside new post-restart samples with no gap attributable to loss.
3. **Given** the service has been offline for a period, **When** it restarts, **Then** the chart shows a gap only for the offline window — not a loss of prior history.

---

### User Story 2 - Audit Search Across Historical Drain Events (Priority: P1)

A compliance reviewer asks, *"Show every drain-on event across the farm in the last 30 days, grouped by host and operator."* An administrator uses the dashboard (or CLI) and returns an answer in seconds, without scanning log files by hand.

**Why this priority**: Audit durability and searchability are the second half of the feature's value. Without it, the JSONL file remains the only record, grep remains the only tool, and the in-memory copy keeps consuming RAM proportional to uptime.

**Independent Test**: Simulate drain-on/drain-off events across several hosts over several days (including service restarts), then run a time-range audit query and verify every event is returned in chronological order with host, actor, and transition fields intact.

**Acceptance Scenarios**:

1. **Given** drain-mode changes have been recorded by the service, **When** an administrator queries the audit history for a time range, **Then** all matching events are returned with host, timestamp, previous and new state, and the acting principal.
2. **Given** the service has restarted since events were recorded, **When** the same query is run, **Then** pre-restart and post-restart events are returned in a single unified result set.
3. **Given** the operator runs the CLI command that displays audit history, **When** it executes, **Then** the output is the same logical dataset as the dashboard shows — not a stale JSONL snapshot.

---

### User Story 3 - Zoom Across Resolution Tiers Without Losing Fidelity (Priority: P2)

A troubleshooter sees a suspicious bump in CPU on the 5-day overview, drags to zoom the chart to a 30-minute window, and the chart automatically switches to full-resolution samples so the spike is clearly visible.

**Why this priority**: The history is only useful if operators can navigate it. A 5-day chart at raw resolution is too dense to read; an averaged 5-day chart hides the detail that matters. Adaptive resolution turns the same dataset into both a trend view and a detail view.

**Independent Test**: With several days of data present, open the dashboard, zoom incrementally from 5 days → 24 hours → 1 hour → 10 minutes, and verify the chart re-renders with smooth transitions and the visible data resolution increases at each step.

**Acceptance Scenarios**:

1. **Given** a 5-day chart is rendered, **When** the operator zooms in to a window of roughly 24 hours or less, **Then** the chart re-fetches and renders at a higher resolution (≈ 5-minute sampling).
2. **Given** a 24-hour window is rendered, **When** the operator zooms further to under an hour, **Then** the chart shows full-resolution samples within that window.
3. **Given** the operator pans (not zooms) left or right within the same resolution band, **Then** the visible data shifts without flipping resolution tiers.

---

### User Story 4 - Seamless Upgrade From Existing audit.jsonl (Priority: P2)

An operator upgrades an existing DrainCtl installation that has months of accumulated `audit.jsonl`. After the upgrade the dashboard and CLI show the full historical audit trail without any manual import step.

**Why this priority**: The audit record is compliance-relevant for many customers, so upgrading must not appear to "lose" history. Automatic migration removes a class of deployment risk.

**Independent Test**: Stage a pre-upgrade installation with an `audit.jsonl` file containing records spanning several months, install the upgrade, start the service, then verify an audit query returns records dated before the upgrade alongside new post-upgrade records.

**Acceptance Scenarios**:

1. **Given** an existing `audit.jsonl` file in the data directory, **When** the upgraded service starts for the first time, **Then** all records from the JSONL are imported into the audit store before the service begins accepting new events.
2. **Given** the JSONL import has completed successfully, **When** the operator inspects the data directory, **Then** the original file has been renamed to a backup (for example `audit.jsonl.bak`) and no new records are written to the JSONL.
3. **Given** a partial import is interrupted (for example the service is stopped during migration), **When** the service is restarted, **Then** import resumes or re-runs safely without creating duplicates in the audit store.

---

### User Story 5 - Retention Keeps Storage Bounded (Priority: P3)

A farm with 50 hosts runs DrainCtl unattended for a year. The database file does not grow without bound because old samples and old audit events are purged automatically according to retention settings.

**Why this priority**: Operators expect any telemetry system to self-manage storage. Without retention pruning the file grows linearly with time and eventually fills the system drive — a self-inflicted outage.

**Independent Test**: Configure an artificially short retention window, inject synthetic records with timestamps older than the window, trigger the retention job, and verify those records are gone while newer records remain.

**Acceptance Scenarios**:

1. **Given** the configured retention is N days, **When** the retention job runs, **Then** metrics samples older than N days are removed from the store.
2. **Given** audit retention is set separately, **When** the retention job runs, **Then** audit events respect the audit retention setting, independent of the metrics retention setting.
3. **Given** the retention job is running, **When** the dashboard queries data or the service records a new sample, **Then** those operations complete without error (retention does not block ingest or read).

---

### Edge Cases

- **Database file missing or corrupted on startup**: the service must either recover (accepting loss of historical data) or refuse to start with a clear error pointing at the file path — never silently drop into an unwritable state.
- **Disk full during write**: new samples and audit events should be dropped with a logged warning; the service must continue running and recover automatically when space returns.
- **Service restart during a retention or aggregation job**: partial work must not corrupt the store; the job must be safe to re-run on next start.
- **Drain mode changed while the service is stopped**: because the CLI writes the registry directly, a change made during service downtime leaves no live witness. On next start the service MUST detect the divergence (registry drain state differs from the last committed audit state for that host) and write a single reconciliation audit record marking the uncertainty window, so the audit trail shows a change happened even if the exact moment and principal are unrecoverable.
- **Host clock skew**: future-dated samples must still be stored and queryable, flagged in logs but not rejected, because clock skew is the operator's problem to diagnose.
- **Retention shortened at runtime**: on the next retention job, data older than the new (shorter) threshold is purged; operators see a one-time cleanup, not a gradual drift.
- **Fresh install with no prior audit.jsonl**: startup proceeds cleanly and no migration attempt is logged as a warning.
- **Dashboard opened before any samples have been collected**: the chart renders an empty plot with a "Collecting data…" status message and begins populating as samples arrive; it does not surface errors, does not show the registry snapshot as a fake data point, and does not hide the chart.
- **Upgrade path from a version that never wrote audit.jsonl**: no migration attempt is made; the store simply begins empty.
- **Concurrent CLI read while service is writing**: CLI read must succeed without blocking or being blocked by the service.
- **Zoom window straddles a retention boundary**: the chart must degrade gracefully — show the tier that still has data, and indicate that older samples beyond the retention window are not available.
- **Extremely large pre-existing audit.jsonl (for example hundreds of megabytes)**: migration must complete in bounded time without preventing the service from answering new requests indefinitely.

## Requirements *(mandatory)*

### Functional Requirements

#### Durable Storage

- **FR-001**: The system MUST persist every drain-mode change as a discrete audit record containing at minimum: host name, event timestamp, previous drain state, new drain state, and the identity of the principal that caused the change.
- **FR-001a**: On service start, the system MUST compare the current drain state of each monitored host against the most recent committed audit record for that host. When they differ, the system MUST emit a single reconciliation audit record per affected host marking the change as observed after a service-downtime gap (principal unknown — stored as empty string to match the partial index in data-model.md, timestamp window = last-known-good through first-post-start observation). The system MUST also emit a reconciliation record when the registry `LastWriteTime` for a host has advanced past the last audit record's timestamp even if the drain state matches (detects A→B→A oscillations during downtime).
- **FR-001b**: Audit records MUST be immutable after insertion. The system MUST NOT expose any interface (API, CLI, or dashboard) that updates or deletes an individual audit record. The only permitted deletion path is the bulk retention job defined in FR-012, acting uniformly on records older than the configured retention window.
- **FR-002**: The system MUST persist per-host performance-counter samples at the service's configured sampling interval, retaining each sample's host, timestamp, and counter values.
- **FR-003**: Committed records MUST survive service restart, host reboot, and ungraceful termination; the only tolerated loss is data that had not yet been committed at the moment of failure.
- **FR-004**: The storage file MUST live in the existing DrainCtl data directory (`%ProgramData%\LISS Technologies\LISSTech DrainCtl\`) and MUST have the same access control as `config.json` (SYSTEM, local Administrators, and the DrainCtl service account; no broader access).

#### Read While Writing

- **FR-005**: Dashboard and CLI reads MUST be able to execute concurrently with service writes without either side blocking the other for a user-perceptible duration.
- **FR-006**: Background maintenance (aggregation and retention) MUST NOT block ingest, dashboard reads, or CLI queries beyond brief internal transactions.

#### Downsampled Aggregates

- **FR-007**: The system MUST maintain a coarser hourly aggregate of per-host metrics derived from the raw samples, capturing at minimum average, minimum, and maximum for each counter over each hour.
- **FR-008**: Hourly aggregates MUST be produced by a periodic job; the job MUST be idempotent (running it twice must not produce duplicate aggregates).
- **FR-009**: The system MUST expose query access for raw samples, for an intermediate (≈ 5-minute) resolution, and for hourly aggregates, so callers can match resolution to the queried time window.

#### Retention

- **FR-010**: Retention windows MUST be configurable in whole days.
- **FR-011**: Metrics retention MUST support at least the range 1–365 days. Audit retention MUST support a longer range (at least 1–3650 days) to cover compliance needs.
- **FR-012**: A periodic retention job MUST delete records older than the configured retention and reclaim disk space.
- **FR-013**: Retention job failures MUST be logged and MUST NOT crash the service; the next scheduled run MUST retry.

#### Query API

- **FR-014**: The dashboard MUST be able to request metrics for a given host and time range, specifying the desired resolution tier (raw / 5-minute / hourly).
- **FR-015**: When a caller omits the resolution, the system MUST select an appropriate tier automatically based on the requested time span.
- **FR-016**: The audit API MUST support querying by time range, by host, and by actor, with results ordered chronologically.

#### Chart Behaviour

- **FR-017**: The dashboard chart MUST support zoom and pan across the full retained window.
- **FR-018**: As the user zooms in or out, the chart MUST request the appropriate resolution tier so that the data density on screen stays useful (neither too sparse nor so dense that individual samples cannot be distinguished).
- **FR-019**: When the visible window spans data that exists only at a coarser tier (because the raw tier has been purged), the chart MUST render the coarser data rather than showing "no data".
- **FR-019a**: When no samples exist at all for a host in the requested window (e.g., fresh install, newly added host, or a gap predating the earliest retained record), the chart MUST render an empty plot with a visible "Collecting data…" status indicator and MUST NOT synthesize data points from the current registry snapshot.

#### Migration

- **FR-020**: On service start, if a legacy `audit.jsonl` file exists and a migration marker is absent, the system MUST import every record into the audit store before new events begin to be written.
- **FR-021**: The migration MUST be safe to re-run after an interruption: either it resumes, or it produces the same final state as a single run (idempotent).
- **FR-022**: On successful migration, the legacy JSONL MUST be renamed to a `.bak` suffix and left in place so operators can archive or delete it manually.
- **FR-023**: The migration MUST log its progress (records processed, time elapsed) and any skipped/malformed lines.

#### Interface Changes

- **FR-024**: The `drainctl history` CLI command MUST read from the new store, producing the same user-visible output whether executed against the live service or directly against the on-disk store.
- **FR-025**: The in-memory audit cache and the in-memory metrics ring buffer MUST be removed; all reads MUST resolve from the durable store.
- **FR-026**: Existing dashboard and CLI behaviour that currently reads the ephemeral history MUST continue to function without operator-visible regression after the switch-over.

#### Operational Constraints

- **FR-027**: The existing named mutex that serializes `config.json` writes MUST remain independent of the telemetry store's own internal locking; these two serialization mechanisms MUST NOT be conflated.
- **FR-028**: The store file MUST NOT require any runtime dependency beyond what the service already ships with (no additional runtime installer, no system-level database engine).

#### Maintenance Job Observability

- **FR-029**: For each background maintenance job (hourly aggregation, retention pruning, schema/migration work), the service MUST record the last-run timestamp, the duration of the most recent run, the outcome (success / failure / skipped), and — on failure — a short reason string.
- **FR-030**: The dashboard MUST display this last-run status for each job in a dedicated widget so operators can determine at a glance whether maintenance is current, without consulting log files.
- **FR-031**: A maintenance job whose last successful run is older than twice its scheduled interval MUST be rendered as a warning state in the dashboard widget so operators notice silent failures.
- **FR-032**: Log emission (ETW + file log) for maintenance jobs MUST continue in parallel with the new UI surface; the widget reflects the same facts the logs record.

### Key Entities

- **Audit Record** — a single drain-mode transition event. Attributes: timestamp, host, previous state, new state, principal, optional free-text reason. One per change. Immutable after insertion; only the bulk retention job may remove records.
- **Metric Sample** — a single observation of a host's performance counters at a point in time. Attributes: timestamp, host, set of counter values (CPU, memory, session counts, etc.). Written at the service's sampling cadence.
- **Hourly Metric Aggregate** — a roll-up of all Metric Samples for a single host in a single calendar hour. Attributes: hour boundary, host, per-counter (avg, min, max). One per host per hour.
- **Retention Policy** — the operator's configuration for how long each class of record (metrics vs. audit) is kept. Expressed in days.
- **Migration Marker** — a persistent flag indicating that the legacy JSONL has already been imported. Prevents re-migration on subsequent starts.
- **Maintenance Job Run** — the recorded last execution of a named background job (aggregation, retention). Attributes: job name, started-at timestamp, duration, outcome, optional error reason. One row per job; overwritten on each new run.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After running continuously for 5 days with 50 hosts at a 15-second sampling interval, the dashboard's default view shows an unbroken 5-day chart for every host.
- **SC-002**: Opening the dashboard on a cold client renders the default 5-day chart in under 2 seconds over a typical LAN.
- **SC-003**: Zooming from a 5-day view to a 1-hour view updates the rendered data in under 500 milliseconds.
- **SC-004**: Stopping and restarting the service during active sampling loses no more than one sampling interval of data per host.
- **SC-005**: A 30-day audit query against a store containing at least one year of audit history returns results in under 1 second.
- **SC-006**: Disk usage for 5 days of raw metrics plus 1 year of audit retention, on a 50-host farm, stays under 500 MB.
- **SC-007**: Background retention and aggregation jobs cause no measurable interruption to dashboard responsiveness or sample ingest (no visible pause longer than 100 ms).
- **SC-008**: On upgrade from a version with an existing `audit.jsonl` up to 100 MB, migration completes on first service start within 60 seconds and preserves every valid record.
- **SC-009**: The number of storage-related code paths in the service drops relative to today: the file-only audit store, the in-memory audit cache, and the in-memory metrics ring buffer are all removed.
- **SC-010**: A fresh Windows host with no developer tooling installed can run the upgraded service without any extra runtime install step for the storage layer.

## Assumptions

- Typical deployment is an RDSH farm of 10–100 hosts sampled at 15–30 second intervals; the capacity targets in Success Criteria assume this range.
- The dashboard is the primary consumer of query APIs. CLI and future automation reuse the same query surface.
- Retention is expressed per record class (separate value for metrics, separate value for audit) so operators can keep compact metrics history alongside long-lived audit history. The defaults chosen at implementation time should err toward "audit kept long, metrics kept short."
- The legacy JSONL import is one-shot per installation: after the first successful migration it never runs again.
- Clock synchronization across the farm is the operator's responsibility; the store tolerates skew but does not correct it.
- Existing `config.json`-based configuration remains the source of truth for DrainCtl configuration. The new retention settings fit into that same configuration file.
- The dashboard's existing authentication and authorization posture applies unchanged to the new query endpoints; no new auth mechanism is introduced by this feature.
- The service, not the CLI, owns write access to the store. CLI continues to read directly from the store file when the service is unreachable, but does not write.
- The three resolution tiers (raw / ≈ 5-minute / hourly) are sufficient for the UX; further tiers can be added later without changing the query contract.
- Schema migrations beyond the initial import are out of scope for this feature; adding new counters or columns later will be handled by a follow-up.
- Portable export of audit or metrics data to CSV / JSON is out of scope. Operators needing an archive copy can query the store file directly or copy the file itself. A dedicated export UX is deferred to a follow-up feature if operator feedback calls for it.
