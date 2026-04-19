# Tasks: Event Log Anomaly Detection (evtspike)

**Input**: Design documents from `/specs/006-evtspike-detection/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Included. The spec's Success Criteria (SC-001 through SC-009) and the Independent Test language in every user story make tests load-bearing for this feature — robust baseline, warm restart, and graceful degradation are only provable by test. Unit tests + contract tests + integration tests are in-scope. Manual smoke tests live in `quickstart.md` and are reflected only as Polish-phase validation tasks.

**Organization**: Tasks are grouped by user story so the detector path (US1+US2) can ship as MVP, and standalone/warm-restart/config polish can follow as independent increments.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no cross-task dependencies)
- **[Story]**: US1..US5 per spec.md
- Every task names the exact file(s) touched

## Path Conventions

Single-project Windows-only layout (per plan.md Structure Decision). Paths below are relative to repo root:
- Root package `drainctl`: files directly under `/` (e.g., `config.go`, `notify.go`, `drainctl.go`)
- `cmd/evtspike/` — standalone CLI / service binary
- `cmd/drainctl/` — user CLI (unchanged by this feature)
- `internal/evtspike/` — NEW detector library
- `internal/pipe/`, `internal/dashboard/`, `internal/svc/` — existing, edited additively
- `ui/src/` — existing Svelte 5 dashboard
- `msi/` — WiX 5 project

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Prepare the repo for the feature. Most scaffolding already exists; this phase is small.

- [x] T001 Create empty `internal/evtspike/` package directory with a placeholder `doc.go` file containing `//go:build windows` and a one-line package comment, so subsequent parallel file adds don't race on directory creation
- [x] T002 [P] Create the fixture directory `specs/006-evtspike-detection/fixtures/` with placeholder `normal-day.json` and `stress.json` files (filled during SC-validation tasks in Phase 9)
- [x] T003 [P] Update `CLAUDE.md` Architecture section with a single-line note about the new `internal/evtspike` subsystem (additive, non-breaking)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lift the POC detector into a reusable library and add the shared types all stories depend on. **Nothing in US1..US5 can start until this phase completes.**

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### Config + Trigger

- [x] T004 Add `EvtSpikeConfig` struct to `config.go` (zero-value defaults, JSON tags per `contracts/evtspike-config.md`) and embed in `Config` as `EvtSpike EvtSpikeConfig \`json:"evtspike"\``
- [x] T005 Add `ClampEvtSpike(cfg *EvtSpikeConfig)` helper to `config.go` (clamps per `data-model.md` §1 tables); call it from `LoadConfig` next to the existing `ClampRetention` call
- [x] T006 Add `TriggerEventSpike Trigger = "event_spike"` constant to `config.go`; add to `ValidTriggers` map; DO NOT add to `DefaultTriggers` (explicit opt-in required by design)

### Detector library (lift from POC)

- [x] T007 [P] Move `cmd/evtspike/detector.go` → `internal/evtspike/detector.go`; update package from `main` to `evtspike`; keep `//go:build windows` tag. **REQUIRED change during move**: replace the hardcoded `minSlotN = 5` with a parameter sourced from `cfg.SlotMaturityObservations` (default 7 per `data-model.md` §1). This is not optional — keeping the hardcoded value would silently ignore the configurable threshold defined in `EvtSpikeConfig`.
- [x] T008 [P] Move `cmd/evtspike/detector_test.go` → `internal/evtspike/detector_test.go`; update package; keep tests passing
- [x] T009 [P] Create `internal/evtspike/channels.go` with the 54-channel default list (lifted verbatim from `cmd/evtspike/main.go` lines 32-109) exported as `var Defaults = []string{...}` and a `ResolveChannels(cfg EvtSpikeConfig) []string` function implementing the merge rules from `data-model.md` §4. When `cfg.SecurityChannelEnabled == true`, `"Security"` is added to the list.
- [x] T010 [P] Create `internal/evtspike/channels_test.go` with table tests for: default-only returns 54 items, disable removes by case-insensitive name, add appends, add of duplicate dedupes, `SecurityChannelEnabled == true` adds `Security`, `SecurityChannelEnabled == false` does not include `Security` even if default list were to contain it.
- [x] T011 [P] Create `internal/evtspike/subscriber_windows.go` by lifting the `subscribe(ctx, channel, query, counter)` function from `cmd/evtspike/main.go` lines 172-235; export as `Subscribe`; keep `//go:build windows`
- [x] T012 [P] Create `internal/evtspike/spike.go` defining `SpikePayload` struct with JSON tags exactly matching `contracts/event_spike-payload.md` `spike` sub-object schema plus the top-level `Host` field used by the notification envelope. The struct has NO `Severity` field — severity is assigned by notification-target wiring per FR-011a, not carried on the spike itself.
- [x] T012a [P] Create `internal/evtspike/privilege_windows.go` with `EnableSecurityPrivilege() error` that calls `LookupPrivilegeValue(SE_SECURITY_NAME)` then `AdjustTokenPrivileges` on the current process token to enable the privilege. Return a typed error (e.g., `ErrPrivilegeNotAssigned`) when `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED` — callers (subsystem Start) log-and-skip rather than abort.

### Baseline persistence

- [x] T013 [P] Create `internal/evtspike/baseline.go` with `BaselineFile`, `ChannelState`, and (re-exported) `GammaState` types per `data-model.md` §3; `SchemaVersion = 1`
- [x] T014 [P] Add `WriteBaseline(path string, bf *BaselineFile) error` to `internal/evtspike/baseline.go` using the existing atomic-write primitive from `config.go` (temp file + `MoveFileEx REPLACE_EXISTING`, named-mutex guard)
- [x] T015 [P] Add `LoadBaseline(path string) (*BaselineFile, error)` to `internal/evtspike/baseline.go` handling four distinct startup cases per `data-model.md` §3 load protocol: (1) file missing → log warning, return fresh `BaselineFile` — NO rename; (2) file unreadable (AV lock / transient EACCES) → log warning, return fresh `BaselineFile` — NO rename (so a transient lock that clears before next write does not permanently lose learned state); (3) JSON unmarshal error → rename to `.corrupt-YYYYMMDD-HHMMSS.bak`, log warning, return fresh; (4) `SchemaVersion > 1` → rename to `.incompat-YYYYMMDD-HHMMSS.bak`, log warning, return fresh.
- [x] T016 [P] Create `internal/evtspike/baseline_test.go` with round-trip test (write → read → deep-equal), missing-file fresh-return test (asserts NO `.bak` created), unreadable-file fresh-return test (simulate EACCES; asserts NO `.bak` created and original file untouched), corrupt-JSON rename-and-rebuild test (asserts `.corrupt-*.bak` created), incompatible-version rename-and-rebuild test (asserts `.incompat-*.bak` created).

