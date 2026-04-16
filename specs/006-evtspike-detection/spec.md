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
- Q: Is the Windows `Security` channel watched by default, and how do admins enable it if they want it? → A: Excluded by default. Enablement is an explicit installer opt-in — a single checkbox "Enable Security event log monitoring" that (a) adds `Security` to the watched list and (b) grants `SeSecurityPrivilege` to the DrainCtl service account. The installer copy names the expanded capabilities (read Security log, clear Security log, alter audit policy) so the admin accepts informed risk. Single process, single config, single baseline; no privilege-isolated sidecar. Opting out via the installer removes `Security` from the watched list and revokes `SeSecurityPrivilege`, restoring minimum-privilege posture.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Early Warning on Anomalous Event Activity (Priority: P1)

An administrator is responsible for a farm of RDSH servers. A driver on one server starts logging intermittent failures at several times its usual rate. Within a minute of the rate change becoming sustained, the administrator receives a notification identifying the affected server, the affected event channel, and the severity of the deviation — well before user complaints arrive. The administrator opens the Event Viewer on that channel, confirms the fault, and schedules a drain.

**Why this priority**: This is the entire product premise. Admins today only learn about driver crashes, auth failures, profile-service thrashes, SMB hiccups, and similar issues after user complaints. Shifting detection earlier is the core value — every other story exists to support this one.

**Independent Test**: Deploy the built-in mode to one server, let the baseline settle, then inject a sustained burst of events on any watched channel. Verify a notification fires within the confirmation window (≤3 × 60 s), names the channel correctly, and does not fire on isolated one-off events.

**Acceptance Scenarios**:

1. **Given** a server has been running with the detector enabled for at least its configured warm-up period, **When** a watched channel logs events at a rate that is statistically unusual for the current time of day and the elevated rate persists across two of three consecutive 60-second windows, **Then** a notification fires through the configured targets (webhook, ntfy, email) identifying server, channel, observed count, expected count, and time.
2. **Given** the detector is running, **When** a watched channel logs a single transient burst within one 60-second window, **Then** no notification fires (the confirmation requirement suppresses one-shot transients).
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

### User Story 3 - Standalone Deployment for Servers Without Full DrainCtl (Priority: P2)

An administrator wants to monitor event activity on a server where running the full DrainCtl service is not appropriate (e.g., a server outside the drain workflow, or a sensitive host where admins prefer the minimum footprint). They deploy the evtspike CLI as a standalone process. The CLI detects spikes locally and, when a DrainCtl service is available on the same machine, forwards spike events to it for consolidated notification routing. When no DrainCtl is reachable, the CLI still logs spikes locally so the admin retains visibility.

**Why this priority**: The built-in mode covers the primary deployment, but operators have asked for a standalone packaging for hosts not yet onboarded, for minimal-footprint monitoring, and for isolation from the main service. It is not required on day one, but shipping it materially widens adoption.

**Independent Test**: On a host with a running DrainCtl service, run the evtspike CLI and verify spikes appear in DrainCtl's alert stream. Stop the DrainCtl service and verify the CLI continues to detect spikes and emits them to its local log without crashing.

**Acceptance Scenarios**:

1. **Given** DrainCtl is running on the same machine, **When** the standalone CLI detects a confirmed spike, **Then** DrainCtl receives the spike event and routes it through the normal notification pipeline as if it had detected it natively.
2. **Given** DrainCtl is not running on the same machine, **When** the standalone CLI detects a confirmed spike, **Then** the CLI logs the spike locally and continues operating; it does not crash, retry in a tight loop, or block detection.
3. **Given** the standalone CLI is running, **When** DrainCtl later becomes available, **Then** subsequent spikes are forwarded without requiring a restart of the CLI.

---

### User Story 4 - Warm Restart Without Retraining (Priority: P2)

