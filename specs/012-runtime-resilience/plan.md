# Implementation Plan: Runtime Resilience and Diagnostics (012)

**Branch**: `012-runtime-resilience` | **Date**: 2026-09-26 | **Spec**: [spec.md](./spec.md)
**Input**: Evidence-backed runtime-resilience specification.

## Summary

Make service recovery deterministic and inspectable, collect the next `drainctld.exe` crash locally without exfiltrating memory, turn stale-host derivation into durable one-shot offline/recovery events, and make RemoteFX persistence reject invalid data while preserving directional percentile meaning.

Technical approach in one sentence: retain SCM as the recovery authority, provision a protected WER LocalDumps folder through installer-owned configuration, add durable host-freshness epochs to the dashboard store/SSE broker, and normalize RemoteFX values before the existing telemetry pipeline.

## Technical context

**Language/version**: Go 1.26.2; WiX installer; Svelte dashboard client.
**Primary dependencies**: `golang.org/x/sys/windows`, existing `modernc.org/sqlite`, Windows SCM, and WER registry configuration.
**Storage**: Existing ProgramData product root, SQLite dashboard/telemetry database, and a new local-only `dumps` directory.
**Target platform**: Windows Server LocalSystem service (`drainctld.exe`) and authenticated browser dashboard.
**Testing**: Focused Go unit/integration tests around installer configuration helpers, freshness transitions, report ingest, and metric persistence; controlled service smoke test on an isolated Windows test VM; manual local dump workflow.
**Performance goals**: No per-heartbeat network call or dump-directory scan; one bounded local transition write only on state boundary; RemoteFX validation is O(number of optional fields).
**Constraints**:

- SCM is the sole automatic restart owner. Do not implement a second in-process restart loop.
- WER dumps are local opt-in diagnostic artifacts. Never upload, transmit, add to SQLite, or include dump bytes in events/logs/notifications.
- Do not change core report compatibility: all new report and SSE data is additive.
- Use the effective heartbeat interval already exposed by `DashboardConfig.HeartbeatInterval`; offline means exactly three intervals.
- Preserve the existing `server_update` contract and existing directional aggregation semantics.
- Do not place customer-specific hostnames, URLs, secrets, credentials, or dump data in the repository.

## Evidence and decision record

| Decision | Basis | Result |
|---|---|---|
| Preserve SCM recovery policy | Confirmed installer source already configures two 5-second restarts, third none, 1-day reset (`installer/LISSTech.DrainCtl.wxs`). | Assert and retain that configuration; service panics continue to surface as failed process exits. |
| Add WER LocalDumps | Confirmed v26.9.17 session-enumeration crash plus SCM 7034/1067 evidence ended stopped; no LocalDumps setup exists in the current tree. | Configure only `drainctld.exe`, mini dumps, 3 files, protected product-local directory. |
| Derive offline from server receive time | Existing `LastSeen` is written on dashboard report handling, and current threshold is `3 × interval` (`internal/dashboard/store.go`, `server.go`). | Use accepted report time, not agent timestamp; persist one-shot epoch state. |
| Treat zero FPS/quality as gap | Existing `checkResultSamples` already omits zero FPS/quality (`internal/dashboard/samples.go`). Evidence is sparse/nonzero FPS. | Make this an ingest invariant and do not create synthetic zero buckets. |
| Reject sentinel/outlier RemoteFX values | v26.9.47 evidence includes an encoding value near `2^32`; current PDH output is not bounded. | Validate per field before percentiles/persistence; retain valid siblings. |
| Preserve percentile direction | Existing `AggregateServicePercentiles` uses numeric P5 for higher-is-better FPS/quality and P95 for other fields. | Keep semantics and make labels/API explicit. |

**Inference caution**: Evidence does not establish why the v26.9.17 service remained stopped or prove the meaning of the near-`2^32` encoding value. The design treats both as diagnostic/validity risks, not as a root-cause claim.

## Constitution check

- **Windows-first delivery**: PASS. Runtime changes target Windows service, WiX setup, WER, PDH, and dashboard backend; all net-new Go files require `//go:build windows`.
- **Stable operator surfaces**: PASS. Existing reports and `server_update` remain compatible. New SSE types and any diagnostics endpoint are additive. Dumps are local-only and do not alter the network contract.
- **Tests and zero-noise verification**: PASS. Boundary and transition tests must be deterministic. The controlled crash test runs only on a dedicated test machine, never a production service.
- **Config and release discipline**: PASS. Reuse existing installer/config flow and product ProgramData root. No hand-maintained release version or parallel configuration system.
- **Operational observability**: PASS. Surface setup failures, recovery configuration, dump inventory metadata, freshness boundaries, and invalid RemoteFX drops without leaking sensitive values.

## Architecture

### 1. Service recovery state machine

