# Contract: Named-Pipe `spike` Command

**Scope**: The standalone `evtspike` CLI forwards confirmed spikes to a DrainCtl service on the same host via the existing `\\.\pipe\drainctl` named pipe (FR-015).

**Transport**: Existing pipe framing from `internal/pipe/pipe.go`. One request / one response per connection. JSON body.

---

## Request

The existing `PipeRequest` struct gains a new `Cmd` value and an optional `Spike` field:

```go
type PipeRequest struct {
    Cmd         string        `json:"cmd"`                 // NEW value: "spike"
    Limit       int           `json:"limit,omitempty"`
    ChangesOnly bool          `json:"changes_only,omitempty"`
    Hostname    string        `json:"hostname,omitempty"`
    Spike       *SpikePayload `json:"spike,omitempty"`    // NEW: populated when Cmd == "spike"
}
```

For `Cmd == "spike"`, `Spike` MUST be non-nil; all other fields MUST be zero-valued.

### Request example

```json
{
  "cmd": "spike",
  "spike": {
    "host": "RDSH-04",
    "channel": "Microsoft-Windows-Winlogon/Operational",
    "window_start": "2026-04-16T14:22:42-05:00",
    "window_end": "2026-04-16T14:23:42-05:00",
    "observed": 47,
    "expected": 3.2,
    "tail_probability": 2.1e-7,
    "confirmation_count": 3,
    "first_seen_at": "2026-04-16T14:20:42-05:00"
  }
}
```

## Response

Standard `PipeResponse` envelope (unchanged):

```go
type PipeResponse struct {
    OK    bool            `json:"ok"`
    Error string          `json:"error,omitempty"`
    Data  json.RawMessage `json:"data,omitempty"`
}
```

### Success

```json
{ "ok": true }
```

`Data` is omitted — there is nothing to return.

### Failure modes

| Cause | `ok` | `error` | Standalone response |
|-------|------|---------|---------------------|
| Service does not recognize `"spike"` cmd (older service) | `false` | `"unknown command: spike"` | Fall back to local log (FR-016). Do not retry. |
| Service received spike but `SendNotification` failed (e.g., all targets unreachable) | `true` | — | Service logs; standalone is unaware. Service side handles retry/cooldown. |
| `Spike` field missing or malformed | `false` | `"spike: missing payload"` / `"spike: invalid payload: <detail>"` | Log + drop. This is a bug, not a transient condition. |
| Pipe connection refused (service not running) | n/a | n/a | Fall back to local log (FR-016). |
| Pipe write timeout (service hung) | n/a | n/a | Drop this spike; continue detection. Next spike will attempt reconnect (FR-017). |

## Service-side handling

On receipt of `Cmd == "spike"`:
1. Validate `Spike != nil`, invariants from `data-model.md` §5 satisfied.
2. Call the same `drainctl.SendNotification(targets, state, result, TriggerEventSpike, "")` pipeline that the in-service subsystem calls.
3. Publish to the dashboard broker as a `recent_spike` SSE event and update the ring buffer.
4. Return `{"ok": true}`.

The service's handler runs `SendNotification` on the caller goroutine (same as other pipe commands) — no background queue. If notification fan-out is slow, the standalone CLI's pipe write will block briefly, which is acceptable (an extra 100ms under normal conditions; no retry storm).

## Authentication / authorization

Uses the existing pipe ACL. The `\\.\pipe\drainctl` pipe is created with a descriptor that permits `LocalSystem` and `Administrators` only (matches existing `internal/pipe` behavior). No additional auth for the `spike` command.

## Idempotence

`spike` is **not** idempotent — the service will fan out a notification for every received payload. The standalone CLI is responsible for its own de-duplication (cooldown already lives in the detector, not the pipe client).

---

## Contract tests

- `TestPipeSpike_Round_Trip`: client sends a valid `spike` payload; server decodes identical struct.
- `TestPipeSpike_TriggersNotification`: fake `SendNotification` is called exactly once with `TriggerEventSpike`.
- `TestPipeSpike_UnknownCmd_OldService`: server unaware of `"spike"` returns `{"ok": false, "error": "unknown command: spike"}`.
- `TestPipeSpike_MalformedPayload_Rejected`: omit `Spike` field or set `TailProbability = 1.5` → server returns error.
- `TestPipeSpike_ServiceDown_ClientFallsBack`: dial against closed pipe; client logs locally and returns.