### Dashboard status types (shared)

- [x] T017 [P] Create `internal/evtspike/status.go` defining `DetectorStatus` and `RecentSpikeEntry` structs per `data-model.md` §6 and §7, with a `DeriveState(enabled bool, startupErr error, matureChannels int) string` helper implementing the state table

**Checkpoint**: Foundation ready — detector library is package-internal and importable by both the in-service subsystem and the standalone CLI. User story implementation can now begin in parallel.

---

## Phase 3: User Story 1 - Early Warning on Anomalous Event Activity (Priority: P1) 🎯 MVP

**Goal**: Enable the detector inside the DrainCtl service, wire confirmed spikes into the existing notification pipeline, and expose the additive dashboard surface (status pill + recent-spikes list). After this phase, an admin who flips `evtspike.enabled: true` and subscribes a webhook to `event_spike` receives a notification on the first confirmed spike, and sees the detector on the dashboard.

**Independent Test**: On a test RDSH with the built-in mode enabled, inject 50 events on `Application` in a sustained burst. Within three scoring buckets (≤30 seconds): webhook receives an `event_spike` POST naming the `Application` channel, the dashboard status pill shows `training` (first week) or `healthy` (after), and the detail view shows the spike in the recent-spikes list. A single transient burst lasting **one 10-second scoring bucket or less** (covered by the 2-of-3 confirmation suppression, per spec.md US1 Acceptance Scenario 2 and SC-003) does NOT fire a notification.

### Tests for User Story 1

- [x] T018 [P] [US1] Contract test in `notify_test.go`: marshal a `SpikePayload` via `SendNotification(..., TriggerEventSpike, "")`; assert the body matches `contracts/event_spike-payload.md` (top-level fields present, `spike` sub-object shape, HMAC header correct when `secret` configured)
- [x] T018a [P] [US1] Email template render test in `notify_test.go` (or `email_test.go`): render the MJML template for an `event_spike` payload to HTML; assert the subject emoji matches severity (⚠️ warning, 🚨 alert), preview text contains the channel name and observed/expected values, and the card body lists Observed-vs-Expected and Confirmation-Window rows. Corresponds to `TestEventSpikePayload_EmailTemplate` in `contracts/event_spike-payload.md`.
- [x] T018b [P] [US1] Ntfy priority mapping test in `notify_test.go`: assert `status: "warning"` → priority 3, `status: "alert"` → priority 4, tags include `["evtspike", host, channel-basename]`. Corresponds to `TestEventSpikePayload_NtfyPriority` in `contracts/event_spike-payload.md`.
- [x] T019 [P] [US1] Contract test in `internal/dashboard/server_test.go`: `GET /api/evtspike/status?host=X` returns shape from `contracts/dashboard-sse-events.md`; 404 for unknown host; 401 without session
- [x] T020 [P] [US1] Contract test in `internal/dashboard/server_test.go`: `GET /api/evtspike/spikes?host=X&limit=20` returns newest-first ring-buffer content; `limit=500` clamped to 50
- [x] T021 [P] [US1] Integration test in `internal/evtspike/subsystem_test.go`: fake `Subscriber` feeds 50-event bursts in 2-of-3 windows; assert `OnSpike` invoked exactly once per cooldown with a `SpikePayload` matching invariants from `data-model.md` §5
- [x] T022 [P] [US1] Integration test in `internal/evtspike/subsystem_test.go`: single-window transient burst (one window over threshold, next two clean) does NOT call `OnSpike` (confirms 2-of-3 suppression)
- [x] T023 [P] [US1] Integration test in `internal/dashboard/broker_test.go`: state transition `disabled → training → healthy` emits exactly one `detector_status` SSE event per transition; same-state no-op emits zero

### Implementation for User Story 1

#### Service-side subsystem

- [x] T024 [US1] Create `internal/evtspike/subsystem.go` with `Subsystem` struct owning channel subscriptions, detectors, baseline ticker, and an `OnSpike func(SpikePayload)` callback; implement `Start(ctx) error` (load baseline via `LoadBaseline`, subscribe per `ResolveChannels`, start scoring goroutine, start persistence ticker per R1) and `Stop()` (cancel context, flush baseline via `WriteBaseline`, wait for goroutines)
- [x] T025 [US1] In `internal/evtspike/subsystem.go`, implement the scoring goroutine: 10-second ticker reading per-channel counters (already atomic.Int64), feeding `Detector.ObserveBucket`, and on confirmed spike building a `SpikePayload` and invoking `OnSpike`
- [x] T026 [US1] Integrate subsystem into `internal/svc/svc.go`: construct when `cfg.EvtSpike.Enabled == true`; pass an `OnSpike` callback that invokes `drainctl.SendNotification(cfg.Notifications, notifyState, spikeCheckResult, TriggerEventSpike, "")` on the service's existing goroutine pool
- [x] T027 [US1] In `internal/svc/svc.go`, add the subsystem to the graceful-shutdown sequence (call `Subsystem.Stop()` before returning from `Run`)
- [x] T028 [US1] At subsystem Start, if `cfg.SecurityChannelEnabled == true`: call `EnableSecurityPrivilege()` (from T012a) on the service's own process token; if it returns `nil`, proceed to subscribe to Security (resolved by `ResolveChannels` when the flag is set); if it returns `ErrPrivilegeNotAssigned` (admin runs DrainCtl under a dedicated account without the right), log a warning and skip Security subscription — other channels continue. Log at INF level which channels were subscribed vs skipped (FR-024).

