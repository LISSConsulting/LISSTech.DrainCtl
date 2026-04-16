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
| PersistIntervalSeconds | `int` | `persist_interval_seconds` | `900` | [60, 86400] | Baseline write cadence (R1). |
| HalfLifeBuckets | `int` | `half_life_buckets` | `360` | [60, 10000] | Exponential forgetting half-life in 10s buckets. |
| PriorStrength | `float64` | `prior_strength` | `60` | [1, 10000] | Weakly-informative prior strength in bucket-equivalents. |
| MeanPerBucketPrior | `float64` | `mean_per_bucket_prior` | `0.1` | [0.0, 1000] | Prior expected events per 10s bucket. |
| BaselinePath | `string` | `baseline_path` | `""` → default path | — | Override for the baseline file location. Empty string means the default. |
| DisabledChannels | `[]string` | `disabled_channels` | `[]` | — | Names of default channels to skip on this host (FR-001a). Case-insensitive match. |
| AddedChannels | `[]string` | `added_channels` | `[]` | — | Extra channel names to subscribe (FR-001b). Case-insensitive dedup. |

**Default baseline path**: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-baseline.json`.

**Validation**: `ClampEvtSpike(cfg *EvtSpikeConfig)` runs during config load. Values out of range are clamped to the nearest endpoint and a warning is logged (pattern: `ClampRetention()`).

**Live reload**: All fields except `Enabled`, `BaselinePath`, `DisabledChannels`, `AddedChannels` can reload without restart. The three excepted fields require Stop+Start of the subsystem (guarded in `internal/svc`).

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

1. File missing → log warning, initialize fresh baselines (FR-019).
2. File unreadable → same as missing.
3. JSON unmarshal error → log warning, rename corrupt file to `.corrupt-YYYYMMDD-HHMMSS.bak`, initialize fresh (FR-019, edge case #6).
4. `SchemaVersion > 1` → log warning, rename to `.incompat-...bak`, initialize fresh (FR-019).
5. Valid → populate in-memory baselines for channels present in the file; missing channels (e.g., newly added to config) start fresh.

---

## 4. `ChannelList` resolver

Lives in `internal/evtspike/channels.go`. Not an entity per se — a helper. Included here because it drives what ever gets subscribed.

```go
func ResolveChannels(cfg EvtSpikeConfig, securityOptIn bool) []string
```

Behavior:
1. Start with `Defaults` (the curated 54-channel list baked into the binary).
2. If `securityOptIn == true` (i.e., the MSI marker file is present), add `"Security"`.
3. Remove any channel whose case-insensitive name appears in `cfg.DisabledChannels`.
4. Append `cfg.AddedChannels`.
5. Case-insensitive dedup (preserve first occurrence's casing).
6. Return.

**Rules**:
- Channels in `AddedChannels` that are already in `Defaults` are silently deduped, not errors. Makes the config forgiving.
- Channel names are strings — no structural validation here; invalid names surface as subscription errors at startup and log as skipped (FR-009).

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
- `WindowEnd == WindowStart + 60s`.
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

**State derivation**:
| Condition | State |
|-----------|-------|
| `cfg.EvtSpike.Enabled == false` | `disabled` |
| subsystem failed to start (couldn't open baseline file, 0 channels subscribed) | `error` |
| running but `MatureChannels == 0` | `training` |
| running and `MatureChannels > 0` | `healthy` |

---

## 7. `RecentSpikeEntry` (dashboard ring buffer element)

Same fields as `SpikePayload` above plus a server-assigned `ID` (monotonic `int64`) for stable list keys in the UI.

| Field | Type | JSON | Notes |
|-------|------|------|-------|
| ID | `int64` | `id` | Monotonic within a service run. |
| (embeds SpikePayload) | | | |

**Storage**: bounded in-memory ring buffer per host, capacity 20. Oldest dropped on overflow. Not persisted (FR-028). Cleared on service restart (acceptable — admin has notifications and logs).

---

## 8. `PipeRequest` extension

Edits `internal/pipe/pipe.go`:

```go
type PipeRequest struct {
    Cmd         string        `json:"cmd"`                      // existing: "status", "history", "servers", "remove-server", NEW: "spike"
    Limit       int           `json:"limit,omitempty"`
    ChangesOnly bool          `json:"changes_only,omitempty"`
    Hostname    string        `json:"hostname,omitempty"`
    Spike       *SpikePayload `json:"spike,omitempty"`          // NEW: populated when Cmd == "spike"
}
```

Response shape unchanged — standalone CLI checks `PipeResponse.OK` and falls back to local log if `!OK` (FR-016, FR-017).

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
          │
          ▼
┌────────────────────┐   ┌─────────────────────┐
│ ChannelList        │◀──│ MSI marker file     │ (Security opt-in)
│ (defaults + diffs) │   └─────────────────────┘
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

Standalone-mode variant: `SpikePayload` → `internal/pipe` client (`"spike"` cmd) → service-side handler → exact same `SendNotification` + broker path.