```mermaid
stateDiagram-v2
    [*] --> Running
    Running --> Failure1: unexpected process exit
    Failure1 --> RestartDelay1: SCM schedules 5 s
    RestartDelay1 --> Running: start succeeds
    Running --> Failure2: second unexpected exit < 1 day
    Failure2 --> RestartDelay2: SCM schedules 5 s
    RestartDelay2 --> Running: start succeeds
    Running --> Failure3: third unexpected exit < 1 day
    Failure3 --> Stopped: no SCM action
    Failure1 --> Reset: 1 day without counted failure
    Failure2 --> Reset: 1 day without counted failure
    Reset --> Running: next failure is first
    Running --> ControlledStop: stop/shutdown/upgrade
    ControlledStop --> Stopped
```

- WiX `util:ServiceConfig` remains the installer source of truth for failure actions.
- `internal/svc/service_loop.go` deliberately re-panics after logging a recovered panic so SCM observes failure. Do not add a `recover` that returns a clean stop.
- SCM event IDs and dump metadata are complementary: service-control history explains recovery decisions; WER preserves the process evidence. Neither is automatically sent elsewhere.

### 2. WER LocalDumps lifecycle and security

**Provisioning path**:

1. Installer/repair creates `%ProgramData%\LISS Technologies\LISSTech DrainCtl\dumps`.
2. It applies a protected DACL: LocalSystem and local Administrators full control; removes broad inherited access.
3. It writes the per-executable WER key under `HKLM\Software\Microsoft\Windows\Windows Error Reporting\LocalDumps\drainctld.exe` with `DumpFolder`, `DumpType=1`, and `DumpCount=3`.
4. It logs success/failure as setup metadata. WER, rather than application code, owns dump creation and count-based retention.

**Runtime lifecycle**:

- Unexpected crash → WER may create mini dump locally → SCM applies configured action.
- Normal planned service stop → no crash dump expected.
- WER retention evicts oldest dumps after its count cap. Product code does not race this behavior.
- Operator copies a selected file to approved incident storage manually; the product never does it.

**Security boundary**:

- A dump can contain credentials, memory, and operator data. It must not inherit ProgramData's broad read permissions.
- Expose only inventory metadata to an authorized local/admin surface; never raw dump content through HTTP, named pipe, logs, notification payloads, or telemetry.
- Directory or registry provisioning failure is a visible degraded-diagnostics condition, not permission to use an unprotected fallback directory.

### 3. Stale-host transition architecture

Current source surfaces:

- `internal/dashboard/store.go`: `ServerState.Update` persists `LastSeen` and calls `OnUpdate`.
- `internal/dashboard/server.go`: `staleAfter()` calculates `missedHeartbeatLimit * HeartbeatInterval`.
- `internal/dashboard/handlers_servers.go` and `handlers_status.go`: derive `off`/offline for read responses.
- `internal/dashboard/sse.go` and `broker.go`: publish typed SSE envelopes.

Add a durable `host_freshness` record keyed by canonical hostname. It carries `last_accepted_at`, `state`, `offline_emitted_for`, and `recovered_emitted_for` (the report epoch timestamp). The update/report path and freshness evaluator serialize transitions through this record:

```mermaid
stateDiagram-v2
    [*] --> Unknown
    Unknown --> Fresh: first accepted report
    Fresh --> Fresh: accepted report updates epoch
    Fresh --> Offline: now >= lastAccepted + 3 * effectiveInterval
    Offline --> Offline: no accepted report; event already recorded
    Offline --> Fresh: accepted report; recovery event recorded
```

- `Unknown` has no accepted report and is not a missed-heartbeat state.
- A polling/status evaluator may derive Offline, but it atomically writes `offline_emitted_for` before broadcasting; repeated reads cannot repeat the event.
- Report acceptance atomically opens a new fresh epoch. If previous state was Offline, it records one recovery transition before/with the normal state update. Event ordering is documented and tested.
- Existing `server_update` remains emitted after state update. New `host_offline` / `host_recovered` events are additive envelopes.
- The evaluator runs on existing dashboard lifecycle cadence or a bounded freshness sweep; it must not perform an unbounded per-request scan. The exact cadence must make boundary recognition no later than one effective heartbeat interval after crossing threshold.

### 4. RemoteFX normalization pipeline

Current source surfaces:

- `internal/perfmon/collector.go`: reads PDH arrays and calls `AggregateServicePercentiles`.
- `internal/perfmon/snapshot.go`: defines P5-for-floor / P95-for-upper-tail directionality.
- `internal/dashboard/samples.go`: maps `PerfSnapshot` into retained telemetry samples.
- `internal/dashboard/handlers_metrics.go` and frontend chart consumers expose retained series.

Normalization boundary: immediately after PDH array retrieval and again at remote-report ingestion before a `PerfSnapshot` can reach `checkResultSamples`. This protects both local and remotely supplied values. Do not use clamping: an outlier is invalid and must disappear, not be turned into a plausible maximum.