An administrator restarts the DrainCtl service (or the standalone CLI) — for a version upgrade, a scheduled reboot, or a config change. When the detector comes back up, it resumes with the baseline it had already learned. It does not spend another day in a warm-up period, and it does not generate a flood of alerts immediately after restart while it re-learns normal.

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
- An event channel becomes unavailable mid-run (provider unloaded, permissions changed): the detector logs and continues; it does not bring down the whole subsystem.
- The host clock changes (DST transition, manual adjustment, NTP correction): the detector uses the new wall-clock time going forward; per-slot baselines remain meaningful because they key on time-of-day, not elapsed time.
- The baseline state file becomes corrupt during a crash: on next start, the detector treats it as missing and rebuilds from scratch.
- Two processes try to own the baseline file simultaneously (built-in mode and standalone CLI on the same host): the detector must either coordinate or refuse to double-run; it must not silently overwrite each other's state.
- A channel produces zero events for long stretches (rare provider): the detector handles this without flagging silence as anomalous.
- The standalone CLI loses its pipe connection mid-stream: it reconnects on the next spike without losing in-flight spikes already emitted.
- An alert target is misconfigured (webhook 401, ntfy unreachable, SMTP down): delivery failure for one target does not suppress the other targets, and does not disable detection.

## Requirements *(mandatory)*

### Functional Requirements

#### Detection

- **FR-001**: The detector MUST subscribe to a curated default set of Windows Event Log channels relevant to RDSH/Citrix workloads (authentication, remote desktop services, profile services, SMB, printing, networking, stability, and security providers). The default list ships in code and remains authoritative across upgrades; administrators do not restate it to make adjustments.
- **FR-001a**: Administrators MUST be able to disable specific channels from the default list by name in `config.json` (suppression).
- **FR-001b**: Administrators MUST be able to add extra channels not on the default list in `config.json` (addition).
- **FR-001c**: Per-channel sensitivity or cooldown overrides are out of scope for MVP; sensitivity and cooldown apply globally to all watched channels on a host.
- **FR-001d**: The Windows `Security` channel MUST be excluded from the default watched list. The DrainCtl service account MUST NOT receive `SeSecurityPrivilege` as a side effect of a default install.

#### Optional Security channel monitoring (installer opt-in)

- **FR-029**: The installer MUST present a clearly labeled optional component "Enable Security event log monitoring" that is off by default.
- **FR-030**: When the admin opts in, the installer MUST (a) add `Security` to the watched channel list for this host and (b) grant `SeSecurityPrivilege` to the DrainCtl service account.
- **FR-031**: The installer UI for this option MUST state plainly that `SeSecurityPrivilege` additionally allows the DrainCtl service to read the Security log, clear the Security log, alter SACLs, and manage audit policy — so the admin is accepting informed risk.
- **FR-032**: When the admin opts out of this component on re-run of the installer, the installer MUST (a) remove `Security` from the watched channel list on this host and (b) revoke `SeSecurityPrivilege` from the DrainCtl service account, restoring minimum-privilege posture.
- **FR-033**: The detector MUST run as a single process with a single configuration and a single baseline state file regardless of whether Security monitoring is enabled — Security is an extra channel inside the same detector, not a separate sidecar service.
- **FR-002**: The detector MUST count event arrivals on each subscribed channel into short time buckets (approximately 10 seconds), aggregated into rolling windows of approximately 60 seconds for scoring.
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
- **FR-014**: The detector MUST also ship as a standalone command-line program that can run without the full DrainCtl service installed.
- **FR-015**: When the standalone CLI detects a confirmed spike and a local DrainCtl service is reachable via the existing named-pipe channel, it MUST forward the spike event to DrainCtl, which then routes it through the normal notification pipeline.
- **FR-016**: When the standalone CLI detects a confirmed spike and the DrainCtl service is unreachable, it MUST log the spike to its own output and continue operating; it MUST NOT crash, spin, or stop detecting.
- **FR-017**: The standalone CLI MUST reconnect to a DrainCtl service that becomes available after it has been unavailable, without requiring a CLI restart.

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

