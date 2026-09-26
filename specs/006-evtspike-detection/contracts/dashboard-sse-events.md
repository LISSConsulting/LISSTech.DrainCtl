# Contract: Dashboard SSE Events + REST Endpoints for evtspike

**Scope**: Additive to the existing DrainCtl dashboard server (`internal/dashboard/`). Two new SSE event types, two new REST endpoints. No new dashboard pages.

---

## REST endpoints

Both require an authenticated dashboard session (same cookie/SSPI auth as existing endpoints). Both are GET-only.

### `GET /api/evtspike/status?host=<hostname>`

Returns the current detector status for one registered server.

**Response 200** — body matches `DetectorStatus` (see `data-model.md` §6):

```json
{
  "host": "RDSH-04",
  "state": "healthy",
  "enabled_channels": 54,
  "mature_channels": 41,
  "warmup_started_at": "2026-04-09T14:22:52-05:00",
  "last_spike_at": "2026-04-16T14:22:52-05:00"
}
```

**Response 404**: hostname not registered.
**Response 401**: no valid session.

> **Disabled feature**: when evtspike is disabled in configuration, this endpoint returns **200** with `state: "disabled"` in the `DetectorStatus` body — not 503. HTTP 503 would imply "retry later"; a deliberately disabled feature is a permanent configuration state. The `DetectorStatus.State` field is the correct signal for the UI.

**Omit optional fields** when zero-valued:
- `warmup_started_at` omitted when the detector is disabled.
- `error_reason` omitted unless `state == "error"`.
- `last_spike_at` omitted if no spike has ever been seen.

### `GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>`

Without `from` and `to`, returns the most recent confirmed spikes for one host,
newest first. This compatibility mode returns an array of `RecentSpikeEntry`.

**Response 200** — body is an array of `RecentSpikeEntry`:

```json
[
  {
    "id": 412,
    "host": "RDSH-04",
    "channel": "Microsoft-Windows-Winlogon/Operational",
    "window_start": "2026-04-16T14:22:42-05:00",
    "window_end": "2026-04-16T14:22:52-05:00",
    "observed": 47,
    "expected": 3.2,
    "tail_probability": 2.1e-7,
    "confirmation_count": 3,
    "first_seen_at": "2026-04-16T14:22:32-05:00"
  }
]
```

`limit` is clamped to `[1, 50]`, defaulting to 20. An empty array means no
spikes were recorded for this host, not that the host is unknown.

### `GET /api/evtspike/spikes?host=<hostname>&from=<RFC3339>&to=<RFC3339>`

Range mode requires both bounds and returns the Event Spikes swimlane envelope.
The visible window is half-open: a spike matches only when
`window_start ∈ [from, to)`.

**Response 200** — body is:

```json
{
  "spikes": [{ "id": 412, "host": "RDSH-04", "window_start": "2026-04-16T14:22:42-05:00" }],
  "total": 763,
  "truncated": true,
  "as_of_id": 927
}
```

- `total` is the exact number of matching spikes in the visible window.
- `spikes` contains at most 500 matching rows, ordered newest first.
- `truncated` is true when older matching rows were omitted from `spikes`.
- `as_of_id` is the maximum matching row ID included in the same SQLite read
  snapshot, or `0` for an empty window. An in-window live SSE spike increments
  the displayed total only when its ID is greater than this watermark.

The dashboard header displays `total`, not the plotted-row count.

**Response 400**: `from` and `to` are not both supplied, either timestamp is malformed, or `to` is not after `from`.
**Response 401**: no session.
**Response 404**: hostname not registered.

## SSE events

Dashboard clients subscribe to the existing `/api/v1/events` SSE stream. Two new event types:

### `event: detector_status`

Fired when a server's detector transitions between states (`disabled` → `training` → `healthy`, or → `error`). `training` holds until **both** the durable seven-day warm-up and the channel-readiness gate complete. The detector continues scoring and emits confirmed `recent_spike` events throughout warm-up.

```
event: detector_status
data: {"host":"RDSH-04","state":"training","enabled_channels":54,"mature_channels":54,"warmup_started_at":"2026-04-16T14:22:52-05:00"}
```

**Not fired** for identical status snapshots. A changed subscription count,
mature-channel count, warm-up start, error detail, or state emits so connected
clients see training progress without receiving the scorer's unchanged
ten-second snapshots.

### `event: recent_spike`

Fired when a new confirmed spike is stored.

```
event: recent_spike
data: {"id":412,"host":"RDSH-04","channel":"...","window_start":"...","observed":47,...}
```

Payload is identical to a single `RecentSpikeEntry`. The client deduplicates
entries by `id`, because a live event can also appear in the current range
response after a refresh.

## SSE fallback

When SSE is unavailable (older dashboard without SSE support, or connection lost), the existing 30s poll fetches `GET /api/evtspike/status` for each card and the dashboard reconciles. Acceptable degradation — the status pill updates once per minute instead of live, and recent-spikes updates on detail-page open.

## Interaction with broker

`internal/dashboard/broker.go` gains two additional event types alongside its existing ones. The broker already handles fan-out to multiple subscribers and backpressure; no new transport logic.

Both event types are broadcast globally, same as existing events. The client filters by `host` in the payload (the existing pattern).

---

## Contract tests

- `TestAPI_EvtSpikeStatus_Healthy`: mock registry with one healthy host; assert response shape and `state: "healthy"`.
- `TestAPI_EvtSpikeStatus_Training`: mock with zero mature channels; assert `state: "training"`.
- `TestAPI_EvtSpikeStatus_Disabled`: mock with `cfg.EvtSpike.Enabled == false`; assert `state: "disabled"`.
- `TestAPI_EvtSpikeStatus_UnknownHost_404`: unregistered hostname → 404.
- `TestAPI_EvtSpikeStatus_NoSession_401`: unauthenticated → 401.
- `TestAPI_EvtSpikeSpikes_RingBufferOrdering`: insert N > 50, assert only most recent 50 returned, newest first.
- `TestAPI_EvtSpikeSpikes_LimitClamp`: `limit=500` → clamped to 50 (200 OK, 50 entries).
- `TestAPI_EvtSpikeSpikes_RangeEnvelope`: a `[from, to)` query returns an exact `total`, at most 500 newest-first `spikes`, and `truncated` when older matching rows were omitted.
- `TestBroker_DetectorStatusEvent_OnTransition`: simulate state change → exactly one `detector_status` event fan-out.
- `TestBroker_RecentSpikeEvent_OnNewSpike`: append spike → exactly one `recent_spike` event.
- `TestBroker_NoStatusEvent_OnNoOp`: no transition → no event emitted (noise control).
