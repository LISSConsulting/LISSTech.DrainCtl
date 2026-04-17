# Feature Specification: Event Log Anomaly Detection (evtspike)

**Feature Branch**: `006-evtspike-detection`
**Created**: 2026-04-16
**Status**: Draft
**Input**: User description: "Event Log Anomaly Detection (evtspike): Subscribe to 54 Windows Event Log channels on RDSH servers, count events in 10-second buckets, aggregate into 60-second rolling windows, and score against a Bayesian baseline (Gamma-Poisson model with Negative Binomial predictive). Spikes that persist across 2-of-3 windows trigger notifications. Available as both a built-in DrainCtl subsystem (optional, config-gated) and a standalone CLI. Standalone communicates spikes to DrainCtl over named pipe. Baseline state (two floats per channel per time-of-day slot, ~135KB total) persisted to JSON file for warm restart. Time-of-day learning via 96 fifteen-minute slots so morning logon storms don't false-alarm. Robust capped updates prevent spikes from poisoning the baseline. Integration with existing notification pipeline (webhook, ntfy, email) via new 'event_spike' trigger type. POC already validated in feat/evtspike-poc branch."

## Clarifications

### Session 2026-04-16

- Q: How long must the detector observe a given time-of-day slot before that slot's baseline is considered mature enough for alerts based on it? → A: Default one week (≈7 observations per slot), configurable via `config.json`; until maturity the all-hours global baseline is used as fallback.
- Q: What dashboard visibility must MVP ship with? → A: Additive-minimal on the existing dashboard — per-server detector status pill (healthy / training / disabled / error) plus a recent-spikes list for the selected server. No new dashboard construction; no per-channel drilldown, heatmap, or baseline-editing UI in MVP.
- Q: What operations can an administrator perform on the watched-channels list in `config.json`? → A: Additive + suppressive. The 54-channel default list ships in code and remains authoritative; admins can (a) disable specific defaults by channel name and (b) add extra channels not on the default list. Admins do not restate the full default list. Per-channel sensitivity/cooldown overrides are out of scope for MVP.
- Q: How should `event_spike` alerts map to DrainCtl's existing severity levels? → A: Admin-configured per notification target, same model as existing trigger types — the admin picks the severity when they wire up a notification. No automatic escalation in MVP: persistence is handled by the existing per-target repeat interval; intensity-based severity bumping is out of scope for MVP. Admins who want "wake me up" can pick Alert from the start.
- Q: Is the Windows `Security` channel watched by default, and how do admins enable it if they want it? → A: Excluded by default. Enablement is a config-level opt-in via `evtspike.security_channel_enabled: true` in `config.json`. The DrainCtl service runs as `LocalSystem` by default, which already has `SeSecurityPrivilege` present in its token (Disabled state); when the flag is set, the service enables that privilege on its own token via `AdjustTokenPrivileges` at subsystem start and subscribes to the `Security` channel. No MSI component and no `LsaAddAccountRights` / `LsaRemoveAccountRights` operations — LocalSystem's built-in privilege is sufficient. Admins who have reconfigured DrainCtl to run under a dedicated service account must grant `SeSecurityPrivilege` to that account manually via `secedit` or group policy (out of MVP scope — the subsystem logs a warning and skips Security subscription if `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED`).
- Q: Should evtspike ship as a standalone CLI in addition to the in-service subsystem? → A: No. MVP ships only as the config-gated subsystem inside the DrainCtl service. The dual-deployment cost (pipe contract, separate baseline file, separate service install, double-run coordination, separate MSI) is not justified by the three originally-proposed use cases (not-yet-onboarded hosts, minimal footprint, isolation). If a thin RMM-deployable CLI is needed later, it is a separate project.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Early Warning on Anomalous Event Activity (Priority: P1)

An administrator is responsible for a farm of RDSH servers. A driver on one server starts logging intermittent failures at several times its usual rate. Within a minute of the rate change becoming sustained, the administrator receives a notification identifying the affected server, the affected event channel, and the severity of the deviation — well before user complaints arrive. The administrator opens the Event Viewer on that channel, confirms the fault, and schedules a drain.

**Why this priority**: This is the entire product premise. Admins today only learn about driver crashes, auth failures, profile-service thrashes, SMB hiccups, and similar issues after user complaints. Shifting detection earlier is the core value — every other story exists to support this one.

