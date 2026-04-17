# Phase 1 Data Model: evtspike

Entities described here are the types that will exist in Go code. Each entity lists fields, JSON/on-wire representation when it has one, validation rules, and the state transitions the entity owns (if any). Types that are pure computation (e.g., `negBinUpperTail`) are out of scope — those belong in `detector.go`, not this doc.

---

## 1. `EvtSpikeConfig` (config block inside `config.json`)

Nested under the existing root `Config` struct. Edits `config.go`.

| Field | Type | JSON | Default | Clamp | Notes |
|-------|------|------|---------|-------|-------|
| Enabled | `bool` | `enabled` | `false` | — | Master switch. Feature is opt-in. |
| MinCount | `int` | `min_count` | `10` | [1, 10000] | Absolute floor — suppresses alerts below this count. |
| Threshold | `float64` | `threshold` | `1e-4` | [1e-9, 0.1] | Tail probability threshold. Anomalous iff `tail < threshold`. |
| CooldownMinutes | `int` | `cooldown_minutes` | `10` | [1, 1440] | Suppress repeat alerts for same (host, channel) within this window. |
| SlotMaturityObservations | `int` | `slot_maturity_observations` | `7` | [1, 100] | Observations per 15-minute slot before that slot is trusted (Q1 clarification). |
| PersistIntervalSeconds | `int` | `persist_interval_seconds` | `900` | [60, 86400] | Baseline write cadence (R1). See "Cadence alignment" below. |
| HalfLifeBuckets | `int` | `half_life_buckets` | `360` | [60, 10000] | Exponential forgetting half-life in 10s buckets (360 × 10 s = ~1 hour). |
| PriorStrength | `float64` | `prior_strength` | `60` | [1, 10000] | Weakly-informative prior strength in bucket-equivalents. **Hot-reload scope: new channels only** — changing this at runtime does NOT rewrite existing `GammaState.Alpha/Beta` (the prior's influence has already been absorbed via observations and fades over time). Affects channels observed AFTER the config change. Admins who want to re-prior a mature detector should delete the baseline file and restart instead. |
| MeanPerBucketPrior | `float64` | `mean_per_bucket_prior` | `0.1` | [0.0, 1000] | Prior expected events per 10s bucket. **Same hot-reload scope caveat as `PriorStrength`.** |
| BaselinePath | `string` | `baseline_path` | `""` → default path | — | Override for the baseline file location. Empty string means the default. |
| DisabledChannels | `[]string` | `disabled_channels` | `[]` | — | Names of default channels to skip on this host (FR-001a). Case-insensitive match. |
| AddedChannels | `[]string` | `added_channels` | `[]` | — | Extra channel names to subscribe (FR-001b). Case-insensitive dedup. |
| SecurityChannelEnabled | `bool` | `security_channel_enabled` | `false` | — | Opt-in for Windows `Security` channel monitoring (FR-029). When `true`, the subsystem at Start enables `SeSecurityPrivilege` on its own token via `AdjustTokenPrivileges` and adds `"Security"` to the watched list. LocalSystem (default service account) already has this privilege present (Disabled state). Dedicated service accounts need the right granted manually. |

**Default baseline path**: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-baseline.json`.

**Validation**: `ClampEvtSpike(cfg *EvtSpikeConfig)` runs during config load. Values out of range are clamped to the nearest endpoint and a warning is logged (pattern: `ClampRetention()`).

**Cadence alignment for `PersistIntervalSeconds`**:
- Default (`900`) and multiples of 900 (1800, 2700, 3600, ..., 86400): the ticker is aligned to slot-rollover boundaries (`:00, :15, :30, :45` at the default). Preserves R1's coherence rationale — a slot is either fully updated or not.
- Divisors of 900 (60, 180, 300, 450): the ticker is aligned to wall-clock sub-boundaries but MAY write mid-slot. R1 coherence is NOT guaranteed at these values. `ClampEvtSpike` logs a warning.
- Any other value: the ticker fires at a fixed offset from subsystem Start; no alignment; R1 coherence not guaranteed. `ClampEvtSpike` logs a warning.

**Live reload**:
- Most fields hot-apply to running detectors.
- `Enabled`: transition starts or stops the subsystem entirely (not a reload of a running subsystem).
- `BaselinePath`, `DisabledChannels`, `AddedChannels`, `SecurityChannelEnabled`: trigger a Stop+Start cycle of a running subsystem (channel-list change).
- `PriorStrength`, `MeanPerBucketPrior`: hot-apply, but affect only channels observed after the change (see row notes above).
- All other scalar fields: hot-apply, takes effect on the next scoring tick.

---

## 2. `Trigger` (new constant)

Edits `config.go`:

```go
const TriggerEventSpike Trigger = "event_spike"
```

Added to `ValidTriggers` set. Not added to `DefaultTriggers` — existing notification targets do not auto-receive spike events; admins must explicitly subscribe to avoid surprise notifications on upgrade.

---

## 3. `Baseline` (in-memory) and `BaselineFile` (on-disk)

Lives in `internal/evtspike/baseline.go`.

### `Baseline` (in memory, one per channel)

| Field | Type | Notes |
|-------|------|-------|
| Slots | `[96]GammaState` | Per-slot posterior stats. Slot index = `(hour*60 + minute) / 15`. |
| Global | `GammaState` | All-hours fallback used until per-slot maturity. |
| RecentFlags | `uint8` | Rolling 3-bit window for 2-of-3 confirmation. |
| LastAlert | `time.Time` | For cooldown suppression. |
| SubscriptionStatus | `string` | Runtime state: `"subscribed"` / `"retrying"` / `"failed"`. Not persisted. Distinct from `DisabledChannels` config — that's admin intent; this is runtime state. |
| RetryAttempts | `int` | Consecutive failed subscription attempts. Transitions to `failed` after `SubscriptionMaxRetries = 12` (1 hour at the 5-minute retry cadence). |
| LastRetryAt | `time.Time` | Last subscription retry timestamp; not persisted. |

### `GammaState`

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| Alpha | `float64` | `a` | Gamma shape; posterior mean = Alpha/Beta. |
| Beta | `float64` | `b` | Gamma rate. |
| N | `int` | `n` | Observation count (drives slot maturity). |

### `BaselineFile` (on-disk JSON)

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| SchemaVersion | `int` | `schema_version` | Starts at `1`. Bump on any breaking change. Version mismatch → discard and rebuild. |
| WrittenAt | `time.Time` | `written_at` | RFC 3339; informational, not load-gating. |
| Host | `string` | `host` | Hostname at write time; informational. |
| Channels | `map[string]ChannelState` | `channels` | Keyed by channel name. |

### `ChannelState` (per-channel)

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| Slots | `[96]GammaState` | `slots` | |
| Global | `GammaState` | `global` | |
| LastAlert | `time.Time` | `last_alert` | RFC 3339; zero-value means never. |

**RecentFlags is intentionally NOT persisted** — starting fresh after restart is fine; the 2-of-3 window re-fills within 30 seconds of running.

### Atomic write protocol

```
write to %ProgramData%\...\evtspike-baseline.json.tmp  (new file, full content)
fsync
MoveFileEx(tmp, final, REPLACE_EXISTING)
```

Matches `config.go`'s atomic-write pattern — same named-mutex guard, same `MoveFileEx` helper.

### Load protocol

1. File missing → log warning, initialize fresh baselines. **No rename.** (FR-019)
2. File unreadable (AV lock / transient EACCES) → log warning, initialize fresh baselines. **No rename** — preserves the file for a subsequent read once the lock clears, so a transient error does not permanently lose learned state. (FR-019)
3. JSON unmarshal error → log warning, rename corrupt file to `.corrupt-YYYYMMDD-HHMMSS.bak`, initialize fresh. (FR-019, edge case)
4. `SchemaVersion > 1` → log warning, rename to `.incompat-YYYYMMDD-HHMMSS.bak`, initialize fresh. (FR-019)
5. Valid → populate in-memory baselines for channels present in the file; missing channels (e.g., newly added to config) start fresh.

---

## 4. `ChannelList` resolver

Lives in `internal/evtspike/channels.go`. Not an entity per se — a helper. Included here because it drives what gets subscribed.

```go
func ResolveChannels(cfg EvtSpikeConfig) []string
```

Behavior:
1. Start with `Defaults` (the curated 54-channel list baked into the binary).
2. If `cfg.SecurityChannelEnabled == true`, add `"Security"`.
3. Remove any channel whose case-insensitive name appears in `cfg.DisabledChannels`.
4. Append `cfg.AddedChannels`.
5. Case-insensitive dedup (preserve first occurrence's casing).
6. Return.

**Rules**:
- Channels in `AddedChannels` that are already in `Defaults` are silently deduped, not errors. Makes the config forgiving.
- Channel names are strings — no structural validation here; invalid names surface as subscription errors at startup and log as skipped (FR-009).
- The Security channel opt-in is a single source-of-truth: `cfg.SecurityChannelEnabled`. No marker file. No MSI state. An admin who puts `"Security"` directly in `AddedChannels` without setting the flag will have the channel subscribed to but `SeSecurityPrivilege` will not be enabled — subscription will fail and be logged as skipped per FR-009.

---

## 5. `SpikePayload` (in-process + on-wire)

Lives in `internal/evtspike/spike.go`. Used three ways: notification dispatch, pipe message body, dashboard SSE event.

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| Host | `string` | `host` | Hostname. |
| Channel | `string` | `channel` | Event log channel name. |
| WindowStart | `time.Time` | `window_start` | RFC 3339. Start of the scoring window that triggered confirmation. |
| WindowEnd | `time.Time` | `window_end` | RFC 3339. |
| Observed | `int` | `observed` | Event count in the triggering window. |
| Expected | `float64` | `expected` | Posterior mean at scoring time. |
| TailProbability | `float64` | `tail_probability` | P(Y ≥ observed) from NegBin predictive. |
| ConfirmationCount | `int` | `confirmation_count` | How many of the last three windows were anomalous (always 2 or 3 at alert time). |
| FirstSeenAt | `time.Time` | `first_seen_at` | Timestamp of the first of the 2-of-3 windows that confirmed — lets ops see "spike started at X, confirmed at Y". |

**Invariants**:
- `Observed >= 0`, `Expected >= 0`, `0 < TailProbability < 1`, `ConfirmationCount in {2, 3}`.
- `WindowEnd == WindowStart + 10s`.
- `FirstSeenAt <= WindowStart`.

---

## 6. `DetectorStatus` (dashboard contract)

Lives in `internal/evtspike/status.go`. Published via REST + SSE per R7.

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| Host | `string` | `host` | |
| State | `string` | `state` | One of `"healthy"` / `"training"` / `"disabled"` / `"error"`. |
| EnabledChannels | `int` | `enabled_channels` | Count of subscribed channels. |
| MatureChannels | `int` | `mature_channels` | Count of channels with at least one mature slot (any of 96 slots has `N >= SlotMaturityObservations`). |
| ErrorReason | `string` | `error_reason,omitempty` | Populated only when `state == "error"`. |
| LastSpikeAt | `time.Time` | `last_spike_at,omitempty` | Zero-value → never. |

**State derivation** (in priority order — first matching rule wins):
| Condition | State |
|-----------|-------|
| `cfg.EvtSpike.Enabled == false` | `disabled` |
| `EnabledChannels == 0` (no channel subscriptions succeeded) | `error` |
| `MatureChannels * 2 >= EnabledChannels` (≥50% mature) | `healthy` |
| else (running, <50% mature) | `training` |

The threshold uses integer arithmetic (`MatureChannels * 2 >= EnabledChannels`) to avoid floating-point edge cases. The enum remains the four-state `healthy | training | disabled | error` — no new `partial` state is introduced; the transition from `training` to `healthy` is simply gated on more than one mature channel.

---

## 7. `RecentSpikeEntry` (dashboard ring buffer element)

Same fields as `SpikePayload` above plus a server-assigned `ID` (monotonic `int64`) for stable list keys in the UI.

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| ID | `int64` | `id` | Monotonic within a service run. |
| (embeds SpikePayload) | | | |

**Storage**: bounded in-memory ring buffer per host, capacity 20. Oldest dropped on overflow. Not persisted (FR-028). Cleared on service restart (acceptable — admin has notifications and logs).

---

## 8. (Removed 2026-04-17 — `PipeRequest` extension was for the standalone CLI, which was scope-reduced out of MVP.)

---

## 9. `NotifyState` extension

Edits `notify.go`:

```go
type NotifyState struct {
    LastAlertNotify       map[string]time.Time
    LastSessionWarnNotify map[string]time.Time
    LastPerfNotify        map[string]map[Trigger]time.Time
    LastSpikeNotify       map[string]map[string]time.Time   // NEW: target URL → (host|channel) → last sent
}
```

The double map key `target URL → "host|channel" → time` lets per-target repeat intervals apply per (host, channel), consistent with how existing triggers key on target + sub-context.

---

## Entity lifecycle diagram

```
┌────────────────────┐
│ EvtSpikeConfig     │ (read from config.json at load + live reload)
└─────────┬──────────┘
          │   (security_channel_enabled: true?)
          ▼
┌────────────────────┐
│ ChannelList        │  defaults ± disabled ± added (+ "Security" if flag is set)
└─────────┬──────────┘
          │ one Subscription + one Detector per channel
          ▼
┌────────────────────┐   writes every N seconds   ┌────────────────────┐
│ Detector/Baseline  │ ──────────────────────────▶│ BaselineFile       │
│ (in memory)        │ ◀────── reads at startup ──│ (on disk)          │
└─────────┬──────────┘                            └────────────────────┘
          │ on confirmed spike
          ▼
┌────────────────────┐
│ SpikePayload       │
└────┬──────┬────────┘
     │      │
     │      └────────────────────────────────────────────┐
     ▼                                                    ▼
┌────────────────────┐                       ┌────────────────────┐
│ SendNotification   │ (via TriggerEventSpike)│ Dashboard broker   │
│ (existing pipeline)│                        │ (SSE + REST)       │
└─────────┬──────────┘                        └────────────────────┘
          │                                            │
          ▼                                            ▼
   webhooks / ntfy / email                    status pill + recent spikes
```

All in-process within the DrainCtl service. No inter-process communication. Security opt-in at subsystem Start additionally enables `SeSecurityPrivilege` on the service's own token via `AdjustTokenPrivileges` (LocalSystem has this privilege present in its token by default; Disabled state).