#### Notification pipeline integration

- [x] T029 [US1] In `notify.go`, add `LastSpikeNotify map[string]map[string]time.Time` to `NotifyState` per `data-model.md` §9 (keyed by target URL → "host|channel" → last sent)
- [x] T030 [US1] In `notify.go`, add an `event_spike` branch to `SendNotification`: reuse the existing envelope, populate the `spike` sub-object from a new `*SpikePayload` parameter (thread through the existing signature via a typed context struct or a new `SendSpikeNotification` helper — pick whichever keeps call sites clean); enforce per-target repeat interval using `LastSpikeNotify`
- [x] T031 [US1] In `notify.go`, ensure severity comes from the target wiring (per-target `severity` field or existing severity-tag mechanism); no auto-escalation (FR-011a, FR-011b)
- [x] T032 [US1] Update `email.go` (and/or its MJML template) to render `event_spike` using the card layout from commits `bdf197e`/`38d6dae`: subject emoji (⚠️ warning / 🚨 alert), preview text `"<channel>: <observed> events, expected ~<expected>. Tail probability <p>."`, card body with Observed-vs-Expected and Confirmation-Window rows
- [x] T033 [US1] Add `ntfy` rendering branch for `event_spike` in `notify.go`: Title=subject, Message=message, Priority (3 for warning / 4 for alert), Tags=["evtspike", host, channel-basename]

#### Dashboard surface

- [x] T034 [US1] In `internal/dashboard/broker.go`, add `detector_status` and `recent_spike` SSE event types to the broker dispatch; reuse existing fan-out and backpressure logic
- [x] T035 [US1] In `internal/dashboard/server.go`, add `GET /api/evtspike/status?host=<hostname>` handler returning `DetectorStatus` per `contracts/dashboard-sse-events.md`; require authenticated session
- [x] T036 [US1] In `internal/dashboard/server.go`, add `GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>` handler serving from an in-memory ring buffer (capacity 20 per host), backed by a new `internal/dashboard/spikestore.go` ring-buffer type; default limit 20, clamp to 50
- [x] T037 [US1] In the subsystem's `OnSpike` call path (service-side), additionally push the spike to the dashboard's ring buffer and emit a `recent_spike` broker event; fire a `detector_status` broker event on state transitions in `Subsystem.Start` (state derivation per `DeriveState`)
- [x] T038 [US1] In `ui/src/lib/api.js`, add `fetchEvtSpikeStatus(host)` and `fetchRecentSpikes(host)`; extend the existing SSE client to handle `detector_status` and `recent_spike` event types and update in-memory state
- [x] T039 [US1] In `ui/src/lib/types.ts` (or `.d.ts`), add `DetectorStatus` and `RecentSpike` TypeScript types mirroring `data-model.md` §6 and §7
- [x] T040 [US1] In `ui/src/components/ServerCard.svelte`, render a detector status pill next to the existing server pills using the four-state color map (healthy=green, training=amber, disabled=grey, error=red); consume from SSE-driven state
- [x] T041 [US1] In `ui/src/components/ServerDetail.svelte`, add a "Recent spikes" section reusing the existing list styling (channel, time, observed vs expected); populate from `fetchRecentSpikes` on mount and refresh on `recent_spike` SSE events

#### Logging + ops

- [x] T042 [US1] Add slog structured log lines at key lifecycle points in `internal/evtspike/subsystem.go`: `evtspike=start`, `evtspike=subscribed channel=X`, `evtspike=skipped channel=X reason=…`, `evtspike=confirmed_spike host=X channel=X observed=X expected=X tail=X`, `evtspike=baseline_written bytes=X`, `evtspike=stop`

**Checkpoint**: User Story 1 + 2 (via shared detector) fully functional end-to-end. An admin can enable the feature, inject a spike, see notification fire, see dashboard update. MVP is deployable at this point.

---

## Phase 4: User Story 2 - Resilient Baseline That Does Not Poison After Incidents (Priority: P1)

**Goal**: Prove and protect the robust-cap update path. The detector math from the POC already implements this; what's missing is test coverage against the full subsystem so future refactors can't silently break it.

**Independent Test**: Feed a fake subscriber a 30-minute sustained flood on one channel (e.g., 100 events per 10s bucket for ~180 consecutive buckets). After the flood ends, feed a single anomalous burst (e.g., 30 events when baseline would previously have been ~1). Assert: (a) the channel's posterior mean after the flood is within 2× of its pre-flood value (not 100×), and (b) the follow-up 30-event burst still triggers an `OnSpike` call.

### Tests for User Story 2

- [x] T043 [P] [US2] Unit test in `internal/evtspike/detector_test.go`: feed 30 consecutive anomalous observations (y=100 with expected=1); assert `Slots[slot].Alpha/Beta` grows by ≤2× over the pre-flood value (robust cap effectiveness)
- [x] T044 [P] [US2] Integration test in `internal/evtspike/subsystem_test.go`: simulate flood → pause → second smaller anomaly; assert `OnSpike` called for BOTH the flood (first confirmation) and the second anomaly (robust cap did not poison)
- [x] T045 [P] [US2] Integration test in `internal/evtspike/subsystem_test.go`: flood-then-restart scenario — persist baseline mid-flood, reload, verify post-reload scoring of the second anomaly still flags (robust cap survives warm restart)

### Implementation for User Story 2

No new production code — the POC's `Detector.ObserveBucket` already caps update `y` at `negBinQuantile(0.99, alpha, beta)` before `ewmaUpdate`. These tests verify and lock in that behavior.

- [x] T046 [US2] Audit `internal/evtspike/detector.go` for the robust-cap path (lines 105-111 in the POC); add a single-line comment tying it to SC-004 and US2's Independent Test, so future refactors don't inadvertently remove it

**Checkpoint**: US1 + US2 are both fully functional and regression-guarded. MVP is hardened.

---