**Independent Test**: Deploy the built-in mode to one server, let the baseline settle, then inject a sustained burst of events on any watched channel. Verify a notification fires within the confirmation window (≤3 × 10 s = ≤30 s), names the channel correctly, and does not fire on isolated one-off events.

**Acceptance Scenarios**:

1. **Given** a server has been running with the detector enabled for at least its configured warm-up period, **When** a watched channel logs events at a rate that is statistically unusual for the current time of day and the elevated rate persists across two of three consecutive 10-second scoring buckets, **Then** a notification fires through the configured targets (webhook, ntfy, email) identifying server, channel, observed count, expected count, and time.
2. **Given** the detector is running, **When** a watched channel logs a single transient burst within one 10-second scoring bucket, **Then** no notification fires (the confirmation requirement suppresses one-shot transients).
3. **Given** the detector is running during a routine morning logon storm that is normal for this time of day after observation, **When** logon-related channels spike as usual, **Then** no notification fires (time-of-day awareness recognizes the spike as normal).
4. **Given** a notification fires for a channel and the channel continues to spike, **When** the configured repeat interval has not elapsed, **Then** no additional notification fires for the same server/channel (cooldown).

---

### User Story 2 - Resilient Baseline That Does Not Poison After Incidents (Priority: P1)

An administrator enables the detector. A genuine incident (runaway driver, misconfigured agent) floods one channel for half an hour. After the incident, a second, smaller anomaly occurs on the same channel the next day. The admin still gets notified about the second anomaly, because the incident did not teach the baseline that floods are now "normal."

**Why this priority**: Without this, the first real incident silently disables detection for that channel going forward — a silent failure mode that would destroy trust in the product. This is table stakes for a statistical detector.

**Independent Test**: Feed the detector a sustained artificial flood on one channel. After the flood ends, feed a smaller anomaly (big enough to normally alert). Verify the detector still recognizes it as anomalous.

**Acceptance Scenarios**:

1. **Given** a channel's baseline has been learned, **When** an extended anomalous burst occurs, **Then** the channel's baseline mean after the burst remains close to its pre-burst value (anomalies contribute to the baseline only in a capped form).
2. **Given** the detector has processed a prior flood on a channel, **When** a new, smaller anomaly occurs on the same channel later, **Then** it is still flagged as anomalous and triggers a notification.

---

### User Story 3 - Standalone Deployment (DROPPED — out of MVP scope)

Previously a P2 story for deploying `evtspike` as a standalone CLI that forwards spikes to DrainCtl over a named pipe. Dropped in the 2026-04-17 clarification round: the dual-deployment cost (separate pipe contract, separate baseline file, separate service install, double-run coordination, separate MSI packaging) is not justified by the three originally-proposed use cases (not-yet-onboarded hosts, minimal footprint, isolation from main service). If a thin RMM-deployable CLI is needed in a future release, it will be scoped as a separate project that can reuse the `internal/evtspike/` detector library without reopening the built-in-vs-standalone coordination questions.

Historical acceptance scenarios (NOT implemented):

(historical acceptance scenarios removed — see the Dropped note above)

---

### User Story 4 - Warm Restart Without Retraining (Priority: P2)

An administrator restarts the DrainCtl service — for a version upgrade, a scheduled reboot, or a config change. When the detector comes back up, it resumes with the baseline it had already learned. It does not spend another day in a warm-up period, and it does not generate a flood of alerts immediately after restart while it re-learns normal.

**Why this priority**: Without persistence, every restart throws away the detector's learned sense of normal. For an operations tool, this turns routine maintenance into an alert storm and wastes days of observation. P2 because the feature still works without it (it just retrains after restart) — but in production, it's close to mandatory.

**Independent Test**: Run the detector for long enough to mature its baseline, stop it, restart it, and verify that immediately after restart: (a) no alert storm fires, (b) a genuine anomaly is detected on the first post-restart minute it occurs.

**Acceptance Scenarios**:

1. **Given** the detector has been running long enough that its per-channel, per-time-of-day baseline is considered mature, **When** the process stops and restarts, **Then** it resumes from the persisted baseline without a fresh warm-up period.
2. **Given** the baseline state file is missing or unreadable at startup, **When** the detector starts, **Then** it logs a warning, initializes a fresh baseline, and continues operating (it does not crash or refuse to start).
3. **Given** the baseline state file exists but was written by an incompatible version, **When** the detector starts, **Then** it logs a warning, discards the incompatible state, and starts with a fresh baseline.

---

### User Story 5 - Admin Configuration and Visibility (Priority: P3)

An administrator wants to turn detection on, pick which servers participate, tune sensitivity, verify it is healthy, and understand why a given alert fired. They use the existing DrainCtl dashboard and configuration file — no new admin surface to learn. On the existing per-server card, a small detector status pill tells them at a glance whether the detector is healthy, still training, disabled, or in error. Clicking into a server reveals a short list of recent spikes for that host.

**Why this priority**: Operationally important but not on the critical path for MVP. Launching with sensible defaults and config-file control is acceptable; the dashboard surface is additive (one status pill + one recent-spikes list on the existing dashboard), not a new screen to design.

**Independent Test**: Toggle the feature off, confirm detection stops and the status pill shows "disabled." Toggle it on, confirm detection resumes and the pill transitions through "training" to "healthy" as the baseline matures. Raise sensitivity and inject borderline anomalies — confirm more fire. Lower sensitivity, confirm fewer fire. Inject a confirmed spike — confirm it appears in the recent-spikes list.

**Acceptance Scenarios**:

1. **Given** the detector is disabled in configuration, **When** the service starts, **Then** no subscriptions are created, no baseline file is written, and the feature has no measurable resource cost.
2. **Given** the detector is enabled, **When** an administrator changes sensitivity in configuration and saves, **Then** the change takes effect without requiring a service restart.
3. **Given** a spike alert has fired, **When** the administrator views the alert, **Then** the alert contains enough information to locate the event in the Windows Event Log: server name, channel, confirmation window timestamps, observed event count, and expected event count for that time-of-day.
4. **Given** the detector is running, **When** the administrator opens the existing dashboard, **Then** each server card shows a detector status pill with one of four states (healthy, training, disabled, error) and the most recent confirmed spike (if any) is visible without leaving the dashboard.
5. **Given** the administrator opens the detail view for a server, **When** confirmed spikes have occurred for that server, **Then** a recent-spikes list shows the last several entries (channel, time, observed vs. expected count) using the dashboard's existing list styling.

---

### Edge Cases

- A configured event channel does not exist on the host (e.g., optional Windows role not installed): the channel is skipped with a warning; detection continues on the remaining channels.
- A configured channel exists but the service lacks permission to subscribe: the channel is skipped with a warning; detection continues on the remaining channels.
- An event channel becomes unavailable mid-run (provider unloaded, permissions changed): the channel transitions from `subscribed` to `retrying` in the in-memory subscription state, its baseline is preserved, and the detector retries the subscription every 5 minutes; after 12 consecutive failures (1 hour) the channel transitions to `failed`. The dashboard `mature_channels` count excludes channels not currently in `subscribed` state. The detector does not bring down the whole subsystem.
- The host clock changes (DST transition, manual adjustment, NTP correction): the detector uses the new wall-clock time going forward; per-slot baselines remain meaningful because they key on time-of-day, not elapsed time. Specifically:
  - Backward wall-clock jump within an in-progress 10s bucket: drop the bucket counter, resume on the next 10s boundary.
  - DST fall-back (same 15-min slot visited twice in one day): both visits update the same `GammaState.N` — acceptable because per-slot stats accumulate observations regardless of calendar date.
  - DST spring-forward (one 15-min slot skipped that day): no harm — the slot simply gets one fewer observation that day.
- The baseline state file becomes corrupt during a crash: on next start, the detector renames the file to `.corrupt-<ts>.bak` and rebuilds from scratch. A file that is merely unreadable (AV lock, transient EACCES) is treated as missing — no rename, no data loss if the lock clears before the next write.
- A channel produces zero events for long stretches (rare provider): the detector handles this without flagging silence as anomalous.
- An alert target is misconfigured (webhook 401, ntfy unreachable, SMTP down): delivery failure for one target does not suppress the other targets, and does not disable detection.

## Requirements *(mandatory)*

### Functional Requirements

#### Detection

