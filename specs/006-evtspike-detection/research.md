# Phase 0 Research: evtspike

## Purpose

Resolve every open technical decision the spec and plan left unspecified, so Phase 1 can produce concrete data models, contracts, and quickstart without ambiguity. Each section follows a Decision / Rationale / Alternatives format.

---

## R1. Baseline persistence cadence

**Decision**: Write the baseline JSON file once every 15 minutes (slot-rollover aligned at the default cadence) and once on graceful shutdown. Also write whenever a channel's subscription outcome or a config-driven channel-list change occurs, so restart state reflects the current config.

**Rationale**:
- At the default 900s cadence, ~135 KB × 96 writes/day = ~13 MB/day of write volume, well within SSD endurance budgets and far below NTFS journal pressure.
- Slot-rollover alignment at the default cadence keeps the written state coherent — a slot is either fully updated or not, simplifying the on-disk schema (no "partial slot" flag needed).
- Worst-case data loss on a hard crash is 15 minutes of learning, which for a detector whose maturity threshold is ~one week is negligible (equivalent to <0.15% of a mature slot's observations).
- Shutdown-time flush captures the last sub-slot partial observations, so a clean restart has zero learning loss.

**Alternatives considered**:
- **Every bucket (10 s)**: 8,640 writes/day/host. Rejected — 60× the churn for <1% extra signal retention; battery/SSD hostile.
- **Every minute**: 1,440 writes/day. Rejected — still 15× the churn for marginal benefit; 15-min boundaries are a natural schema fit.
- **On shutdown only**: rejected — hard crashes would lose the entire session's learning, which is the failure mode the spec's SC-005 (no post-restart storm) specifically guards against.
- **Event-driven (only when a slot matures or an anomaly fires)**: rejected — irregular write cadence complicates reasoning about warm-restart freshness.

**Configurable?** Yes — `evtspike.persist_interval_seconds` with default 900, clamp [60, 86400]. Alignment semantics for non-default values are defined in `data-model.md` §PersistIntervalSeconds (multiples of 900 slot-aligned; divisors of 900 wall-clock aligned but not slot-coherent; other values not aligned at all — `ClampEvtSpike` logs a warning for non-multiples).

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

## R3. (Removed 2026-04-17)

Previously specified the pipe message schema for standalone → service spike forwarding. Dropped with the standalone CLI scope reduction. Section intentionally kept as a placeholder to preserve numbering in downstream references.

---

## R4. (Removed 2026-04-17)

Previously specified standalone CLI Windows service installation via `install-service` / `uninstall-service` / `run-service` subcommands. Dropped with the standalone scope reduction. Section intentionally kept as a placeholder to preserve numbering in downstream references.

---

## R5. Security channel opt-in mechanism

**Decision**: Add a single boolean field `evtspike.security_channel_enabled` (default `false`) to `EvtSpikeConfig` in `config.json`. When `true`, the subsystem at Start enables `SeSecurityPrivilege` on its own process token via `AdjustTokenPrivileges(SE_SECURITY_NAME, SE_PRIVILEGE_ENABLED)` and adds `"Security"` to the watched channel list via `ResolveChannels`.

No MSI component. No WiX `CustomAction`. No `LsaAddAccountRights` / `LsaRemoveAccountRights`. The MSI is unchanged by this feature.

**Rationale**:
- Verified (2026-04-17 via `whoami /priv` in a LocalSystem-context shell): LocalSystem's token already contains `SeSecurityPrivilege` in the Disabled state. A process running as LocalSystem can enable this privilege at any time via `AdjustTokenPrivileges` without any LSA grant.
- The research.md-v1 premise — that the MSI must call `LsaAddAccountRights` to grant the privilege — was incorrect for the default service account configuration. Granting to an account that already has the right in its kernel-assembled token is redundant.
- `LsaRemoveAccountRights(LocalSystem, SeSecurityPrivilege)` would be a **host-global** operation that removes the right from every LocalSystem-running service on the machine. There is no supported API to scope revocation to a single service running as a shared account. Documenting this blast radius as an acceptable risk, or implementing a confirm-on-revoke dialog, both leave the operational footgun in place.
- A config flag keeps the decision in the same place as every other evtspike knob (sensitivity, cooldown, channel list). Admins who are already familiar with DrainCtl's `config.json` pattern don't need to learn a new opt-in surface.

**Dedicated service account case**:
- Admins who have reconfigured DrainCtl to run under a dedicated account (non-LocalSystem) will find that account lacks `SeSecurityPrivilege` by default. If they set `security_channel_enabled: true` without first granting the right, `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED` and the subsystem logs a warning and skips the Security subscription. Other channels continue to work.
- The admin's path is to grant the right manually via `secedit /configure` or a group policy that assigns "Manage auditing and security log" to their chosen account. Documented in `quickstart.md` Path C.
- An MSI-based grant for dedicated accounts could be added in a future release if demand materializes; it is out of MVP scope.

**Alternatives considered**:
- **MSI `<Feature Id="SecurityEventLog">` with `LsaAddAccountRights` grant/revoke**: rejected — the LocalSystem default case makes the grant a no-op and the revoke a host-global footgun. The extra MSI surface adds no value over a config flag for the default account, and for dedicated accounts it introduces a separate cross-installer-and-service drift surface.
- **Always enable `SeSecurityPrivilege` at Start regardless of flag**: rejected — violates spec FR-001d ("MUST NOT enable `SeSecurityPrivilege` unless Security monitoring is explicitly opted in").
- **Marker file opt-in signal**: rejected — redundant with `config.json` and creates two sources of truth (MSI-managed marker vs admin-managed config).
- **Registry key opt-in**: rejected — same dual-source-of-truth problem, plus MSI/service registry drift (per `feedback_msi_cli_sync.md`).

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

State → pill mapping: `healthy` if feature enabled and at least one channel has a mature baseline; `training` if enabled but no channels mature yet (first week post-install); `disabled` if config-gated off; `error` if startup failed (e.g., couldn't subscribe to any channel at all — note: a missing/unreadable baseline file is NOT an error; the detector warns and starts fresh per FR-019).

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
- **Unit tests** (no Windows APIs, pure logic): detector math, robust-cap update, 2-of-3 confirmation, slot maturity, baseline JSON round-trip, corrupt-file handling (distinct from unreadable: see `data-model.md` §3), version-tag mismatch, config merge (default + disable + add + `security_channel_enabled`), channel-name case-insensitive dedup, `ClampEvtSpike`.
- **Integration tests** (fake subscriber, fake notifier): end-to-end detector → `OnSpike` → notification fired once per cooldown; live-reload of sensitivity without restart; warm-restart smoke (write baseline → re-read → verify baseline matches); mid-run subscription loss + retry transitions (`subscribed → retrying → subscribed/failed`).
- **Contract tests**: webhook payload matches schema in `contracts/event_spike-payload.md`; email template rendering (MJML → HTML asserts); ntfy priority mapping (warning→3, alert→4).
- **Manual tests** (documented in `quickstart.md`, not automated): subscribing to the real 54 channels on a test RDSH, inducing a spike with PowerShell `New-WinEvent`, verifying the dashboard pill transitions; enabling `security_channel_enabled` on a LocalSystem deployment and verifying the Security subscription succeeds.
- **SC validation workloads**: `normal-day-false-positive-workload` (SC-001, SC-003) and `stress-performance-workload` (SC-007) — deterministic fixtures under `specs/006-evtspike-detection/fixtures/`. The simulator MUST exercise production bucket/slot/maturity/fallback/confirmation/cooldown logic end-to-end; compressed-time is allowed for SC-001 provided the slot progression is preserved.

**Rationale**:
- `EvtSubscribe` doesn't mock well inside Go unit tests; that path goes into integration or manual tiers.
- Contract tests pin down the JSON shapes downstream integrators depend on — cheapest place to catch breakage before a release.
- The SC workloads are named and fixture-backed so independent testers can reproduce them.

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
