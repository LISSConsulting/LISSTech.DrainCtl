# Event-Log Anomaly Detection: Status Semantics, Defaults Analysis, and Tuning Data

**Branch**: `sprint/evtspike-controls-20260924` | **Date**: 2026-09-24
**Status**: Owner-selected seven-day warm-up policy implemented.
**Related**: spec `006-evtspike-detection/spec.md`, `006-evtspike-detection/data-model.md`,
`006-evtspike-detection/contracts/evtspike-config.md`.

## Why this document exists

A common operator question is: *"I just enabled the event-spike detector on a
fresh server — why does it show **Healthy** within a few hours?"* The former
answer was that status expressed channel readiness, not baseline age. The
owner selected a durable seven-day warm-up policy: detection continues scoring
and emits confirmed spikes immediately, while the public state remains
**TRAINING** until seven elapsed days and the existing channel-readiness gate
both pass. This document preserves the defaults analysis and records the
policy and migration evidence used for the cutover.

---

## 1. Status semantics

The detector surfaces a small status block on every host. The two fields
that drive the **Healthy** badge are:

| Field | Source | What it actually means |
|-------|--------|------------------------|
| `EnabledChannels` | `internal/evtspike.Status()` | Channels currently subscribed and in `StateSubscribed` (plan §B1). Channels in `StateRetrying` / `StateFailed` are excluded, so the badge reflects **operational capacity**, not configuration. |
| `MatureChannels` | `detectorMature(d, SlotMaturityObservations)` | Channels whose detectors have crossed `SlotMaturityObservations` observations for their time-of-day slot. The steady default is 630: at up to 90 observations per 15-minute slot visit, this requires seven visits. |

The post-release EventSpike sample shows **81% of repeats arrive within one
hour** and a busy slot receives **90 observations/day**. The steady values
therefore use a 60-minute cooldown, 630 slot observations, and a 630-bucket
half-life. This holds repeat notifications to the observed burst window while
requiring a week of same-slot observations before slot-specific scoring is
trusted.

**Implication**: "Healthy" is a *positive signal that nothing has tripped the
detector yet*, not "the detector has learned what normal looks like." The
status pill carries no "still warming up" indicator today. The first time the
detector fires is the first authoritative test of its tuning on that host.

This is consistent with the spec's §FR-009 ("per-channel subscribe errors are
logged and the channel is skipped") — the badge is intentionally honest about
running capacity but intentionally quiet about training convergence.

---

## 2. Default candidates and their evidence

All knobs below are already exposed through the new dashboard UI. The
question is *only* whether to ship different `clampIntField` / `clampFloatField`
defaults in `ClampEvtSpike`. The four candidates on the table, with the
evidence backing each:

### Candidate A — "Ship what 006 shipped" (status quo)

| Knob | Default | Source |
|------|---------|--------|
| `MinCount` | 10 | `DefaultEvtSpikeMinCount` |
| `Threshold` | 1e-4 | `DefaultEvtSpikeThreshold` |
| `CooldownMinutes` | 60 | 81% of observed repeats arrived within one hour |
| `SlotMaturityObservations` | 630 | 90 observations/day × seven same-slot visits |
| `HalfLifeBuckets` | 630 | Seven-visit steady learning horizon |
| `PriorStrength` | 60 | `DefaultEvtSpikePriorStrength` |
| `MeanPerBucketPrior` | 0.1 | `DefaultEvtSpikeMeanPerBucketPrior` |
| `PersistIntervalSeconds` | 900 | `DefaultEvtSpikePersistIntervalSeconds` (slot rollover) |

**Evidence**: these are the values POC validation used and were kept when the
detector moved into the product. They are the defaults any existing
production deployment has been running on for months; changing them is a
behaviour change for every existing install.

**Risk**: spec authors and operators were never asked which *target workload*
shape the defaults suit. The POC environment was an internal lab; production
RDHS hosts see a different event-rate distribution, and the values were never
back-tested against a corpus of real burst data.

### Candidate B — "Vigilant" (tighter, fewer false negatives)

| Knob | Change from A |
|------|---------------|
| `MinCount` | 20 (was 10) |
| `Threshold` | 1e-5 (was 1e-4) |
| `CooldownMinutes` | 5 (was 10) |
| `SlotMaturityObservations` | 90 (unchanged) |
| `HalfLifeBuckets` | 480 (was 360) — slower forgetting |

