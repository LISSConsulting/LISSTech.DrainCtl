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

- [ ] T001 Create empty `internal/evtspike/` package directory with a placeholder `doc.go` file containing `//go:build windows` and a one-line package comment, so subsequent parallel file adds don't race on directory creation
- [ ] T002 [P] Create empty `msi/SecurityOptIn.wxs` placeholder referenced by the Feature component (content filled in Phase 7)
- [ ] T003 [P] Update `CLAUDE.md` Architecture section with a single-line note about the new `internal/evtspike` subsystem (additive, non-breaking)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lift the POC detector into a reusable library and add the shared types all stories depend on. **Nothing in US1..US5 can start until this phase completes.**

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### Config + Trigger

- [ ] T004 Add `EvtSpikeConfig` struct to `config.go` (zero-value defaults, JSON tags per `contracts/evtspike-config.md`) and embed in `Config` as `EvtSpike EvtSpikeConfig \`json:"evtspike"\``
- [ ] T005 Add `ClampEvtSpike(cfg *EvtSpikeConfig)` helper to `config.go` (clamps per `data-model.md` §1 tables); call it from `LoadConfig` next to the existing `ClampRetention` call
- [ ] T006 Add `TriggerEventSpike Trigger = "event_spike"` constant to `config.go`; add to `ValidTriggers` map; DO NOT add to `DefaultTriggers` (explicit opt-in required by design)

### Detector library (lift from POC)

- [ ] T007 [P] Move `cmd/evtspike/detector.go` → `internal/evtspike/detector.go`; update package from `main` to `evtspike`; keep all logic unchanged; keep `//go:build windows` tag
- [ ] T008 [P] Move `cmd/evtspike/detector_test.go` → `internal/evtspike/detector_test.go`; update package; keep tests passing
- [ ] T009 [P] Create `internal/evtspike/channels.go` with the 54-channel default list (lifted verbatim from `cmd/evtspike/main.go` lines 32-109) exported as `var Defaults = []string{...}` and a `ResolveChannels(cfg EvtSpikeConfig, securityOptIn bool) []string` function implementing the merge rules from `data-model.md` §4
- [ ] T010 [P] Create `internal/evtspike/channels_test.go` with table tests for: default-only returns 54 items, disable removes by case-insensitive name, add appends, add of duplicate dedupes, security-opt-in adds `Security` when flag is true
- [ ] T011 [P] Create `internal/evtspike/subscriber_windows.go` by lifting the `subscribe(ctx, channel, query, counter)` function from `cmd/evtspike/main.go` lines 172-235; export as `Subscribe`; keep `//go:build windows`
- [ ] T012 [P] Create `internal/evtspike/spike.go` defining `SpikePayload` struct with JSON tags exactly matching `contracts/event_spike-payload.md` `spike` sub-object schema plus the top-level `Host` field used by the pipe message

### Baseline persistence

- [ ] T013 [P] Create `internal/evtspike/baseline.go` with `BaselineFile`, `ChannelState`, and (re-exported) `GammaState` types per `data-model.md` §3; `SchemaVersion = 1`
- [ ] T014 [P] Add `WriteBaseline(path string, bf *BaselineFile) error` to `internal/evtspike/baseline.go` using the existing atomic-write primitive from `config.go` (temp file + `MoveFileEx REPLACE_EXISTING`, named-mutex guard)
- [ ] T015 [P] Add `LoadBaseline(path string) (*BaselineFile, error)` to `internal/evtspike/baseline.go` that handles missing / unreadable / JSON-corrupt / incompatible-SchemaVersion cases by renaming to timestamped `.bak` and returning a fresh `BaselineFile` (per FR-019 + `data-model.md` §3 load protocol)
- [ ] T016 [P] Create `internal/evtspike/baseline_test.go` with round-trip test (write → read → deep-equal), missing-file fresh-return test, corrupt-JSON rename-and-rebuild test, incompatible-version rename-and-rebuild test

### Dashboard status types (shared)