## Phase 5: User Story 3 (DROPPED — standalone CLI out of MVP scope)

Previously described the standalone `evtspike` binary as a Windows service forwarding spikes to DrainCtl over a named pipe. Dropped on 2026-04-17 (see `spec.md` Clarifications). Task IDs T047–T058 are intentionally left as a numbering gap; do not re-use them.

---

## Phase 6: User Story 4 - Warm Restart Without Retraining (Priority: P2)

**Goal**: Ensure service restarts preserve the learned baseline so the detector doesn't re-enter warm-up and doesn't fire an alert storm on restart.

**Independent Test**: Start the service. Feed enough observations that at least one slot matures (lower `slot_maturity_observations` to 1 for the test). Stop the service. Within 1 second of restart: (a) no `OnSpike` calls fire, (b) the dashboard pill shows `healthy` immediately (not `training`), (c) a genuine anomaly on the first post-restart scoring window still triggers `OnSpike`.

### Tests for User Story 4

- [x] T059 [P] [US4] Integration test in `internal/evtspike/subsystem_test.go`: mature a slot, Stop+Start the subsystem, assert loaded baseline's `GammaState` values equal pre-stop values (bit-for-bit via `reflect.DeepEqual`)
- [x] T060 [P] [US4] Integration test in `internal/evtspike/subsystem_test.go`: post-restart, inject a normal-rate bucket → NO `OnSpike`; inject an anomalous bucket → `OnSpike` fires on the first eligible confirmation window
- [x] T061 [P] [US4] Unit test in `internal/evtspike/baseline_test.go`: write a baseline file with `SchemaVersion=99`, then `LoadBaseline` → returns fresh baseline, writes warning to slog, renames original to `.incompat-YYYYMMDD-HHMMSS.bak`
- [x] T062 [P] [US4] Unit test in `internal/evtspike/baseline_test.go`: write a baseline file, truncate to half its bytes, then `LoadBaseline` → returns fresh baseline, writes warning, renames original to `.corrupt-YYYYMMDD-HHMMSS.bak`

### Implementation for User Story 4

Most persistence code is already in Foundational (T013-T015). Remaining tasks:

- [x] T063 [US4] In `internal/evtspike/subsystem.go`, hook `LoadBaseline` at subsystem start: if a file is returned, seed the per-channel `Detector.Slots` and `Detector.Global` from `BaselineFile.Channels[name]`; for newly added channels not in the file, start with fresh state (do not error)
- [x] T064 [US4] In `internal/evtspike/subsystem.go` `Stop()`, flush in-memory state to a `BaselineFile` and call `WriteBaseline` one final time (covers crash-less shutdown)
- [x] T065 [US4] Verify the periodic persistence ticker (already in T024) calls `WriteBaseline` every `PersistIntervalSeconds` (default 900 = 15 min per R1); add a test in `subsystem_test.go` that a mock clock advance triggers the write

**Checkpoint**: Warm restart is verified and no-alert-storm on restart is guaranteed.

---

## Phase 7: User Story 5 - Admin Configuration and Visibility (Priority: P3)

**Goal**: Make the detector's runtime fully tunable via `config.json` without service restart for scalar tunables, and restart cleanly for channel-list changes. This story also finishes the dashboard configuration touchpoints that US1 built on.

**Independent Test**: With the service running and an active baseline: (a) change `threshold` from `1e-4` to `1e-3` in config.json; next scoring window uses the new value (verify via log line). (b) Change `disabled_channels` to add one channel; subsystem restarts within 1 s; dashboard pill transitions correctly; no alert storm. (c) Toggle `enabled: true → false`; subsystem stops cleanly, pill shows `disabled`; toggle back → `training` then `healthy`.

### Tests for User Story 5

- [x] T066 [P] [US5] Integration test in `internal/evtspike/subsystem_test.go`: running subsystem → call `Reload(newCfg)` with a changed `threshold` → next `ObserveBucket` uses new value (inspect internal config)
- [x] T067 [P] [US5] Integration test in `internal/evtspike/subsystem_test.go`: call `Reload(newCfg)` with a changed `disabled_channels` → assert subsystem performs Stop+Start (detectable via a "channel list changed" slog line), no alert storm
- [x] T068 [P] [US5] Integration test in `internal/svc/svc_test.go`: file watcher picks up a mtime change on `config.json` → `EvtSpike.Reload` is called with the new config

### Implementation for User Story 5

- [x] T069 [US5] In `internal/evtspike/subsystem.go`, implement `Reload(newCfg EvtSpikeConfig) error`: hot-apply the scalar fields from the live-reload matrix in `contracts/evtspike-config.md` (min_count, threshold, cooldown_minutes, slot_maturity_observations, persist_interval_seconds, half_life_buckets, prior_strength, mean_per_bucket_prior); detect channel-list or baseline-path changes and perform Stop+Start while holding an internal mutex
- [x] T070 [US5] In `internal/svc/svc.go` config reload path (existing file watcher), add a branch that calls `evtspike.Reload(cfg.EvtSpike)` when that block's fields change; log the event at INF level
- [x] T071 [US5] Update the dashboard's existing settings UI (not a new page, per FR-028) to surface `evtspike.enabled` as a simple toggle in the existing Settings modal — reuse existing API endpoints; NO per-channel or sensitivity editing controls (explicitly out of scope)

**Checkpoint**: Admin config surface is complete; live reload behavior is validated; dashboard exposes the toggle but no new visual real estate beyond US1's additions.

---

## Phase 8: Security Channel Opt-In (DROPPED — MSI component out of MVP scope)

Previously specified an MSI `<Feature Id="SecurityEventLog">` with `LsaAddAccountRights` / `LsaRemoveAccountRights` custom actions. Dropped on 2026-04-17 after verifying that the default `LocalSystem` service account already has `SeSecurityPrivilege` present (Disabled) in its token; the subsystem simply enables it at Start via `AdjustTokenPrivileges` when `security_channel_enabled: true` (see T028, T012a). No MSI changes required for this feature. Task IDs T072–T082 are intentionally left as a numbering gap; do not re-use them.