| Field family | Valid range | Zero treatment | P95 meaning |
|---|---:|---|---|
| FPS | `(0, 240]` | inactive/missing | numeric P5 floor |
| Quality (%) | `(0, 100]` | inactive/missing | numeric P5 floor |
| Encode time (ms), RTT (ms) | `[0, 60000]` | valid | numeric P95 upper tail |
| Loss (%) | `[0, 100]` | valid | numeric P95 upper tail |
| Server/network skipped frames/sec | `[0, 1000000]` | valid | numeric P95 upper tail |

NaN, ±Inf, negatives, and values above the family maximum are invalid. Reject independently by field and increment a rate-limited diagnostic count tagged only with metric name/reason—not sample values or host identifiers where unnecessary.

### 5. Rolling upgrade and downgrade

| Direction | Expected behavior |
|---|---|
| New agent → old dashboard | New diagnostics fields are optional JSON additions; old server ignores them. Core report path remains accepted. |
| Old agent → new dashboard | Missing diagnostic fields leave optional metrics absent. The host uses existing `LastSeen` and health semantics. |
| New dashboard restart | Durable freshness epoch data prevents duplicate offline event for an already stale report epoch. |
| Upgrade stop/start | MSI `ServiceControl` controls stop/install/start. Do not depend on recovery action during planned upgrade. |
| Rollback | Preserve config/database/dumps. Older binary ignores additive freshness records and has no automatic dump deletion behavior. Existing state view still derives freshness from `LastSeen`. |

## Delivery phases

1. **Installer and WER diagnostics**
   - Extend `installer/LISSTech.DrainCtl.wxs` and/or existing installer custom actions with idempotent protected dump directory plus LocalDumps registry setup.
   - Add narrowly scoped Windows helpers/tests if WiX cannot safely express ACL registry work alone.
   - Confirm `drainctld.exe` service recovery declaration remains exactly two restarts then none.

2. **Freshness epochs and SSE transitions**
   - Extend dashboard persistence/migration and `ServerState` APIs.
   - Implement atomic report/recovery and fresh/offline transition emission.
   - Add SSE envelope/API documentation and frontend handling that refreshes state without breaking unrecognized events.

3. **RemoteFX validation**
   - Add a shared pure validation/normalization helper to protect local PDH and remote report paths.
   - Update sample extraction so absence remains absent and invalid values cannot reach durable telemetry.
   - Update OpenAPI/chart text for directional percentiles if current wording is incomplete.

4. **Operational proof and release readiness**
   - Execute isolated Windows service/SCM/WER smoke tests.
   - Exercise restart-safe freshness deduplication, mixed-version reports, and invalid-value matrix.
   - Update operator documentation with dump handling, rollout, and rollback steps.

## Observability contract

| Signal | Purpose | Sensitive data rule |
|---|---|---|
| `service_recovery_policy` startup/install record | Shows configured actions and reset period | No dump contents or tenant path. |
| `wer_localdumps_configured` / `wer_localdumps_setup_failed` | Shows availability of local diagnostics | Metadata only. |
| Local dump inventory | Indicates filename, size, creation time, count | Admin/local only; no byte content. |
| `host_offline` / `host_recovered` SSE | One state boundary per epoch | Canonical host, timestamps, threshold only. |
| `remotefx_value_dropped` | Detects provider/sentinel quality issues | Metric family and reason; rate-limit; no raw sensitive payload. |
| Existing SCM 7034/1067 and service logs | Correlate failure/restart outcome | Retain existing logging policy. |

## Required acceptance coverage

- Installer configuration inspection: service failure action values and WER LocalDumps values match exact requirements.
- SCM controlled fault scenario: first two failures restart after approximately five seconds; third remains stopped; planned stop does not trigger recovery.
- Protected dump scenario: induce controlled crash on an isolated test machine, observe a mini dump capped at three, inspect ACL, and observe no network traffic/product event containing dump bytes.
- Deterministic freshness boundary, duplicate-suppression, process-restart persistence, recovery, and interval-hot-reload tests.
- RemoteFX table-driven field/range/NaN/Inf/inactive-zero tests; report with one invalid sibling keeps valid samples.
- Directional percentile tests at local aggregation and retained API/chart serialization boundaries.
- Mixed-version report decode tests and rollback smoke test against a copy of existing state.

## Complexity tracking

| Potential complexity | Why needed | Simpler alternative rejected |
|---|---|---|
| Durable freshness epoch record | Prevents duplicate offline SSE after restart or repeated status reads | In-memory boolean loses correctness at restart and cannot identify a new report epoch. |
| WER configuration + protected directory | Captures OS-level process failure evidence with bounded retention | Application-written dumps require unsafe crash-path work and still do not cover all fault modes. |
| Shared RemoteFX validator | Both local PDH and remote report data can reach persistence | Validating only chart output leaves bad values in retained telemetry and alert/fleet paths. |