- **FR-001**: The detector MUST subscribe to a curated default set of Windows Event Log channels relevant to RDSH/Citrix workloads (authentication, remote desktop services, profile services, SMB, printing, networking, stability, and security providers). The default list ships in code and remains authoritative across upgrades; administrators do not restate it to make adjustments.
- **FR-001a**: Administrators MUST be able to disable specific channels from the default list by name in `config.json` (suppression).
- **FR-001b**: Administrators MUST be able to add extra channels not on the default list in `config.json` (addition).
- **FR-001c**: Per-channel sensitivity or cooldown overrides are out of scope for MVP; sensitivity and cooldown apply globally to all watched channels on a host.
- **FR-001d**: The Windows `Security` channel MUST be excluded from the default watched list. The service MUST NOT enable `SeSecurityPrivilege` on its own token unless Security monitoring is explicitly opted in via config.

#### Optional Security channel monitoring (config-level opt-in)

- **FR-029**: The detector MUST expose a single `config.json` boolean field `evtspike.security_channel_enabled` (default `false`) that controls whether the Windows `Security` channel is added to the watched list.
- **FR-030**: When `security_channel_enabled` is `true`, the subsystem at Start MUST (a) add `Security` to the watched channel list for this host and (b) enable `SeSecurityPrivilege` on its own token via `AdjustTokenPrivileges(SE_SECURITY_NAME, ENABLE)`. The default LocalSystem service account already has this privilege present (Disabled state) in its kernel-assembled token, so enabling succeeds without `LsaAddAccountRights`.
- **FR-031**: Documentation (README and the Settings UI help text) MUST state plainly that enabling `security_channel_enabled` lets the DrainCtl service read the Security log, clear the Security log, manage audit policy, and set SACLs — so the admin accepts informed risk. The canonical capability sentence is: "This enables the DrainCtl service to read the Security log, clear the Security log, manage audit policy, and set SACLs on this host."
- **FR-032**: When `security_channel_enabled` transitions from `true` to `false` via live config reload, the subsystem MUST Stop+Start: remove `Security` from the watched channel list and skip enabling `SeSecurityPrivilege` on its token. No `LsaRemoveAccountRights` call is ever made — LocalSystem's built-in privilege is not revoked; it simply goes back to its default Disabled state on the next token assembly.
- **FR-032a**: Admins running DrainCtl under a dedicated service account (non-LocalSystem) must grant `SeSecurityPrivilege` to that account manually via `secedit` or group policy before enabling `security_channel_enabled`. If `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED`, the subsystem MUST log a warning and skip the Security subscription — it MUST NOT crash, and other channels MUST continue to operate.
- **FR-033**: The detector MUST run as a single process with a single configuration and a single baseline state file regardless of whether Security monitoring is enabled — Security is an extra channel inside the same detector.
- **FR-002**: The detector MUST count event arrivals on each subscribed channel into 10-second buckets and score each bucket directly against the channel's baseline. There is no further aggregation into longer windows; the 10-second bucket is the atomic scoring unit.
- **FR-003**: The detector MUST maintain a separate expected-rate baseline per channel per 15-minute time-of-day slot (96 slots covering 24 hours) so that rates that are normal during one part of the day but unusual during another are scored correctly.
- **FR-004**: The detector MUST flag a window as anomalous only when the observed count both exceeds a configured absolute floor and is statistically unlikely given the channel's baseline.
- **FR-005**: The detector MUST require an anomaly to persist across at least two of three consecutive scoring windows before firing an alert (confirmation).
- **FR-006**: The detector MUST limit the contribution of an anomalous observation to its baseline so that a real incident cannot teach the baseline that the incident rate is normal (robust/capped baseline updates).
- **FR-007**: The detector MUST suppress repeat alerts for the same server-channel pair within a configurable cooldown interval.
- **FR-008**: The detector MUST use a global (all-hours) baseline as fallback when an individual time-of-day slot has not yet accumulated enough observations to be trusted. A slot is considered mature once it has accumulated a configurable minimum number of observations; the default is seven (approximately one week of wall-clock time, since each 15-minute slot is visited once per day). Administrators MAY adjust the threshold via `config.json`.
- **FR-009**: The detector MUST skip channels it cannot subscribe to (missing provider, insufficient permission) with a logged warning, and continue operating on the channels it can subscribe to.

