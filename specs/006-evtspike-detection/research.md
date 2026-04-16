# Phase 0 Research: evtspike

## Purpose

Resolve every open technical decision the spec and plan left unspecified, so Phase 1 can produce concrete data models, contracts, and quickstart without ambiguity. Each section follows a Decision / Rationale / Alternatives format.

---

## R1. Baseline persistence cadence

**Decision**: Write the baseline JSON file once every 15 minutes (on slot-rollover boundaries) and once on graceful shutdown. Also write whenever a channel's subscription outcome or a config-driven channel-list change occurs, so restart state reflects the current config.

**Rationale**:
- ~135 KB writes are negligible even on slow disks; 96 writes/day is ~13 MB/day of churn, well within SSD endurance budgets and far below NTFS journal pressure.
- Slot-rollover alignment keeps the written state coherent — a slot is either fully updated or not, simplifying the on-disk schema (no "partial slot" flag needed).
- Worst-case data loss on a hard crash is 15 minutes of learning, which for a detector whose maturity threshold is ~one week is negligible (equivalent to <0.15% of a mature slot's observations).
- Shutdown-time flush captures the last sub-slot partial observations, so a clean restart has zero learning loss.

**Alternatives considered**:
- **Every bucket (10 s)**: 8,640 writes/day/host. Rejected — 60× the churn for <1% extra signal retention; battery/SSD hostile.
- **Every minute**: 1,440 writes/day. Rejected — still 15× the churn for marginal benefit; 15-min boundaries are a natural schema fit.
- **On shutdown only**: rejected — hard crashes would lose the entire session's learning, which is the failure mode the spec's SC-005 (no post-restart storm) specifically guards against.
- **Event-driven (only when a slot matures or an anomaly fires)**: rejected — irregular write cadence complicates reasoning about warm-restart freshness.

**Configurable?** Yes — `evtspike.persist_interval_seconds` with default 900, clamp [60, 86400].

---

## R2. Event spike payload (webhook / ntfy / email)

**Decision**: Extend the existing notification payload with event-spike-specific fields; do not invent a new envelope. Concretely: the existing top-level `event` field becomes `"event_spike"`, existing fields (`host`, `timestamp`, `subject`, `status`, `version`) are reused, and a new `spike` object carries detector-specific data: `channel`, `window_start`, `window_end`, `observed`, `expected`, `tail_probability`, `confirmation_count` (how many of the last three windows were anomalous), `severity` (string echoed back from the admin's target config).

**Rationale**:
- Consistency with existing payload shape (`CheckResult` / drain / perf triggers) — downstream webhook consumers parse a single envelope.
- A sub-object `spike` avoids field-name collisions with `CheckResult` and makes it explicit that those fields are trigger-specific.
- All numeric fields are JSON numbers (not strings) so Grafana/Power BI style consumers can index directly.
- Email templates get two new MJML sections: a "what was expected vs observed" callout and a "which channel" badge, following the card layout recently polished in the `v26.105.49` commits.

**Alternatives considered**:
- **Entirely separate payload envelope**: rejected — forces integrators to special-case this trigger.
- **Flatten everything at top level**: rejected — would collide with the existing `message`, `status`, `changed_by` fields.

---

## R3. Pipe message schema for standalone → service spike forwarding

**Decision**: Extend `PipeRequest` (in `internal/pipe/pipe.go`) with a new `cmd = "spike"` value. Add an inline `Spike *SpikePayload` field (pointer so it's omitted from existing commands). Service handler injects the payload into the same `SendNotification` pipeline the in-service subsystem uses, so standalone and built-in are indistinguishable from the downstream perspective.

`SpikePayload` fields mirror the `spike` sub-object from R2 exactly, plus a `source_host` field (the standalone CLI's hostname — usually equal to the service's hostname, but kept explicit for future cross-host flexibility).

**Rationale**:
- Reuses the existing `\\.\pipe\drainctl` endpoint, existing authentication model (local-only named-pipe ACL), existing connection handling, existing JSON framing. No new pipe, no new listener, no new auth model.
- Asymmetric extension (pointer field, new `cmd` value) means older service binaries reading a newer request simply don't match the switch and return an error response — the standalone CLI then falls through to its local-log fallback (FR-016).
- Service-side dispatch uses the same `SendNotification` function, meaning: same debounce, same per-target repeat intervals, same severity routing, same email template.

**Alternatives considered**:
- **Separate pipe for spikes**: rejected — two listeners on the same service is unnecessary complexity; the existing broker already handles concurrency.
- **HTTP POST to the dashboard's existing REST API**: rejected — creates a dependency on dashboard being enabled, introduces TLS cert pinning for standalone-to-service talk, and goes across a different auth model. Pipe is the natural local IPC.
- **Write a file the service tails**: rejected — file-based IPC is brittle (crashes mid-write, log rotation races).

---

## R4. Standalone CLI Windows service installation

**Decision**: The standalone `evtspike` binary gains three subcommands:
- `evtspike install-service` — registers a Windows service named `EvtSpike` using `golang.org/x/sys/windows/svc/mgr`. The service runs as `LocalSystem` by default; admins who want a different account can reconfigure via `sc.exe config` after install.
- `evtspike uninstall-service` — removes the registered service.
- `evtspike run-service` — the service entry point; dispatches to `svc.Run` with the same detection loop the foreground mode uses.
- Foreground mode (no subcommand) remains the POC default, useful for CI/smoke and for "just poke at it" interactive runs.

Service installation is admin-triggered via PowerShell (`evtspike install-service`), not via MSI. This is intentional: the built-in DrainCtl subsystem is the primary path, and the standalone CLI is a tier-2 deployment. An MSI feature for the standalone would bloat the main installer; a separate standalone MSI is deferred to a future release if demand materializes.

**Rationale**:
- Matches the `kraken` / `squid` skill-driven ops style — admins drive install via CLI, not click-through.
- `golang.org/x/sys/windows/svc/mgr` is already used in `internal/svc`, so no new dependency.
- Service name `EvtSpike` is distinctive (not "LISSTech evtspike" or similar) to make it obvious this is an auxiliary service, not a fork of DrainCtl.

**Alternatives considered**:
- **New MSI for standalone**: rejected for MVP — too much installer work for a minority deployment.
- **Run standalone as a scheduled task**: rejected — no graceful shutdown signal path, and task-based processes lose the ETW/slog dual-sink setup.

---

## R5. MSI component for Security opt-in (privilege grant/revoke)

**Decision**: Add a WiX 5 `<Feature Id="SecurityEventLog" Title="Enable Security event log monitoring" Level="1000">` (Level > 100 so it is off by default in the typical UI). The feature owns a single Component containing:
- A marker file (`%ProgramData%\...\evtspike\security_enabled`) whose presence tells the service to add `Security` to its effective channel list at startup (read by `channels.go` during config merge).
- Two CA (`CustomAction`) steps scheduled in `InstallExecuteSequence`:
  - `GrantSeSecurityPrivilege` — scheduled `After="InstallFiles"`, condition `&SecurityEventLog=3 AND NOT Installed OR !SecurityEventLog=2` (i.e., running install of the feature). Calls a managed DLL that P/Invokes `LsaAddAccountRights` to add `SeSecurityPrivilege` to the DrainCtl service account SID.
  - `RevokeSeSecurityPrivilege` — scheduled `Before="RemoveFiles"`, condition fires when the feature is being uninstalled. Calls `LsaRemoveAccountRights`.

The CA DLL is written in C# (net48) — WiX 5 supports managed CAs via `CAQuietExec` or native. Either path works; C# + `LsaAddAccountRights` P/Invoke is the shortest. A fallback that shells to `ntrights.exe` is rejected because `ntrights.exe` is not installed by default on modern Windows Server.

Installer UI wording (exact copy, pulled into the Assumption section of the spec's FR-031): **"Enable Security event log monitoring. This grants the DrainCtl service the privilege to read the Security log, clear the Security log, alter audit policy, and set SACLs. Only enable if you understand and accept this expanded service capability on this host."**

**Rationale**:
- WiX Feature + Component is the natural expressive unit for an installer opt-in; the admin sees it on the feature-select page with the warning text visible.
- `LsaAddAccountRights` / `LsaRemoveAccountRights` is the only supported API for granting account rights programmatically; `secedit.exe` and GPO-based paths are either non-silent or not idempotent.
- Marker file (rather than MSI property alone) means the service can detect the opt-in at startup without reading the MSI registry — consistent with how `config.json` works (service reads its own config, not installer-time state).
- Opt-out revoking is symmetric: nothing the installer did persists after uninstall of the feature.

**Alternatives considered**:
- **MSI property + registry key read by the service**: rejected — registry drift between installer and service is exactly the pattern `feedback_msi_cli_sync.md` warns against.
- **Always grant the privilege, let config.json toggle Security channel**: rejected — violates the spec's FR-001d ("default install must not grant SeSecurityPrivilege").
- **Separate `evtspike-security.msi`**: rejected — worse admin UX than a feature inside the main MSI.

---

## R6. Subsystem lifecycle inside the DrainCtl service

**Decision**: Add an `EvtSpike` subsystem struct owned by `internal/svc`, constructed when `cfg.EvtSpike.Enabled == true`. It has:
- `Start(ctx)` — spins up event subscriptions, loads baseline from disk, starts scoring goroutine, starts persistence ticker.
- `Stop()` — cancels context, flushes baseline to disk, waits for goroutines.
- `Reload(newCfg)` — applies sensitivity / cooldown / persist-interval changes live; channel list changes require Stop+Start (guarded by a mutex in `svc.Run`).
- `OnSpike(spike SpikePayload)` — callback that calls `drainctl.SendNotification(..., TriggerEventSpike, "")` and publishes to the dashboard broker.

The existing `internal/svc.Run` already handles graceful shutdown, config reload via registry/file watcher, and similar subsystem lifecycle for the perf monitor. The EvtSpike subsystem slots into the same lifecycle pattern.

**Rationale**:
- Parity with the existing `PerformanceConfig`-driven perf subsystem inside the service — admins already understand the "subsystem enabled in config" pattern.
- Live reload for scalar tunables is cheap; channel-list reload is rare and a brief Stop+Start is acceptable.

**Alternatives considered**:
- **Separate goroutine with no subsystem struct**: rejected — loses reload discipline and makes testing harder.
- **Out-of-process, always using the standalone binary even for built-in mode**: rejected — violates the spec's FR-033 ("single process, single config, single baseline").

---

## R7. Dashboard surface (status pill + recent spikes)

**Decision**: Two additive pieces:
1. Status pill on `ServerCard.svelte` shows one of `healthy` / `training` / `disabled` / `error`, pulled from a new `GET /api/evtspike/status?host=<hostname>` endpoint on the dashboard server and also pushed over SSE as a `detector_status` event (so the existing `broker.go` pattern applies; no new transport).
2. Recent-spikes list on `ServerDetail.svelte` — last 20 confirmed spikes per server, fetched from `GET /api/evtspike/spikes?host=<hostname>&limit=20` and also streamed via SSE as `recent_spike` events. Backed by a bounded in-memory ring buffer on the server side (no new persistence — existing `MemAuditStore` pattern).

State → pill mapping: `healthy` if feature enabled and at least one channel has a mature baseline; `training` if enabled but no channels mature yet (first week post-install); `disabled` if config-gated off; `error` if startup failed (e.g., couldn't open baseline file, couldn't subscribe to any channel at all).

**Rationale**:
- Reuses the existing SSE broker (Feature 005 pattern — `project_sse_dashboard.md` memory notes SSE is in progress/planned). If SSE isn't yet in `main` at implementation time, falls back cleanly to the 30s poll.
- Ring buffer matches how the existing dashboard shows recent checks; no new storage concept for admins to learn.
- Ring buffer size of 20 per server is tiny (~20 × ~300 bytes JSON) — insignificant footprint.

**Alternatives considered**:
- **Persist recent spikes to disk so they survive restart**: rejected for MVP — FR-028 explicitly excludes persistent spike history; admins have notifications + logs for that.
- **Per-channel heatmap on server detail**: explicitly out of scope per FR-028.

---

## R8. Default detector tunables

**Decision**: Ship these defaults in the `EvtSpikeConfig` zero value (matching how `PerformanceConfig` does it in `config.go`):

| Knob | Default | Clamp range | Source |
|------|---------|------------|--------|
| `enabled` | `false` | — | Feature is opt-in. |
| `min_count` | `10` | [1, 10000] | POC-validated floor. |
| `threshold` | `1e-4` | [1e-9, 0.1] | POC-validated tail probability. |
| `cooldown_minutes` | `10` | [1, 1440] | POC-validated. |
| `slot_maturity_observations` | `7` | [1, 100] | Q1 clarification (≈ one week). |
| `persist_interval_seconds` | `900` | [60, 86400] | R1 above. |
| `half_life_buckets` | `360` | [60, 10000] | POC-validated exponential forgetting (~1 hour at 10s). |
| `prior_strength` | `60` | [1, 10000] | POC-validated. |
| `mean_per_bucket_prior` | `0.1` | [0.0, 1000] | POC-validated. |
| `baseline_path` | `""` (→ `%ProgramData%\...\evtspike-baseline.json`) | — | Sensible default path. |
| `disabled_channels` | `[]` | — | Q3: suppression list. |
| `added_channels` | `[]` | — | Q3: addition list. |

**Rationale**:
- All POC-validated values map 1:1 into config; no fresh tuning required for ship.
- Ranges are generous enough for extreme environments but catch obvious typos (e.g., threshold of 0.5 — too lax to be useful; threshold of 1e-30 — would never fire).
- `ClampEvtSpike()` helper mirrors the existing `ClampRetention()` / `ClampPerf()` pattern.

**Alternatives considered**:
- **Per-channel overrides in the default config**: rejected — FR-001c explicitly defers per-channel overrides post-MVP.

---

## R9. Testability strategy

**Decision**:
- **Unit tests** (no Windows APIs, pure logic): detector math, robust-cap update, 2-of-3 confirmation, slot maturity, baseline JSON round-trip, corrupt-file handling, version-tag mismatch, config merge (default + disable + add), channel-name case-insensitive dedup, `ClampEvtSpike`.
- **Integration tests** (fake subscriber, fake notifier): end-to-end detector → `OnSpike` → notification fired once per cooldown; live-reload of sensitivity without restart; warm-restart smoke (write baseline → re-read → verify baseline matches).
- **Contract tests**: webhook payload matches schema in `contracts/event_spike-payload.md`; pipe spike command round-trips via `internal/pipe`; MSI custom action grants and revokes (idempotent on second install).
- **Manual tests** (documented in `quickstart.md`, not automated): subscribing to the real 54 channels on a test RDSH, inducing a spike with PowerShell `New-WinEvent`, verifying the dashboard pill transitions.

**Rationale**:
- `EvtSubscribe` and `LsaAddAccountRights` don't mock well inside Go unit tests; those go into integration or manual tiers.
- Contract tests pin down the JSON shapes downstream integrators depend on — cheapest place to catch breakage before a release.

**Alternatives considered**:
- **Full-system end-to-end test in CI**: rejected for MVP — RDSH CI runners aren't a current thing; manual quickstart is acceptable.

---

## R10. Version / CalVer rollout

**Decision**: The feature ships in a single CalVer bump, coordinated via `just release`. Version touched in seven places per CLAUDE.md: `drainctl.go`, `drainctl.rc`, `.psd1`, `.wixproj`, `README.md`, `CLAUDE.md`, `docs/index.html`. After `.rc` edit, `just resource` regenerates `.syso`. Per the auto-memory note `feedback_version_per_commit.md`, version bumps every commit — so the feature lands in a stream of bumps, not a single jump.

**Rationale**: Simple adherence to the established release protocol. No special handling needed for this feature.

**Alternatives considered**: None viable — the project has a hard rule.

---

## Open questions deferred to implementation

These are decisions that don't need to be locked in before Phase 1 — they fall out of code naturally:

- **Event filter**: the POC uses `*[System[(Level<=4)]]`. Keep it. If an admin wants Level-5 (Verbose) events, `added_channels` is not the knob — this is post-MVP.
- **How "severity" on the notification target maps to email emoji**: reuses the existing email template's severity → emoji map; no new mapping table.
- **Pipe message JSON framing**: reuses the existing `PipeRequest`/`PipeResponse` framing; no new codec.

---

## Exit criteria

- [x] Every NEEDS CLARIFICATION in the plan's Technical Context resolved (there were none — all tech choices were concrete).
- [x] Every open-scope decision from the spec's Assumptions has a research entry.
- [x] Phase 1 can now produce `data-model.md` and `contracts/` without further open questions.