- [ ] T017 [P] Create `internal/evtspike/status.go` defining `DetectorStatus` and `RecentSpikeEntry` structs per `data-model.md` §6 and §7, with a `DeriveState(enabled bool, startupErr error, matureChannels int) string` helper implementing the state table

**Checkpoint**: Foundation ready — detector library is package-internal and importable by both the in-service subsystem and the standalone CLI. User story implementation can now begin in parallel.

---

## Phase 3: User Story 1 - Early Warning on Anomalous Event Activity (Priority: P1) 🎯 MVP

**Goal**: Enable the detector inside the DrainCtl service, wire confirmed spikes into the existing notification pipeline, and expose the additive dashboard surface (status pill + recent-spikes list). After this phase, an admin who flips `evtspike.enabled: true` and subscribes a webhook to `event_spike` receives a notification on the first confirmed spike, and sees the detector on the dashboard.

**Independent Test**: On a test RDSH with the built-in mode enabled, inject 50 events on `Application` in a sustained burst. Within three scoring windows (≤3 minutes): webhook receives an `event_spike` POST naming the `Application` channel, the dashboard status pill shows `training` (first week) or `healthy` (after), and the detail view shows the spike in the recent-spikes list. No notification fires on a single transient burst that lasts <60 s.

### Tests for User Story 1

- [ ] T018 [P] [US1] Contract test in `notify_test.go`: marshal a `SpikePayload` via `SendNotification(..., TriggerEventSpike, "")`; assert the body matches `contracts/event_spike-payload.md` (top-level fields present, `spike` sub-object shape, HMAC header correct when `secret` configured)
- [ ] T019 [P] [US1] Contract test in `internal/dashboard/server_test.go`: `GET /api/evtspike/status?host=X` returns shape from `contracts/dashboard-sse-events.md`; 404 for unknown host; 401 without session
- [ ] T020 [P] [US1] Contract test in `internal/dashboard/server_test.go`: `GET /api/evtspike/spikes?host=X&limit=20` returns newest-first ring-buffer content; `limit=500` clamped to 50
- [ ] T021 [P] [US1] Integration test in `internal/evtspike/subsystem_test.go`: fake `Subscriber` feeds 50-event bursts in 2-of-3 windows; assert `OnSpike` invoked exactly once per cooldown with a `SpikePayload` matching invariants from `data-model.md` §5
- [ ] T022 [P] [US1] Integration test in `internal/evtspike/subsystem_test.go`: single-window transient burst (one window over threshold, next two clean) does NOT call `OnSpike` (confirms 2-of-3 suppression)
- [ ] T023 [P] [US1] Integration test in `internal/dashboard/broker_test.go`: state transition `disabled → training → healthy` emits exactly one `detector_status` SSE event per transition; same-state no-op emits zero

### Implementation for User Story 1

#### Service-side subsystem

