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
  "last_spike_at": "2026-04-16T14:22:52-05:00"
}
```

**Response 404**: hostname not registered.
**Response 401**: no valid session.

> **Disabled feature**: when evtspike is disabled in configuration, this endpoint returns **200** with `state: "disabled"` in the `DetectorStatus` body — not 503. HTTP 503 would imply "retry later"; a deliberately disabled feature is a permanent configuration state. The `DetectorStatus.State` field is the correct signal for the UI.

**Omit optional fields** when zero-valued:
- `error_reason` omitted unless `state == "error"`.
- `last_spike_at` omitted if no spike has ever been seen.

### `GET /api/evtspike/spikes?host=<hostname>&limit=<1..50>`

Returns the most recent confirmed spikes for one host, newest first, from the in-memory ring buffer.

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

`limit` clamped to `[1, 50]`, default 20.
Empty array if no spikes recorded for this host. Not a 404.

**Response 401**: no session.

## SSE events

Dashboard clients subscribe to the existing `/api/events` SSE stream (per the in-progress SSE feature, `project_sse_dashboard.md`). Two new event types:

### `event: detector_status`

Fired when a server's detector transitions between states (`disabled` → `training` → `healthy`, or → `error`).

```
event: detector_status
data: {"host":"RDSH-04","state":"training","enabled_channels":54,"mature_channels":0}
```

**Not fired** on per-bucket or per-window events. Transitions only.

### `event: recent_spike`

Fired when a new confirmed spike is appended to the server-side ring buffer.

```
event: recent_spike
data: {"id":412,"host":"RDSH-04","channel":"...","window_start":"...","observed":47,...}
```

Payload is identical to a single `RecentSpikeEntry`.

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
- `TestBroker_DetectorStatusEvent_OnTransition`: simulate state change → exactly one `detector_status` event fan-out.
- `TestBroker_RecentSpikeEvent_OnNewSpike`: append spike → exactly one `recent_spike` event.
- `TestBroker_NoStatusEvent_OnNoOp`: no transition → no event emitted (noise control).