**Evidence**: matches the "Vigilant" preset shipped in the new dashboard
modal. Designed for noisy multi-tenant RDSH where missing a real burst is
more painful than paging on a noisy channel. Drawback: false-positive rate
goes up; cooldown of 5 minutes means a real event storm pages repeatedly
within an operator's shift.

**Risk**: no production data backs the 1e-5 tail threshold; an operator on
this preset will almost certainly want to raise `MinCount` per-channel
shortly after enablement, which the UI does not yet support.

### Candidate C — "Chill" (looser, fewer false positives)

| Knob | Change from A |
|------|---------------|
| `MinCount` | 5 (was 10) |
| `Threshold` | 1e-3 (was 1e-4) |
| `CooldownMinutes` | 15 (was 10) |
| `SlotMaturityObservations` | 60 (was 90) |
| `HalfLifeBuckets` | 240 (was 360) — faster forgetting |

**Evidence**: matches the "Chill" preset shipped in the new dashboard
modal. Designed for low-volume environments (test labs, dev VMs) where
production burst rates are an order of magnitude smaller and the default
threshold would never fire. Drawback: a real burst on a default channel
becomes nearly invisible until the operator manually tightens per-channel.

### Candidate D — "Workload-adaptive" (owner-selected warm-up policy)

The selected policy is deliberately limited to status semantics:

- Day 0–7 after enablement: subscribe, collect, score, and emit confirmed
  spikes normally; public state is `training`.
- Day 7+: transition to `healthy` only when at least half of subscribed
  channels meet the existing maturity threshold.
- `warmup_started_at` lives in `baseline.json`, is written immediately at
  start/reset, and is therefore restart-safe. It does not auto-tune numeric
  thresholds.

For version-1 baselines, the migration retains learned posteriors and infers
the start from persisted evidence in this order: existing durable start; a
`written_at` at least seven days old; a slot with at least 630 observations
(90 ten-second windows per 15-minute slot per day for seven days); otherwise
the non-future `written_at`, then current start time. The inferred value is
persisted immediately. This keeps a mature existing installation healthy only
when its baseline contains evidence of age and conservatively trains the rest.

---

## 3. What data would settle the choice

To choose between A, B, C, and D with evidence instead of intuition, we
need a corpus of (host, channel, time-bucket, count) tuples from real
deployments, plus a labelled set of "true spike" intervals. Sources we could
draw from, with their tradeoffs:

| Source | What it gives us | Privacy risks | Collection cost |
|--------|------------------|---------------|-----------------|
| DrainCtl telemetry store (`drainctl.db`) | Per-host 5-min rollups of perf metrics + drain transitions; **not** evtspike counts today | Low — already aggregated, already on-disk in customer environments | Trivial: query existing tables |
| DrainCtl `baseline.json` snapshots | Per-slot Gamma posteriors (`Alpha`, `Beta`) for every subscribed channel on every host | Medium — slot-level event rates indirectly describe workload shape but not specific events | Low — copies from customer data dirs (subject to consent) |
| DrainCtl service logs (slog) | `evtspike=spike_dropped`, `evtspike=reload_*`, `evtspike=channel_list_changed` events | Low — counts, channel names, no event bodies | Low — already structured logs |
| Windows Event Log (raw `.evtx`) | True event payloads, including security IDs and message text | **High** — payloads contain usernames, IP addresses, ticket information | High — requires customer DPA + scrubbing pipeline |

**Recommended minimum dataset** for evidence-based default selection:

- A handful of pilot customers opt in to share `baseline.json` snapshots +
  `drainctl.db` 5-min rollups for one quarter.
- Each snapshot yields `(Alpha, Beta, N)` per (channel, 15-min-slot) on a
  production host. From those, derive empirical distributions of bucket
  counts and posterior widths.
- Cross-reference spike_dropped + reload_* events to identify false
  positives in retrospect (any operator ack within 1 minute of an alert
  fires a likely-FP signal in most pipelines; absent such acks, alerts
  were probably true positives).
- Iterate default candidates against this dataset: pick the candidate
  whose 5%/hour false-positive rate at the median workload best matches
  operator expectations.

**Until that corpus exists, A remains the safe choice.** B and C are
available as presets so operators can opt into them per-deployment without
risking other installs.

---

## 4. Privacy considerations for tuning data

Sharing any of the data sources above crosses a privacy line. The
mitigations, in order of strength:

1. **Aggregate, then share.** Push the math to the customer environment.
   DrainCtl would compute `(channel, 99th-percentile count, posterior-width)`
   locally and ship only those numbers — no event payloads, no per-bucket
   counts. This is the standard approach for product telemetry and matches
   what other observability vendors do for baseline-tuning data.
2. **Hash customer identifiers.** Replace hostnames with stable one-way
   hashes before they leave the customer environment. Channel names are
   not customer-specific on the curated default list (`Application`,
   `System`, etc.) so they can stay in cleartext.
3. **Strip Windows Event Log fields.** If raw events are ever needed,
   pre-process them on-host to drop `EventData.UserData`, IP addresses,
   and any field marked `<sensitive>` in the event manifest.
4. **DPA + opt-in.** No collection without a signed Data Processing
   Agreement that names the collected fields and the retention period.
   Operators must be able to disable collection site-wide.

The current code does **none** of this automatically — there is no
telemetry-export path today. If we add one in future, it must land behind
a feature flag (default off) and audit-log every export.

---

## 5. What landed in this sprint (and what did not)

### Shipped

- **Backend** (`config.go`):
  - `EvtSpikeConfigPatch` struct + `ValidateEvtSpikePatch`,
    `ApplyEvtSpikePatch`, `IsEvtSpikePatchEmpty`, `UpdateEvtSpike` —
    partial-update API mirroring the bulk PUT convention.
  - `UpdateEvtSpikeEnabled` retained as a thin wrapper for legacy callers.
- **Dashboard** (`internal/dashboard/handlers_settings.go`):
  - GET handler returns every operator-safe field via `buildEvtSpikeView`.
  - PUT handler validates and persists the full patch; out-of-range
    values return 400 with a clear field-level message instead of being
    silently clamped.
  - `baseline_path` stays admin-only — never appears on the wire.
- **Agent remote-fetch contract** (`internal/dashboard/client.go`,
  `internal/svc/check.go`):
  - `RemoteEvtSpike` is now an alias of the full operator-safe view.
  - `overlayEvtSpikeFromRemote` clamps every knob on the agent side so
    a misconfigured dashboard cannot inject out-of-range values.
  - Channel-list changes trigger the existing `channelSetChanged` restart;
    scalar knob changes hot-reload per `internal/evtspike/subsystem.go`
    `Reload`.
- **Warm-up status** (`internal/evtspike`):
  - `baseline.json` schema 2 stores `warmup_started_at`; startup writes it
    immediately and baseline reset starts a fresh durable seven-day clock.
  - Version-1 baselines retain their learned channels and use the documented
    evidence-based migration fallback.
  - Scoring, confirmation, notifications, REST status, and SSE spike events
    stay active while the public state is `training`.
- **OpenAPI** (`internal/dashboard/openapi.yaml`):
  - `EvtSpikeSettings` (GET) and `EvtSpikeSettingsPatch` (PUT) schemas.
  - `DetectorStatus` and `GET /evtspike/status` document the durable
    warm-up field and active-scoring semantics.
- **Frontend** (`frontend/src/components/ConfigModal.svelte`,
  `frontend/src/lib/api.js`, `frontend/src/components/SpikeSwimlane.svelte`):
  - 3-level presets (Chill / Steady / Vigilant) modeled on `FIRE_PRESETS`.
  - Collapsed "Customize settings manually" toggle with full knob panel,
    inline units, inline range hints, and validation wired into `save()`.
  - "Reset to defaults" button that sends 0 for every knob (the backend
    promotes zeros to canonical defaults via `ClampEvtSpike`).
  - `DisabledChannels` / `AddedChannels` textareas with one-channel-per-line.
  - `loadConfig()` normalises nil slice fields so textareas bind cleanly.
  - The detector-status tooltip shows the seven-day warm-up remaining time,
    or says that channel readiness is still pending after day seven.

### NOT shipped (intentionally)

- **No numeric default change in `ClampEvtSpike`.** The selected policy gates
  public status; it does not alter scoring thresholds or detector tuning.
- **No auto-tuning.** Candidates B / C remain operator-selected presets, not
  values derived from observed data.
- **No telemetry export path.** Section 4 is documentation; no collection is
  implemented.

### Remaining owner decision

Whether to opt in to the §3 pilot data collection. It is not required for the
seven-day warm-up policy, but it would inform any future numeric-default
change.
