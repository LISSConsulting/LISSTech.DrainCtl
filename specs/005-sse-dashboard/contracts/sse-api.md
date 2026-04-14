# API Contract: SSE Events Endpoint

## Endpoint

```
GET /api/v1/events
```

**Authentication**: Session cookie (`drainctl_session`) required. Uses `requireSession` middleware.
**Rate limiting**: Standard rate limiter (same as other UI endpoints).
**Content-Type**: `text/event-stream`
**Cache-Control**: `no-cache`
**Connection**: Keep-alive (long-lived)

## SSE Message Format

Each message follows the [SSE specification](https://html.spec.whatwg.org/multipage/server-sent-events.html):

```
event: <event_type>
data: <json_payload>

```

### Event Types

#### `server_update`

Sent when an agent reports or the local service check completes.

```
event: server_update
data: {"host":"RDSH01","server":{...ServerView...},"timestamp":"2026-04-14T18:30:00Z"}

```

The `server` field has the same shape as a single element from `GET /api/v1/servers`.

#### `settings_update`

Sent when an administrator changes dashboard settings.

```
event: settings_update
data: {"settings":{...SettingsResponse...},"timestamp":"2026-04-14T18:30:00Z"}

```

The `settings` field has the same shape as `GET /api/v1/settings`.

## Error Responses

| Status | Condition |
|--------|-----------|
| 401 | Missing or expired session cookie |
| 429 | Rate limit exceeded |

## Connection Lifecycle

1. Browser opens `EventSource('/api/v1/events')` with credentials
2. Server validates session, begins streaming
3. Server pushes events as they occur (no fixed interval)
4. On disconnect: browser auto-reconnects (EventSource default ~3s retry)
5. On session expiry: server closes connection; browser gets error, clears auth