The single Security-opt-in test now lives in Phase 3 as part of the subsystem tests (T023a below) rather than as a Phase 8 MSI test.

- [x] T023a [P] [US1] Integration test in `internal/evtspike/subsystem_test.go`: set `cfg.SecurityChannelEnabled = true` on a LocalSystem-compatible mock; assert `EnableSecurityPrivilege()` is called exactly once at Start; if the mock simulates `ERROR_NOT_ALL_ASSIGNED` (dedicated-account scenario), assert Security is skipped from `ResolveChannels` output and a warning is logged, but other channels continue to subscribe.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Release plumbing, documentation, and SC-based validation on real hardware.

### Release plumbing

- [x] T083 (DROPPED — versioning is now git-derived CalVer via `scripts/version.ps1`, injected at build time via ldflags / `just resource` / `-p:ProductVersion=` / `.psd1.tmpl` rendering. Nothing to bump by hand. See CLAUDE.md "Key Rules". Tasks.md predated the migration.)
- [x] T084 (DROPPED — `just resource` still exists and is run automatically by `just all`/`just release`; no standalone bump step is required. Superseded alongside T083.)
- [x] T085 Run `just lint` and fix every warning — zero-tolerance noise policy per `feedback_clean_output.md`
- [x] T086 Run `just all` (unsigned) — verify full build produces CLI + DLL + PS module + MSI (no new binaries — the POC `cmd/evtspike` is a dev tool only)
- [x] T087 Run `just release` (signed) — requires `CODE_SIGNING_CERTIFICATE_THUMBPRINT` in `.env`; verify signing order binaries → MSI → sign MSI

### Documentation

- [x] T088 [P] Add an `## Event Log Anomaly Detection (evtspike)` section to `README.md` covering: what it is, how to enable (link to `quickstart.md`), the Security opt-in note including the canonical capability sentence (link to `quickstart.md` Path C). The canonical capability sentence MUST appear byte-identical to FR-031's canonical sentence across `spec.md`, `README.md`, and `quickstart.md` Path C: **"This enables the DrainCtl service to read the Security log, clear the Security log, manage audit policy, and set SACLs on this host."**
- [x] T089 [P] Add a short feature callout to `docs/index.html` in the same style as existing feature callouts
- [x] T090 [P] Write release notes for this bump in the style of `96e28a4` — substance, not just a changelog

### SC validation on real hardware

- [ ] T091 Run `quickstart.md` Path A end-to-end on a test RDSH; verify webhook receives spike, dashboard pill transitions, recent-spikes list populates; capture screenshots per `feedback_verify_visually.md`
- [ ] T092 (DROPPED — was standalone CLI Path B validation; standalone scope-reduced on 2026-04-17)
- [ ] T093 [P] Run `quickstart.md` Path C on a LocalSystem-default install: set `evtspike.security_channel_enabled: true` in config.json, restart the DrainCtl service, verify the subsystem log shows `Security` in the subscribed-channels list and no privilege error. Set back to `false`, restart, verify `Security` no longer appears.
- [ ] T094 Run the `stress-performance-workload` (spec.md §Measurement workloads) for ≥1 hour post-baseline-maturity: assert CPU <5% of one core (1-hour average), RSS <50 MB, and baseline write volume ≤13 MB/day extrapolated (SC-007).
- [ ] T094a Run a 1-hour idle comparison against a build with the feature config-disabled: assert baseline RSS delta ≤1 MB and CPU delta ≤0.1% (SC-006); assert no baseline file is written and no `EvtSubscribe` calls occur (FR-013).
- [ ] T094b Statistical transient-suppression test using the `normal-day-false-positive-workload`: inject N ≥ 1000 single-bucket transients at random times during a steady-state period; assert fewer than 1% of them produce notifications (SC-003).
- [ ] T094c Weekly-cycle SC-001 test using the `normal-day-false-positive-workload` in compressed time (the simulator MUST exercise production bucket/slot/maturity/fallback/confirmation/cooldown logic end-to-end; see spec.md §Measurement workloads): after baseline maturity, run one full weekly cycle and assert zero `event_spike` notifications.
- [x] T094d Automated target-isolation test for FR-025/SC-009: configure three `event_spike` notification targets (two healthy mocks + one that returns 500); trigger a spike; assert both healthy targets received the notification and detection continues unaffected.
- [ ] T095 Induce a genuine anomaly on a real channel; measure time from first anomalous bucket to notification firing — assert SC-002 (≤3 × 10 s = ≤30 s)
- [ ] T096 (Superseded by T094d — kept as an optional manual sanity check on real hardware; not a gate.)
- [ ] T097 Run the baseline-poisoning scenario from US2's Independent Test on real hardware with real events; verify second anomaly is still flagged (SC-004)
- [ ] T098 Subscription-retry test on real hardware: subscribe to a channel, then revoke the provider (e.g., `Stop-Service <provider>`); assert the channel transitions to `retrying` within 10 s, retry fires every 5 min, and after 12 failures (1 h) transitions to `failed`; dashboard `mature_channels` excludes the retrying channel. Re-enable the provider mid-test; assert the next retry transitions back to `subscribed` and baseline is preserved. Covers the mid-run channel loss edge case.

### Codex-review remediation (see `docs/reviews/codex-2026-04-19-remediation-plan.md`)

Added 2026-04-19 after a three-lens codex review (correctness/security/design) on the 85%-complete branch. Plan details, risks, rollbacks, and verification strategies are in the remediation-plan doc; this table is the executable task list.