#### Notification integration

- **FR-010**: A new notification trigger type, `event_spike`, MUST be available in the existing notification configuration. It MUST support the same multi-target fan-out, per-target repeat intervals, and delivery semantics (webhook, ntfy, email) as existing trigger types.
- **FR-011**: Each `event_spike` notification MUST identify: server hostname, event log channel name, time window, observed event count, expected event count for the current time-of-day, and a human-readable severity indicator.
- **FR-011a**: Severity for an `event_spike` notification MUST be configured by the administrator on the notification target wiring, consistent with how existing trigger types assign severity. The detector does not auto-assign severity based on spike intensity.
- **FR-011b**: `event_spike` notifications MUST NOT automatically escalate severity based on persistence or intensity in MVP. Persistent spikes continue to re-fire at the admin-configured severity using the existing per-target repeat interval; they do not silently get promoted to a louder severity.
- **FR-012**: Email `event_spike` notifications MUST follow the same template language and styling as existing DrainCtl email notifications (subject emoji + preview text + card layout).

#### Deployment modes

- **FR-013**: The detector MUST ship as an optional, config-gated subsystem inside the DrainCtl service. When disabled in configuration, the subsystem MUST create no subscriptions, write no baseline file, and consume no measurable steady-state resources.
- **FR-014 through FR-017**: (Removed in the 2026-04-17 scope reduction — standalone CLI dropped from MVP. See Clarifications for rationale. IDs intentionally left as a gap to preserve numbering in downstream docs.)

#### Persistence

- **FR-018**: The detector MUST persist its per-channel, per-time-of-day baseline to a single file on disk so that it can resume after restart without re-entering the warm-up period.
- **FR-019**: The detector MUST tolerate a missing, unreadable, corrupt, or version-incompatible baseline file at startup by logging a warning and starting with a fresh baseline.
- **FR-020**: The detector MUST write baseline state in a way that does not risk corruption from crashes (atomic replace).
- **FR-021**: The baseline file MUST remain small enough that write cost is negligible at the configured persistence cadence (the realistic upper bound for the default channel list is on the order of a few hundred kilobytes).

#### Configuration

- **FR-022**: All detector behavior that an administrator may need to tune — enable/disable, sensitivity (absolute floor and tail-probability threshold), cooldown, per-slot maturity threshold, channel disable-list, channel add-list, baseline file path — MUST be controlled from the existing DrainCtl configuration file (`config.json`), consistent with how other DrainCtl subsystems are configured.
- **FR-023**: Configuration changes MUST take effect without a service restart where doing so does not compromise detection correctness (same live-reload behavior as the rest of the DrainCtl configuration).

#### Operational health

- **FR-024**: The subsystem MUST log a per-channel subscription outcome at startup (subscribed / skipped with reason), so operators can confirm the detector is watching what they expect.
- **FR-025**: Alert delivery failures on one target (e.g., a webhook returns 500) MUST NOT suppress delivery to other configured targets and MUST NOT disable detection.

#### Dashboard surface (additive to existing dashboard)

- **FR-026**: Each server card on the existing dashboard MUST display a detector status pill with one of four states: healthy (baseline mature, detector running), training (detector running, baseline not yet mature), disabled (feature off in configuration), or error (detector failed to start on this host). No new dashboard page is introduced.
- **FR-027**: Each server's detail view on the existing dashboard MUST include a recent-spikes list showing the most recent confirmed spikes for that server: channel name, confirmation time, observed count, expected count. The list MUST reuse the dashboard's existing list styling.
- **FR-028**: Per-channel drilldown, baseline heatmaps, per-channel sensitivity editing, and baseline state inspection tools are explicitly out of scope for MVP.

### Key Entities