- [ ] T024 [US1] Create `internal/evtspike/subsystem.go` with `Subsystem` struct owning channel subscriptions, detectors, baseline ticker, and an `OnSpike func(SpikePayload)` callback; implement `Start(ctx) error` (load baseline via `LoadBaseline`, subscribe per `ResolveChannels`, start scoring goroutine, start persistence ticker per R1) and `Stop()` (cancel context, flush baseline via `WriteBaseline`, wait for goroutines)
- [ ] T025 [US1] In `internal/evtspike/subsystem.go`, implement the scoring goroutine: 10-second ticker reading per-channel counters (already atomic.Int64), feeding `Detector.ObserveBucket`, and on confirmed spike building a `SpikePayload` and invoking `OnSpike`
- [ ] T026 [US1] Integrate subsystem into `internal/svc/svc.go`: construct when `cfg.EvtSpike.Enabled == true`; pass an `OnSpike` callback that invokes `drainctl.SendNotification(cfg.Notifications, notifyState, spikeCheckResult, TriggerEventSpike, "")` on the service's existing goroutine pool
- [ ] T027 [US1] In `internal/svc/svc.go`, add the subsystem to the graceful-shutdown sequence (call `Subsystem.Stop()` before returning from `Run`)
- [ ] T028 [US1] Detect the Security marker file at subsystem start: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike\security_enabled` — pass `os.Stat(marker) == nil` as the `securityOptIn` arg to `ResolveChannels`; log at INF level which channels were subscribed vs skipped (FR-024)

#### Notification pipeline integration

- [ ] T029 [US1] In `notify.go`, add `LastSpikeNotify map[string]map[string]time.Time` to `NotifyState` per `data-model.md` §9 (keyed by target URL → "host|channel" → last sent)
- [ ] T030 [US1] In `notify.go`, add an `event_spike` branch to `SendNotification`: reuse the existing envelope, populate the `spike` sub-object from a new `*SpikePayload` parameter (thread through the existing signature via a typed context struct or a new `SendSpikeNotification` helper — pick whichever keeps call sites clean); enforce per-target repeat interval using `LastSpikeNotify`
- [ ] T031 [US1] In `notify.go`, ensure severity comes from the target wiring (per-target `severity` field or existing severity-tag mechanism); no auto-escalation (FR-011a, FR-011b)
- [ ] T032 [US1] Update `email.go` (and/or its MJML template) to render `event_spike` using the card layout from commits `bdf197e`/`38d6dae`: subject emoji (⚠️ warning / 🚨 alert), preview text `"<channel>: <observed> events, expected ~<expected>. Tail probability <p>."`, card body with Observed-vs-Expected and Confirmation-Window rows
- [ ] T033 [US1] Add `ntfy` rendering branch for `event_spike` in `notify.go`: Title=subject, Message=message, Priority (3 for warning / 4 for alert), Tags=["evtspike", host, channel-basename]

#### Dashboard surface

- [ ] T034 [US1] In `internal/dashboard/broker.go`, add `detector_status` and `recent_spike` SSE event types to the broker dispatch; reuse existing fan-out and backpressure logic
- [ ] T035 [US1] In `internal/dashboard/server.go`, add `GET /api/evtspike/status?host=<hostname>` handler returning `DetectorStatus` per `contracts/dashboard-sse-events.md`; require authenticated session
- [ ] T036 [US1] In `internal/dashboard/server.go`, add `GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>` handler serving from an in-memory ring buffer (capacity 20 per host), backed by a new `internal/dashboard/spikestore.go` ring-buffer type; default limit 20, clamp to 50
- [ ] T037 [US1] In the subsystem's `OnSpike` call path (service-side), additionally push the spike to the dashboard's ring buffer and emit a `recent_spike` broker event; fire a `detector_status` broker event on state transitions in `Subsystem.Start` (state derivation per `DeriveState`)
- [ ] T038 [US1] In `ui/src/lib/api.js`, add `fetchEvtSpikeStatus(host)` and `fetchRecentSpikes(host)`; extend the existing SSE client to handle `detector_status` and `recent_spike` event types and update in-memory state
- [ ] T039 [US1] In `ui/src/lib/types.ts` (or `.d.ts`), add `DetectorStatus` and `RecentSpike` TypeScript types mirroring `data-model.md` §6 and §7
- [ ] T040 [US1] In `ui/src/components/ServerCard.svelte`, render a detector status pill next to the existing server pills using the four-state color map (healthy=green, training=amber, disabled=grey, error=red); consume from SSE-driven state
- [ ] T041 [US1] In `ui/src/components/ServerDetail.svelte`, add a "Recent spikes" section reusing the existing list styling (channel, time, observed vs expected); populate from `fetchRecentSpikes` on mount and refresh on `recent_spike` SSE events

#### Logging + ops

- [ ] T042 [US1] Add slog structured log lines at key lifecycle points in `internal/evtspike/subsystem.go`: `evtspike=start`, `evtspike=subscribed channel=X`, `evtspike=skipped channel=X reason=…`, `evtspike=confirmed_spike host=X channel=X observed=X expected=X tail=X`, `evtspike=baseline_written bytes=X`, `evtspike=stop`

**Checkpoint**: User Story 1 + 2 (via shared detector) fully functional end-to-end. An admin can enable the feature, inject a spike, see notification fire, see dashboard update. MVP is deployable at this point.

---

## Phase 4: User Story 2 - Resilient Baseline That Does Not Poison After Incidents (Priority: P1)

**Goal**: Prove and protect the robust-cap update path. The detector math from the POC already implements this; what's missing is test coverage against the full subsystem so future refactors can't silently break it.

**Independent Test**: Feed a fake subscriber a 30-minute sustained flood on one channel (e.g., 100 events per 60s window for 30 consecutive windows). After the flood ends, feed a single anomalous burst (e.g., 30 events when baseline would previously have been ~1). Assert: (a) the channel's posterior mean after the flood is within 2× of its pre-flood value (not 100×), and (b) the follow-up 30-event burst still triggers an `OnSpike` call.

### Tests for User Story 2

- [ ] T043 [P] [US2] Unit test in `internal/evtspike/detector_test.go`: feed 30 consecutive anomalous observations (y=100 with expected=1); assert `Slots[slot].Alpha/Beta` grows by ≤2× over the pre-flood value (robust cap effectiveness)
- [ ] T044 [P] [US2] Integration test in `internal/evtspike/subsystem_test.go`: simulate flood → pause → second smaller anomaly; assert `OnSpike` called for BOTH the flood (first confirmation) and the second anomaly (robust cap did not poison)
- [ ] T045 [P] [US2] Integration test in `internal/evtspike/subsystem_test.go`: flood-then-restart scenario — persist baseline mid-flood, reload, verify post-reload scoring of the second anomaly still flags (robust cap survives warm restart)

### Implementation for User Story 2

No new production code — the POC's `Detector.ObserveBucket` already caps update `y` at `negBinQuantile(0.99, alpha, beta)` before `ewmaUpdate`. These tests verify and lock in that behavior.

- [ ] T046 [US2] Audit `internal/evtspike/detector.go` for the robust-cap path (lines 105-111 in the POC); add a single-line comment tying it to SC-004 and US2's Independent Test, so future refactors don't inadvertently remove it

**Checkpoint**: US1 + US2 are both fully functional and regression-guarded. MVP is hardened.

---

## Phase 5: User Story 3 - Standalone Deployment for Servers Without Full DrainCtl (Priority: P2)

**Goal**: Make the standalone `evtspike` binary a first-class deployment option — runnable as a foreground process OR as a Windows service, forwarding confirmed spikes to a local DrainCtl service over the existing named pipe, and falling back to local logs when DrainCtl is unreachable.

**Independent Test**: On a host with DrainCtl running, launch `evtspike.exe install-service; Start-Service EvtSpike`. Inject a spike and verify the webhook on DrainCtl receives it with the expected `spike` payload. Stop DrainCtl. Inject another spike. Verify the standalone logs it locally without crashing. Start DrainCtl. Inject a third spike. Verify forwarding resumes without restarting `EvtSpike`.

### Tests for User Story 3

- [ ] T047 [P] [US3] Contract test in `internal/pipe/pipe_test.go`: marshal a valid `"spike"` `PipeRequest`; server handler decodes identical `SpikePayload`; asserts `PipeResponse{OK: true}`
- [ ] T048 [P] [US3] Contract test in `internal/pipe/pipe_test.go`: server that doesn't recognize `"spike"` returns `{OK: false, Error: "unknown command: spike"}`; client logs locally and does NOT retry
- [ ] T049 [P] [US3] Contract test in `internal/pipe/pipe_test.go`: malformed spike payload (missing Spike field, invalid `TailProbability=1.5`) → server returns error; service does not crash
- [ ] T050 [P] [US3] Integration test in `internal/pipe/spike_forward_test.go`: forwarder dials closed pipe (service not running) → falls back to local log path within 1 s, no retry loop
- [ ] T051 [P] [US3] Integration test in `internal/pipe/spike_forward_test.go`: service comes online after forwarder was already running → next spike succeeds (FR-017 reconnect-on-demand)

### Implementation for User Story 3

#### Pipe extension

- [ ] T052 [US3] In `internal/pipe/pipe.go`, add `Spike *SpikePayload` field to `PipeRequest` (pointer so existing commands omit it); add `"spike"` to the accepted `Cmd` values
- [ ] T053 [US3] In `internal/pipe/pipe.go` server dispatch, add handler for `Cmd == "spike"`: validate payload (non-nil + invariants from `data-model.md` §5), invoke the same `SendNotification` + broker path that the in-service subsystem uses (reuse the callback from T026), return `{OK: true}` on success
- [ ] T054 [US3] Create `internal/pipe/spike_forward_windows.go` with a `ForwardSpike(ctx, payload SpikePayload) error` client function that dials `\\.\pipe\drainctl`, writes the JSON request, reads the response, and returns `nil` on `OK: true` / `err` on everything else; one-shot per call (no connection pool), 2-second write timeout, no retry

#### Standalone binary productization

- [ ] T055 [US3] Rewrite `cmd/evtspike/main.go` to a thin entry point that reads `os.Args[1]`: no arg = foreground mode (load config, construct subsystem, `OnSpike` prints to stdout AND calls `ForwardSpike`); `install-service` = register service via `golang.org/x/sys/windows/svc/mgr`; `uninstall-service` = deregister; `run-service` = service entry point dispatched to `svc.Run`
- [ ] T056 [US3] Create `cmd/evtspike/service.go` with the `svc.Handler` implementation: on `Start`, construct the subsystem with the service's config, wire `OnSpike` to `ForwardSpike` (local log on failure); on `Stop/Shutdown`, call `Subsystem.Stop()`; report standard `Running`/`Stopped` states via `svc.Status`
- [ ] T057 [US3] In `cmd/evtspike/main.go`, have the standalone read its own `config.json` (default path `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-standalone.json` to avoid colliding with DrainCtl's main config); populate the same `EvtSpikeConfig` struct reused from T004
- [ ] T058 [US3] In `cmd/evtspike/service.go`, use a separate baseline file (`evtspike-standalone-baseline.json`) so running both built-in and standalone on the same host does not corrupt a single shared file (edge case from spec)

**Checkpoint**: Standalone CLI installable as a Windows service; forwards to DrainCtl over pipe; gracefully degrades when DrainCtl is absent. Covers FR-014, FR-015, FR-016, FR-017.

---

## Phase 6: User Story 4 - Warm Restart Without Retraining (Priority: P2)

**Goal**: Ensure service restarts preserve the learned baseline so the detector doesn't re-enter warm-up and doesn't fire an alert storm on restart.

**Independent Test**: Start the service. Feed enough observations that at least one slot matures (lower `slot_maturity_observations` to 1 for the test). Stop the service. Within 1 second of restart: (a) no `OnSpike` calls fire, (b) the dashboard pill shows `healthy` immediately (not `training`), (c) a genuine anomaly on the first post-restart scoring window still triggers `OnSpike`.

### Tests for User Story 4

- [ ] T059 [P] [US4] Integration test in `internal/evtspike/subsystem_test.go`: mature a slot, Stop+Start the subsystem, assert loaded baseline's `GammaState` values equal pre-stop values (bit-for-bit via `reflect.DeepEqual`)
- [ ] T060 [P] [US4] Integration test in `internal/evtspike/subsystem_test.go`: post-restart, inject a normal-rate bucket → NO `OnSpike`; inject an anomalous bucket → `OnSpike` fires on the first eligible confirmation window
- [ ] T061 [P] [US4] Unit test in `internal/evtspike/baseline_test.go`: write a baseline file with `SchemaVersion=99`, then `LoadBaseline` → returns fresh baseline, writes warning to slog, renames original to `.incompat-YYYYMMDD-HHMMSS.bak`
- [ ] T062 [P] [US4] Unit test in `internal/evtspike/baseline_test.go`: write a baseline file, truncate to half its bytes, then `LoadBaseline` → returns fresh baseline, writes warning, renames original to `.corrupt-YYYYMMDD-HHMMSS.bak`

### Implementation for User Story 4

Most persistence code is already in Foundational (T013-T015). Remaining tasks:

- [ ] T063 [US4] In `internal/evtspike/subsystem.go`, hook `LoadBaseline` at subsystem start: if a file is returned, seed the per-channel `Detector.Slots` and `Detector.Global` from `BaselineFile.Channels[name]`; for newly added channels not in the file, start with fresh state (do not error)
- [ ] T064 [US4] In `internal/evtspike/subsystem.go` `Stop()`, flush in-memory state to a `BaselineFile` and call `WriteBaseline` one final time (covers crash-less shutdown)
- [ ] T065 [US4] Verify the periodic persistence ticker (already in T024) calls `WriteBaseline` every `PersistIntervalSeconds` (default 900 = 15 min per R1); add a test in `subsystem_test.go` that a mock clock advance triggers the write

**Checkpoint**: Warm restart is verified and no-alert-storm on restart is guaranteed.

---

## Phase 7: User Story 5 - Admin Configuration and Visibility (Priority: P3)

**Goal**: Make the detector's runtime fully tunable via `config.json` without service restart for scalar tunables, and restart cleanly for channel-list changes. This story also finishes the dashboard configuration touchpoints that US1 built on.

**Independent Test**: With the service running and an active baseline: (a) change `threshold` from `1e-4` to `1e-3` in config.json; next scoring window uses the new value (verify via log line). (b) Change `disabled_channels` to add one channel; subsystem restarts within 1 s; dashboard pill transitions correctly; no alert storm. (c) Toggle `enabled: true → false`; subsystem stops cleanly, pill shows `disabled`; toggle back → `training` then `healthy`.

### Tests for User Story 5

- [ ] T066 [P] [US5] Integration test in `internal/evtspike/subsystem_test.go`: running subsystem → call `Reload(newCfg)` with a changed `threshold` → next `ObserveBucket` uses new value (inspect internal config)
- [ ] T067 [P] [US5] Integration test in `internal/evtspike/subsystem_test.go`: call `Reload(newCfg)` with a changed `disabled_channels` → assert subsystem performs Stop+Start (detectable via a "channel list changed" slog line), no alert storm
- [ ] T068 [P] [US5] Integration test in `internal/svc/svc_test.go`: file watcher picks up a mtime change on `config.json` → `EvtSpike.Reload` is called with the new config

### Implementation for User Story 5

- [ ] T069 [US5] In `internal/evtspike/subsystem.go`, implement `Reload(newCfg EvtSpikeConfig) error`: hot-apply the scalar fields from the live-reload matrix in `contracts/evtspike-config.md` (min_count, threshold, cooldown_minutes, slot_maturity_observations, persist_interval_seconds, half_life_buckets, prior_strength, mean_per_bucket_prior); detect channel-list or baseline-path changes and perform Stop+Start while holding an internal mutex
- [ ] T070 [US5] In `internal/svc/svc.go` config reload path (existing file watcher), add a branch that calls `evtspike.Reload(cfg.EvtSpike)` when that block's fields change; log the event at INF level
- [ ] T071 [US5] Update the dashboard's existing settings UI (not a new page, per FR-028) to surface `evtspike.enabled` as a simple toggle in the existing Settings modal — reuse existing API endpoints; NO per-channel or sensitivity editing controls (explicitly out of scope)

**Checkpoint**: Admin config surface is complete; live reload behavior is validated; dashboard exposes the toggle but no new visual real estate beyond US1's additions.

---

## Phase 8: Security Channel Opt-In (MSI Feature)

**Purpose**: Ship the installer opt-in for Security event log monitoring per FR-029 through FR-033. Depends on US1's subsystem reading the marker file (T028).

**Not a user story** — this is cross-cutting installer + privilege-management work that lights up the `Security` channel path.

### Tests

- [ ] T072 [P] MSI test harness invocation in `scripts/msi-test.ps1` (new file): `msiexec /i drainctl-*.msi /qn` → verify marker file ABSENT and `SeSecurityPrivilege` NOT granted to service account (via `LsaEnumerateAccountRights` from PowerShell)
- [ ] T073 [P] MSI test: `msiexec /i drainctl-*.msi ADDLOCAL=SecurityEventLog /qn` → verify marker file PRESENT and `SeSecurityPrivilege` granted
- [ ] T074 [P] MSI test: opt-in install → `msiexec /x` → verify privilege revoked and marker removed
- [ ] T075 [P] MSI test: opt-in install → modify with `REMOVE=SecurityEventLog REINSTALL=ALL REINSTALLMODE=omus` → verify privilege revoked; DrainCtl still installed

### Implementation

- [ ] T076 Create `msi/ca/EvtSpikeSecurityCA.csproj` — .NET Framework 4.8 managed custom action DLL project (per R5 + `contracts/msi-security-opt-in.md`)
- [ ] T077 Implement `GrantPrivilege` entry point in `msi/ca/EvtSpikeSecurityCA.cs`: lookup DrainCtl service account SID via `QueryServiceConfig`, open LSA policy handle with `POLICY_CREATE_ACCOUNT | POLICY_LOOKUP_NAMES`, call `LsaAddAccountRights` with `SeSecurityPrivilege`, close handle, return `ERROR_SUCCESS`
- [ ] T078 Implement `RevokePrivilege` entry point in `msi/ca/EvtSpikeSecurityCA.cs`: same SID lookup; `LsaRemoveAccountRights` with the specific privilege (NOT `allRights`); close handle
- [ ] T079 Replace placeholder `msi/SecurityOptIn.wxs` (from T002) with full component per `contracts/msi-security-opt-in.md`: `<Feature Id="SecurityEventLog" Level="1000">` containing the `SecurityOptInMarker` component that installs the `security_enabled` zero-byte marker file in `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike\`
- [ ] T080 In `msi/product.wxs`, reference the new feature + the custom-action binary; wire `GrantSeSecurityPrivilege` CA `After="InstallFiles"` with the condition from the contract; wire `RevokeSeSecurityPrivilege` CA `Before="RemoveFiles"` with the inverse condition
- [ ] T081 Update `justfile` `release` recipe to include the new CA DLL in the signing stage (sign before bundling into MSI, per the existing signing order)
- [ ] T082 Update `msi/*.wixproj` to reference the CA project and the new `SecurityOptIn.wxs` file

**Checkpoint**: MSI feature gates Security channel monitoring behind an informed-consent opt-in; privilege grant is reversible; default installs remain minimum-privilege.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Release plumbing, documentation, and SC-based validation on real hardware.

### Release plumbing

- [ ] T083 CalVer bump: update version in all seven places per CLAUDE.md (`drainctl.go`, `drainctl.rc`, `.psd1`, `.wixproj`, `README.md`, `CLAUDE.md`, `docs/index.html`)
- [ ] T084 Run `just resource` to recompile `.syso` after the `.rc` change
- [ ] T085 Run `just lint` and fix every warning — zero-tolerance noise policy per `feedback_clean_output.md`
- [ ] T086 Run `just all` (unsigned) — verify full build produces CLI + DLL + PS module + MSI + `evtspike.exe`
- [ ] T087 Run `just release` (signed) — requires `CODE_SIGNING_CERTIFICATE_THUMBPRINT` in `.env`; verify signing order binaries → MSI → sign MSI

### Documentation

- [ ] T088 [P] Add an `## Event Log Anomaly Detection (evtspike)` section to `README.md` covering: what it is, how to enable (link to `quickstart.md`), the Security opt-in note (link to `quickstart.md` Path C)
- [ ] T089 [P] Add a short feature callout to `docs/index.html` in the same style as existing feature callouts
- [ ] T090 [P] Write release notes for this bump in the style of `96e28a4` — substance, not just a changelog

### SC validation on real hardware

- [ ] T091 Run `quickstart.md` Path A end-to-end on a test RDSH; verify webhook receives spike, dashboard pill transitions, recent-spikes list populates; capture screenshots per `feedback_verify_visually.md`
- [ ] T092 [P] Run `quickstart.md` Path B: install standalone as service, verify pipe forwarding, stop DrainCtl, verify local-log fallback, restart DrainCtl, verify reconnect (SC-008: standalone first scoring pass <15 s of launch)
- [ ] T093 [P] Run `quickstart.md` Path C: install with `ADDLOCAL=SecurityEventLog`, verify marker + privilege + subscription to Security; uninstall feature, verify cleanup
- [ ] T094 Measure steady-state resource usage on a live RDSH for ≥1 hour: CPU, memory, baseline-write I/O — assert SC-007 (small single-digit % CPU, <50 MB memory)
- [ ] T095 Induce a genuine anomaly on a real channel; measure time from first anomalous bucket to notification firing — assert SC-002 (≤3 × 60 s = ≤3 min)
- [ ] T096 Deliberately misconfigure one webhook target (e.g., URL returning 401) while leaving others working; trigger a spike; verify other targets still receive the notification (SC-009)
- [ ] T097 Run the baseline-poisoning scenario from US2's Independent Test on real hardware with real events; verify second anomaly is still flagged (SC-004)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies.
- **Foundational (Phase 2)**: depends on Setup. **BLOCKS all user stories and Phase 8.**
- **US1 (Phase 3, P1)**: depends on Foundational. Ships MVP.
- **US2 (Phase 4, P1)**: depends on Foundational. Independent of US1's integration work — tests sit against the same detector.
- **US3 (Phase 5, P2)**: depends on Foundational + US1's service-side pipe handler (T053 needs the `SendNotification` integration wired by T026).
- **US4 (Phase 6, P2)**: depends on Foundational. Can run in parallel with US3.
- **US5 (Phase 7, P3)**: depends on Foundational + US1 (reload path calls into the live subsystem built by US1).
- **Phase 8 (Security Opt-In)**: depends on Foundational (marker file is consumed by `ResolveChannels` from T009) + US1 (T028 reads the marker). Can run in parallel with US3/US4/US5.
- **Polish (Phase 9)**: depends on all in-scope stories being complete (T083-T090 are prerequisites for release; T091-T097 are post-release validation).

### User Story Dependencies

- **US1 (P1, MVP)**: no story dependencies; depends only on Foundational.
- **US2 (P1)**: no story dependencies; shares the detector with US1.
- **US3 (P2)**: depends on US1's service-side notification integration (T026, T029-T033).
- **US4 (P2)**: no story dependencies.
- **US5 (P3)**: depends on US1 (Reload acts on the running subsystem).

### Within Each User Story

- Tests (T018-T023, T043-T045, T047-T051, T059-T062, T066-T068) are OPTIONAL per the template but we've opted in; they should be written first and asserted to FAIL before the corresponding implementation tasks land (per `data-model.md` § Testing strategy from R9).
- Within a story: types → services/subsystem → endpoints → dashboard/UI → integration wiring.
- Commit after each task or coherent group; one task per commit is fine (CalVer patch advances per commit per `feedback_version_per_commit.md`).

### Parallel Opportunities

- **Within Phase 2 (Foundational)**: T004, T005, T006 must be sequential (same file, `config.go`); T007-T017 are all [P] — different files.
- **Within Phase 3 (US1)**: all Tests tasks T018-T023 run in parallel; implementation splits into service-side (T024-T028), notify (T029-T033), dashboard backend (T034-T037), UI frontend (T038-T041), logging (T042) — each touches different files.
- **Within Phase 5 (US3)**: all Tests T047-T051 parallel; pipe changes (T052-T054) are one file chain; standalone binary work (T055-T058) is another file chain.
- **Across stories** once Foundational is done: US1, US2, US4, Phase 8 can be parallel; US3 and US5 must wait on US1 pieces they depend on.

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
3. Add **US3 (Standalone)** — widens deployment scope. Medium task count (T047-T058) but self-contained.
4. Add **Phase 8 (Security Opt-In)** — installer work (T072-T082). Can parallel with US3 if staffed.
5. Add **US5 (Live Reload)** — P3 polish on config experience (T066-T071).
6. **Finalize with Phase 9 (Polish)** — CalVer bump, docs, SC validation on real RDSH hardware.

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
