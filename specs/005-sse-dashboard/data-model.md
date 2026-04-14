# Data Model: SSE Dashboard

## Entities

### SSEEvent

A single message pushed from the dashboard server to all connected browsers.

| Field | Type | Description |
|-------|------|-------------|
| type | string | Event discriminator: `"server_update"` or `"settings_update"` |
| host | string | Hostname of the affected server (server_update only) |
| data | object | Payload — either a ServerView (server_update) or SettingsResponse (settings_update) |
| timestamp | ISO 8601 | When the event was generated |

### Subscriber

A connected browser session consuming the event stream.

| Field | Type | Description |
|-------|------|-------------|
| id | string | Unique subscriber identifier (generated on connect) |
| channel | buffered channel | Outbound event queue for this subscriber |
| session | string | Session token from the `drainctl_session` cookie |
| connected_at | timestamp | When the subscriber connected |

## State Transitions

### Subscriber Lifecycle

```
Connect → Authenticated → Streaming → Disconnected
                                    ↗ (browser close, network drop)
                          Streaming → Evicted (channel full, session expired)
```

- **Connect**: Browser opens EventSource to `/api/v1/events`
- **Authenticated**: Session cookie validated by `requireSession` middleware
- **Streaming**: Subscriber receives events via their channel
- **Disconnected**: Browser closes connection; server detects via `r.Context().Done()`
- **Evicted**: Non-blocking send fails (channel buffer full) → subscriber removed

### Event Flow

```
Agent Report → handleReport → state.Update() → broker.Broadcast(server_update)
Local Check  → svcRunCheck  → dashState.ReportLocal() → broker.Broadcast(server_update)
Settings PUT → handlePutSettings → SaveConfig() → broker.Broadcast(settings_update)
```

## Relationships

- One Broker has many Subscribers (1:N)
- One Event is broadcast to all Subscribers (1:N fan-out)
- One Subscriber belongs to one Session (1:1)
- One ServerView maps to one SSEEvent of type `server_update` (1:1)