- [x] T099 [US5] Phase A1 — `Subscribe` grows a `*sync.WaitGroup` param; the **caller** does `wg.Add(1)` before `go func() { defer wg.Done(); ... }()`. `Subsystem.initChannels` passes `&s.wg`. Add `TestStop_WaitsForSubscriberGoroutines` with an injected `SubscribeFunc` that holds a goroutine for ≥2 s after ctx cancel; assert `Stop()` blocks until it exits.
- [x] T100 [US5] Phase A2 — `handler.go` unconditionally: `evtSpikeSub = evtspike.New(...)`; `spikeCh = make(chan dc.SpikePayload, 16)`; wire `OnSpike` / `OnStatusChange` / `RegisterEvtSpikeStatusFunc`; call `Start(ctx)`. Drop the `sub == nil` early-return in `applyEvtSpikeConfigReload`. `Subsystem.Start` becomes idempotent w.r.t. `Enabled`: captures `startCtx` always; only launches subscriptions + loops when `cfg.Enabled == true`. `Subsystem.Reload` detects an `Enabled` diff first; `true→false` quiesces (cancel scoring + persistence, wait on `s.wg`, final `writeBaseline`); `false→true` reuses `s.startCtx` to launch. `channelSetChanged` stays gated on both-sides `Enabled == true`.
- [x] T101 [US5] Phase A2 tests — `TestReload_EnabledFalseStopsScoringLoop`, `TestReload_EnabledTrueStartsFromDisabled`, `TestReload_EnabledToggleFromSvcWiring` (via `applyEvtSpikeConfigReload`).
- [x] T102 [US5] Phase A3 — add `DisableSecurityPrivilege()` to `privilege_windows.go` (symmetric with `EnableSecurityPrivilege`, same error surface). Call it on opt-out path (`SecurityChannelEnabled true→false`) and on full `Enabled true→false`. **Do NOT** add a `Security` filter to `ResolveChannels` — the existing "AddedChannels=Security preserved, subscribe fails at runtime" contract is preserved in `spec.md`/`data-model.md`/`contracts/evtspike-config.md`; privilege restoration alone closes the bypass.
- [x] T103 [US5] Phase A3 tests — `TestResolveChannels_AddedSecurityPreservedWithFlagOff`, `TestReload_SecurityDisableRestoresPrivilege` (mock `PrivilegeFunc` records enable/disable), `TestReload_AddedChannelsSecurityAfterDisableFails` (real subscribe path fails with `ErrPrivilegeNotAssigned`, channel logged skipped, `Status.EnabledChannels` excludes it). Do NOT write `TestPrivilege_DisableHandlesNotAssigned` as a unit test against the real process token — flaky across LocalSystem/admin/standard-user runners; gate any real-token sanity check behind `//go:build integration`.
- [x] T104 [US5] Phase A4 — trim `Subsystem.Reload` docstring (`subsystem.go:150-163`) and `LoadBaseline` four-case restatement (`baseline.go:99-112`) to WHY-only. Keep the live-reload-matrix pointer and the data-model.md pointer; drop field-by-field WHAT restatement. `golangci-lint` clean; no tests needed.
- [ ] T105 [US5] Phase B2 **human decision, gates T106+T107**: keep T098 as-is (implement state machine) OR reduce to log-and-skip. Recommendation: implement per T098 spec; the infrastructure is cheap and the behavior matches operator expectation. If relax: capture in a short `spec.md` + `tasks.md` edit.
- [ ] T106 [US5] Phase B1 — per-channel `subscribed/retrying/failed` state machine. `SubscribeFunc` grows an async-loss callback (`loss func(err error)` or returned `<-chan error`). On async loss, supervisor goroutine transitions to `retrying`, retries `Subscribe` every 5 min up to 12 attempts with ±30 s jitter, then `failed`. Successful retry → `subscribed`; baseline state preserved. `Status.EnabledChannels` AND `Status.MatureChannels` both filter by `state == subscribed` (replace `len(s.channels)` at `subsystem.go:477` and the mature-loop at `subsystem.go:461-466`). Emit `detector_status` SSE on transitions. All new goroutines join `s.wg`. Requires T099 (wg ownership) and T100 (lifecycle) landed first.
- [ ] T107 [US5] Phase B1 tests — `TestSubscription_TransitionsToRetryingOnAsyncLoss` (primary lost-subscription test via the loss callback), `TestSubscription_TransitionsToRetryingOnInitialFailure`, `TestSubscription_TransitionsToFailedAfterTwelveAttempts`, `TestSubscription_RecoversToSubscribed`, `TestStatus_MatureChannelsExcludesRetrying`, `TestStatus_EnabledChannelsExcludesNonSubscribed`. Use injected `RetryTickSource` for fake clock.
- [x] T108 [P] [US1] Phase C1 — `email.go` STARTTLS + SMTPS: `net.Dialer{Timeout: 10*time.Second}`; **`conn.SetDeadline(now+30s)` BEFORE `smtp.NewClient`** (NewClient reads the greeting, so a deadline set after it still hangs on a silent server). Package-level `const`s for the two timeouts (no config knob yet). Test-only override hooks so suite doesn't grow by ~3 minutes. Tests: `TestSMTPStartTLS_DialTimeout` + `TestSMTPStartTLS_WriteDeadline` + `TestSMTPS_*` variants; assert via `errors.As` to a `net.Error` with `Timeout() == true` (not the concrete `*net.OpError`).
- [x] T109 [P] [US1] Phase C2 — `notify.go` event_spike branch rolls back `LastSpikeNotify` on send failure. Add `sync.Mutex` to `NotifyState` that protects **every** read/write/delete of `LastSpikeNotify` during `SendNotification` — not just the failure path. Capture `prev, hadPrev := m[spikeKey]` under the lock; on dispatch failure, `prev`-restore or `delete(m, spikeKey)` under the lock. Tests: `TestSendNotification_SpikeCooldownRolledBackOnWebhookFailure`, `TestSendNotification_SpikeCooldownConsumedOnSuccess`, `TestSendNotification_MultiTargetOneFailsOneSucceeds`.
- [x] T110 [P] [US1] Phase C3 — `detector.go`: raise `maxNBinIter` from 50_000 to 10_000_000 with an early-exit when `pmf` falls below `1e-300` (underflow short-circuit). `negBinQuantile` does NOT return `math.MaxInt32` on cap overflow — it signals overflow via second return value or error sentinel. Caller treats sentinel as "flood — cap at robust threshold, refuse baseline update for that bucket" (preserves FR-009 flood-poisoning protection). Rate-limited WARN per (host, channel) per hour. Tests: `TestNegBinTail_ExtremeCountDoesNotUnderflow`, `TestNegBinQuantile_ExtremeCapSignalsOverflow`, `TestDetector_FloodingAboveSoftCapStillFlags`, `TestDetector_FloodDoesNotPoisonBaseline`.
- [x] T111 [P] [US1] Phase C4 — `baseline.go` `LoadBaseline`: `os.Stat` the file first; reject if `Size() > 16<<20` with `.oversize-<ts>.bak` rename. Tighten schema guard to `bf.SchemaVersion != SchemaVersion` (reject zero, negative, future alike; all three → `.incompat-*.bak`). Post-load clamp per-channel `Alpha`/`Beta` to `[0.001, 1e9]` and `N` to `[0, 1e9]` with WARN on any clamp. Tests: `TestLoadBaseline_OversizeFileIsRenamed`, `TestLoadBaseline_ZeroSchemaVersionIsRenamed`, `TestLoadBaseline_NegativeSchemaVersionIsRenamed`, `TestLoadBaseline_ClampsOutOfBandFloats`.