- **Watched Channel**: A single Windows Event Log channel on a single host that the detector is subscribed to. Has a name (e.g., `Microsoft-Windows-Winlogon/Operational`), a runtime subscription status (`subscribed` / `retrying` / `failed`), and a learned baseline.
- **Baseline**: The detector's learned sense of "normal" for one channel. Composed of one small numeric summary per 15-minute time-of-day slot (96 total), plus an all-hours global summary used while slots are immature.
- **Scoring Window**: A 10-second event-count bucket for one channel. The atomic unit that gets scored and contributes (possibly capped) to the baseline.
- **Spike Event**: The detector's output. Carries server, channel, observed count, expected count, and window timestamps. Severity is assigned by notification-target wiring (FR-011a), not by the detector; the Spike Event itself does not carry a severity field. Delivered to the notification pipeline in-process.
- **Notification Trigger (`event_spike`)**: A new entry in the existing notification trigger taxonomy. Alongside the existing triggers, it can be associated with any mix of webhook, ntfy, and email targets, with per-target repeat intervals.
- **Baseline State File**: The on-disk JSON file holding the full set of learned baselines for all watched channels on this host, written atomically on a cadence the service controls.

## Success Criteria *(mandatory)*

### Measurement workloads

Two named workloads underpin the numeric Success Criteria:

- **`normal-day-false-positive-workload`**: 4-core Windows Server 2019+ VM running the 54-channel default list. Event-generation trace averages 100–500 events/hour aggregate across watched channels, with one morning logon storm (sustained 60 s elevated rate on logon-related channels), a steady-state work-hours period, an evening logoff burst, and an overnight idle period. Runs for a full 7-day cycle. Deterministic fixture under `specs/006-evtspike-detection/fixtures/normal-day.json`. Validates **SC-001** and **SC-003**.
- **`stress-performance-workload`**: Same host shape. Sustained 200 events/sec aggregate across the default channel list for 1 hour after baseline maturity. Deterministic fixture under `specs/006-evtspike-detection/fixtures/stress.json`. Validates **SC-007**.

The validating test/simulator MUST exercise the production bucket/slot/maturity/fallback/confirmation/cooldown logic end-to-end — no mocking of detector internals. Compressed-time simulators are allowed for SC-001 provided slot boundaries, observation counts, and per-slot maturity progression are preserved.

### Measurable Outcomes

- **SC-001**: Running the `normal-day-false-positive-workload` produces zero `event_spike` notifications over a full weekly cycle once the baseline is mature.
- **SC-002**: When a genuine sustained anomaly is present on a watched channel, the administrator receives a notification within three 10-second scoring buckets (≤30 seconds) from the moment the anomaly begins.
- **SC-003**: Under the `normal-day-false-positive-workload`, a single transient burst that lasts under one scoring window does not produce a notification in at least 99% of injection cases.
- **SC-004**: After the detector experiences a sustained artificial flood on one channel, a subsequent smaller-but-still-anomalous event on the same channel is still detected — the baseline is not poisoned by the prior flood.
- **SC-005**: Restarting the service does not produce a post-restart alert storm, and does not re-enter the warm-up period for channels whose baseline was already mature before the restart.
- **SC-006**: When the feature is disabled in configuration, the service's steady-state resource use is within tolerance of a build without the feature: **baseline RSS delta ≤1 MB and CPU delta ≤0.1% of one core over a 1-hour idle measurement window**.
- **SC-007**: Running the `stress-performance-workload` with the default channel list, the feature's steady-state CPU is **<5% of one core (1-hour average)**, memory footprint is **<50 MB RSS**, and baseline write volume extrapolates to **≤13 MB/day** at the default 15-minute persistence cadence.
- **SC-008**: (Removed with the standalone CLI scope reduction on 2026-04-17.)
- **SC-009**: Alert delivery failure on one target (e.g., misconfigured webhook) does not delay or prevent delivery to the other configured targets.

## Assumptions