- **Watched Channel**: A single Windows Event Log channel on a single host that the detector is subscribed to. Has a name (e.g., `Microsoft-Windows-Winlogon/Operational`), a subscription status, and a learned baseline.
- **Baseline**: The detector's learned sense of "normal" for one channel. Composed of one small numeric summary per 15-minute time-of-day slot (96 total), plus an all-hours global summary used while slots are immature.
- **Scoring Window**: A 60-second rolling aggregation of short-bucket counts for one channel. The atomic unit that gets scored and contributes (possibly capped) to the baseline.
- **Spike Event**: The detector's output. Carries server, channel, observed count, expected count, window timestamps, and a severity. Delivered to the notification pipeline (built-in mode) or over the named pipe to DrainCtl (standalone mode).
- **Notification Trigger (`event_spike`)**: A new entry in the existing notification trigger taxonomy. Alongside the existing triggers, it can be associated with any mix of webhook, ntfy, and email targets, with per-target repeat intervals.
- **Baseline State File**: The on-disk JSON file holding the full set of learned baselines for all watched channels on this host, written atomically on a cadence the service controls.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On a representative RDSH host, routine daily operation (morning logon storm, steady-state work hours, evening logoff, overnight idle) produces zero `event_spike` notifications over a full weekly cycle once the baseline is mature.
- **SC-002**: When a genuine sustained anomaly is present on a watched channel, the administrator receives a notification within three 60-second scoring windows (≤3 minutes) from the moment the anomaly begins.
- **SC-003**: A single transient burst that lasts under one scoring window does not produce a notification in at least 99% of cases observed during steady-state operation.
- **SC-004**: After the detector experiences a sustained artificial flood on one channel, a subsequent smaller-but-still-anomalous event on the same channel is still detected — the baseline is not poisoned by the prior flood.
- **SC-005**: Restarting the service does not produce a post-restart alert storm, and does not re-enter the warm-up period for channels whose baseline was already mature before the restart.
- **SC-006**: When the feature is disabled in configuration, the service's steady-state CPU and memory footprint is indistinguishable from a build without the feature.
- **SC-007**: When enabled with the default channel list, the feature's steady-state CPU is a small single-digit percentage of one core under normal event loads, and its memory footprint is well under 50 MB.
- **SC-008**: The standalone CLI starts, subscribes, and emits its first scoring pass within 15 seconds of launch.
- **SC-009**: Alert delivery failure on one target (e.g., misconfigured webhook) does not delay or prevent delivery to the other configured targets.

## Assumptions

- **Platform**: The target platform is Windows Server running Remote Desktop Session Host, consistent with the rest of DrainCtl. Non-Windows hosts are out of scope.
- **Existing infrastructure**: The existing DrainCtl notification pipeline (multi-target webhook/ntfy/email, per-target repeat intervals, slog-based logging, `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`, named-pipe service interface) is reused. The feature adds to these, not alongside them.
- **Privileges**: By default the DrainCtl service runs with its existing privilege set, which is sufficient for every channel on the default watched list. The only channel requiring additional privilege — `Security`, which needs `SeSecurityPrivilege` — is excluded from the default list. Admins who want Security monitoring opt in at install time via a dedicated installer component; the installer grants the privilege on opt-in and revokes it on opt-out. The detector itself remains a single process with a single configuration.
- **Channel list**: The 54-channel default list is curated for RDSH/Citrix workloads and ships in code as the authoritative default. Administrators tailor it by naming specific channels to disable and/or adding extra channels — they do not restate the default list. This keeps admin configs small and lets the default list improve across versions without touching existing deployments.
- **Warm-up**: A per-slot baseline is considered mature after a configurable number of observations, default seven (approximately one week of wall-clock observation, since each 15-minute slot is visited once per day). During warm-up, the detector falls back to the global all-hours baseline; alerts fired during this period are directionally correct but noisier. Admins should expect the first week after install at default settings to produce more false positives than steady state, and may raise or lower the threshold via `config.json`.
- **Standalone pipe scope**: The standalone CLI's named-pipe target is a DrainCtl service on the same host. Cross-machine forwarding is out of scope for this feature.
- **Validation**: The POC on `feat/evtspike-poc` has validated the statistical approach (Gamma-Poisson posterior, Negative Binomial tail, 2-of-3 confirmation, time-of-day slots, robust capped updates). This specification treats the algorithm as committed; productization — deployment modes, persistence, configuration, notification integration — is the remaining work.