### Bug B remediation: remote-host detector status propagation (added 2026-04-19)

**Discovered during T091 Path A on MDS-LDC1-GW1**: the remote host's log confirmed `evtspike=enabled` but the central dashboard (on MDS-LDC1-RDS12) rendered the pill as `EVT DISABLED`. Root cause: `internal/svc/handler.go:605-612` registers a pull function that returns a zero-value `evtspike.DetectorStatus{}` for any non-local host, and `CheckResult` (the remote→central report payload in `format.go:45-64`) carries no detector-status field. T037 wired detector_status SSE only on the host running the subsystem; multi-host propagation was never spec'd. This block closes that gap so the dashboard pill reflects reality for every registered host, with SSE push so clients see updates with no poll lag.

**Scope boundary**: the recent-spikes ring buffer for remote hosts is already propagated via `CheckResult.Spike *SpikePayload` (format.go:61) — T117 verifies this works end-to-end and splits out a follow-up task only if it doesn't.

- [x] T112 [P] [US1] Tests in `internal/dashboard/server_test.go` + `internal/dashboard/broker_test.go`: (1) `TestHandleReport_CachesRemoteEvtSpikeStatus` — POST a `CheckResult` with `EvtSpikeStatus` populated for a non-local host; assert subsequent `GET /api/evtspike/status?host=<remote>` returns the posted content; (2) `TestHandleReport_EmitsSSEOnRemoteStatusChange` — POST two reports with differing DetectorStatus, assert exactly one `detector_status` SSE fan-out; (3) `TestHandleReport_NoSSEOnRemoteStatusNoChange` — same DetectorStatus twice, assert zero SSE emissions; (4) `TestReportState_IncludesEvtSpikeStatus` in `internal/dashboard/client_test.go` — build a CheckResult with EvtSpikeStatus, marshal, assert the `evtspike_status` field is present and round-trips.
- [x] T113 [US1] Extend `CheckResult` in `format.go`: add `EvtSpikeStatus *evtspike.DetectorStatus \`json:"evtspike_status,omitempty"\`` alongside the existing `Spike *SpikePayload`. Field is optional; older central dashboards ignore it; newer central dashboards ignore absent fields on reports from older agents (backward-compatible both directions).
- [x] T114 [US1] Remote-agent side — in `internal/svc/handler.go` where `CheckResult` is assembled for the poll report (next to `Sessions` / `Performance` / `Spike` attachment), populate `result.EvtSpikeStatus` from `evtSpikeSub.Status()` when `evtSpikeSub != nil && fullCfg.EvtSpike.Enabled`. Null-safe: if the subsystem is nil or disabled, leave the field unset so the central dashboard derives `DISABLED` naturally.
- [x] T115 [US1] Central-dashboard side, cache + SSE — in `internal/dashboard/server.go` `/api/v1/report` handler: after unmarshaling, if `result.EvtSpikeStatus != nil` store it in a per-host map (new field `remoteEvtSpikeStatus map[string]evtspike.DetectorStatus` on `dashboardServer`, guarded by the existing `mu` mutex). Compare to the previously cached value; if differs (`!reflect.DeepEqual`), invoke `dashState.OnEvtSpikeStatus(status)` to reuse the existing broker fan-out path so connected SSE clients receive the remote transition with no poll lag. Expose a new `RemoteEvtSpikeStatus(host string) evtspike.DetectorStatus` method on `dashboardServer` for the pull path (T116).
- [x] T116 [US1] Central-dashboard side, pull function — in `internal/svc/handler.go:605-612`, change the non-local branch from `return evtspike.DetectorStatus{}` to `return dashState.RemoteEvtSpikeStatus(h)`. Requires `dashState` to expose `RemoteEvtSpikeStatus` (added in T115). `EvtSpikeStatusFunc` signature unchanged.
- [ ] T117 [US1] Re-run T091 Path A end-to-end on GW1 (remote host reporting to RDS12's dashboard). Verify: (a) RDS12 dashboard pill for GW1 transitions `DISABLED` → `TRAINING`/`HEALTHY` within one poll cycle of GW1's service starting; (b) inject spike on GW1 using the corrected recipe from `quickstart.md` §4; (c) webhook + email + ntfy targets receive the notification; (d) RDS12 dashboard's "Recent spikes" under GW1's card shows the entry (validates `CheckResult.Spike` propagation through the ring buffer). If (d) fails, file a follow-up task T118 to propagate ring-buffer writes for remote hosts; if (d) passes, close this block. Screenshot everything per `feedback_verify_visually.md`.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies.
- **Foundational (Phase 2)**: depends on Setup. **BLOCKS all user stories.**
- **US1 (Phase 3, P1)**: depends on Foundational. Ships MVP. Includes the Security channel opt-in path (T023a, T028, T012a) inline.
- **US2 (Phase 4, P1)**: depends on Foundational. Independent of US1's integration work — tests sit against the same detector.
- **Phase 5 (US3)**: DROPPED — see above.
- **US4 (Phase 6, P2)**: depends on Foundational.
- **US5 (Phase 7, P3)**: depends on Foundational + US1 (reload path calls into the live subsystem built by US1).
- **Phase 8 (Security Opt-In MSI)**: DROPPED — see above.
- **Polish (Phase 9)**: depends on all in-scope stories being complete (T083-T090 are prerequisites for release; T091-T098 are post-release validation).

### User Story Dependencies

- **US1 (P1, MVP)**: no story dependencies; depends only on Foundational.
- **US2 (P1)**: no story dependencies; shares the detector with US1.
- **US4 (P2)**: no story dependencies.
- **US5 (P3)**: depends on US1 (Reload acts on the running subsystem).

### Within Each User Story

- Tests (T018-T023, T043-T045, T047-T051, T059-T062, T066-T068) are OPTIONAL per the template but we've opted in; they should be written first and asserted to FAIL before the corresponding implementation tasks land (per `data-model.md` § Testing strategy from R9).
- Within a story: types → services/subsystem → endpoints → dashboard/UI → integration wiring.
- Commit after each task or coherent group; one task per commit is fine (CalVer patch advances per commit per `feedback_version_per_commit.md`).

### Parallel Opportunities

- **Within Phase 2 (Foundational)**: T004, T005, T006 must be sequential (same file, `config.go`); T007-T017 are all [P] — different files.
- **Within Phase 3 (US1)**: all Tests tasks T018-T023a run in parallel; implementation splits into service-side (T024-T028), notify (T029-T033), dashboard backend (T034-T037), UI frontend (T038-T041), logging (T042) — each touches different files.
- **Across stories** once Foundational is done: US1, US2, US4 can be parallel; US5 must wait on US1 pieces it depends on. Phases 5 and 8 are dropped.

---

## Parallel Example: User Story 1 Tests (run together before US1 implementation)

```bash
# All US1 contract + integration tests (different files, no shared state):
Task: "Contract test for event_spike payload in notify_test.go"
Task: "Contract test for /api/evtspike/status in internal/dashboard/server_test.go"
Task: "Contract test for /api/evtspike/spikes in internal/dashboard/server_test.go"
Task: "Integration test for spike confirmation in internal/evtspike/subsystem_test.go"
Task: "Integration test for 2-of-3 suppression in internal/evtspike/subsystem_test.go"
Task: "Integration test for broker detector_status emission in internal/dashboard/broker_test.go"
```

All six fail initially; implementation proceeds until all pass.

## Parallel Example: Phase 2 Foundational after T004-T006

```bash
# Detector lift + channel resolver + baseline persistence + status types — seven independent files:
Task: "Move cmd/evtspike/detector.go → internal/evtspike/detector.go"
Task: "Move cmd/evtspike/detector_test.go → internal/evtspike/detector_test.go"
Task: "Create internal/evtspike/channels.go with Defaults + ResolveChannels"
Task: "Create internal/evtspike/channels_test.go table tests"
Task: "Create internal/evtspike/subscriber_windows.go (lift subscribe func)"
Task: "Create internal/evtspike/spike.go with SpikePayload type"
Task: "Create internal/evtspike/baseline.go with types + WriteBaseline + LoadBaseline"
Task: "Create internal/evtspike/baseline_test.go"
Task: "Create internal/evtspike/status.go with DetectorStatus + DeriveState"
```

---

## Implementation Strategy

### MVP First (Foundational + US1 + US2)

1. Complete Phase 1: Setup (3 tasks, ~1 hour).
2. Complete Phase 2: Foundational (T004-T017, ~2-3 days with parallelism).
3. Complete Phase 3: US1 (T018-T042, ~3-5 days).
4. Complete Phase 4: US2 (T043-T046, ~half day — mostly tests).
5. **STOP and VALIDATE**: run `quickstart.md` Path A on a test RDSH; verify SC-001 through SC-004, SC-007, SC-009 (the ones the MVP needs to satisfy).
6. Ship an internal preview release if ready — US1+US2 is a deployable product. Admins can enable evtspike in the built-in mode and get early-warning notifications on all 53 non-Security channels.

### Incremental Delivery

1. Ship MVP (Foundational + US1 + US2) — built-in detector with notifications and dashboard.
2. Add **US4 (Warm Restart)** next — low-risk, high-operator-value addition that protects against the post-restart alert storm failure mode. Small task count (T059-T065).
3. Add **US5 (Live Reload)** — P3 polish on config experience (T066-T071).
4. **Finalize with Phase 9 (Polish)** — CalVer bump, docs, SC validation on real RDSH hardware.
(US3 and Phase 8 were dropped from MVP on 2026-04-17.)

### Parallel Team Strategy

Two developers after Foundational completes:

- **Dev A**: US1 (biggest and most cross-cutting — notify + dashboard + subsystem).
- **Dev B**: US2 + US4 (test-heavy, persistence-focused; minimal touch with US1 surface area).

Then:

- **Dev A**: Phase 8 (MSI + CA work — different skill set, can lean on the C# + WiX knowledge).
- **Dev B**: US3 (standalone binary + pipe extension).

Reconverge for US5 + Phase 9 together.

---

## Notes

- Every new `.go` file MUST carry `//go:build windows` — non-negotiable per CLAUDE.md.
- Every commit bumps CalVer patch per `feedback_version_per_commit.md`; each task is a natural commit boundary.
- `prek` pre-commit hook enforces gofmt + vet + golangci-lint + gitleaks — do not bypass with `--no-verify`.
- UI tasks (T038-T041, T071) require visual verification per `feedback_verify_visually.md` — screenshot before calling done.
- Help text in the dashboard must be factual only per `feedback_no_recommendations.md` — do not write prescriptive copy for the new pill or recent-spikes list.
- Email template edits should respect the recently-polished card layout from commits `bdf197e`, `38d6dae`, `96e28a4`, `0049cfa`, `bb8dcdd`.
- The Security opt-in installer copy (FR-031) is fixed wording — the contract tests in T075 verify it verbatim, do not paraphrase.