- **Platform**: The target platform is Windows Server running Remote Desktop Session Host, consistent with the rest of DrainCtl. Non-Windows hosts are out of scope.
- **Existing infrastructure**: The existing DrainCtl notification pipeline (multi-target webhook/ntfy/email, per-target repeat intervals, slog-based logging, `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`) is reused. The feature adds to this, not alongside it. The detector runs in-process as a subsystem of the DrainCtl service and dispatches notifications via direct function calls to `SendNotification` — no inter-process communication is needed.
- **Privileges**: By default the DrainCtl service runs as `LocalSystem`, whose token includes `SeSecurityPrivilege` (Disabled). The 53 default channels do not require this privilege. When Security monitoring is opted in via `evtspike.security_channel_enabled: true`, the subsystem enables the privilege on its own token via `AdjustTokenPrivileges` at Start. No `LsaAddAccountRights` or `LsaRemoveAccountRights` operations are performed; the privilege is already present in LocalSystem's token. Admins running under a dedicated service account must grant the privilege manually (out of MVP scope).
- **Channel list**: The 54-channel default list is curated for RDSH/Citrix workloads and ships in code as the authoritative default. Administrators tailor it by naming specific channels to disable and/or adding extra channels — they do not restate the default list. This keeps admin configs small and lets the default list improve across versions without touching existing deployments.
- **Warm-up**: A per-slot baseline is considered mature after a configurable number of observations, default seven (approximately one week of wall-clock observation, since each 15-minute slot is visited once per day). During warm-up, the detector falls back to the global all-hours baseline; alerts fired during this period are directionally correct but noisier. Admins should expect the first week after install at default settings to produce more false positives than steady state, and may raise or lower the threshold via `config.json`.
- **Validation**: The POC on `feat/evtspike-poc` has validated the statistical approach (Gamma-Poisson posterior, Negative Binomial tail, 2-of-3 confirmation, time-of-day slots, robust capped updates). This specification treats the algorithm as committed; productization — config, persistence, notification integration, dashboard — is the remaining work.

## Architectural Note: Reusable Scoring Engine

### Motivation

The anomaly detection engine specified above — Bayesian baseline learning, time-of-day slotting, capped updates, 2-of-3 confirmation, and cooldown management — is general-purpose. Future use cases such as performance counter monitoring (CPU utilization, memory pressure, disk latency, session counts), application-level metrics, or other numeric time-series data should be able to reuse the same engine without duplication or major refactoring. This addendum captures the architectural seam that MVP must respect in its internal design, without expanding MVP scope or introducing new public surface.

### Design Points

- **Structured observation interface.** The scoring engine SHOULD accept observations through a defined internal interface comprising: source identifier, timestamp, numeric value, and a model descriptor. It SHOULD return a verdict (normal / anomalous / confirming) together with context (observed value, expected value, deviation score). Adapters and the notification pipeline are the only consumers of this interface; it is not exposed as public API.
- **Parameterized models per source.** The model used for scoring SHOULD be parameterized per source. MVP ships with the Gamma-Poisson / Negative Binomial model for count data (event log channels). The interface SHOULD allow future model implementations — for example, a Normal-Inverse-Gamma model for continuous metrics like CPU utilization or disk latency — to be registered and selected per source without modifying the engine core.
- **Event log subscription as adapter.** The event log subscription layer becomes an adapter: it subscribes to channels, buckets arrivals into counts, and feeds the scoring engine. Future adapters (performance counter polling, custom metric ingestion) feed the same engine with their own data and model choice. Each adapter is responsible for acquisition and shaping; it does not reimplement baseline learning, confirmation, or cooldown.
- **Engine-owned cross-cutting concerns.** Baseline storage, time-of-day slotting, per-slot maturity tracking, 2-of-3 confirmation logic, and cooldown management remain engine responsibilities. Adapters MUST NOT reimplement these.
- **Source-agnostic spike output.** The spike event emitted by the engine carries source ID, source type, metric name, observed value, expected value, and window timestamps. The notification pipeline formats the human-readable message based on source type; the engine itself is indifferent to what the source represents.

### MVP Constraint

MVP ships only the event log adapter and the Gamma-Poisson model. The scoring engine interface and model parameterization described above are internal design decisions — they guide code organization and type boundaries, but they do not introduce any new public API, configuration surface, or user-facing model-selection mechanism. The goal is clean separation that makes future extension inexpensive, not premature generalization that adds complexity today.

### Future Considerations

- **Continuous metric models.** A Normal-Inverse-Gamma conjugate model (or similar) would support continuous-valued metrics such as CPU utilization, memory working set, and disk latency, where the Gamma-Poisson count model does not apply.
- **Bidirectional anomaly detection.** The current engine flags only upward deviations (spike detection). Some future metrics — session counts dropping unexpectedly, throughput collapsing — require detection in both directions. The scoring interface should not preclude a signed deviation score.
- **Scaling with many metric sources.** Adding performance counters or application metrics may increase the number of tracked sources per host by an order of magnitude. Baseline storage format, persistence write cost, and per-source memory overhead should be evaluated if the source count grows well beyond the current ~54-channel ceiling.
