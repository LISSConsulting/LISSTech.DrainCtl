# Quickstart: SSE Dashboard

## What This Feature Does

Adds real-time event streaming to the DrainCtl dashboard. When a server reports a state change, all connected browsers see the update within 2 seconds instead of waiting up to 30 seconds for the next poll.

## How It Works

1. Browser opens a persistent connection to `GET /api/v1/events` after login
2. Dashboard server maintains a broker with one channel per connected browser
3. When `handleReport` or `handlePutSettings` completes, the broker broadcasts an event to all subscribers
4. Browser receives the event and updates the server list / config in place
5. Existing 30-second poll continues as a full-sync fallback

## Key Files

| File | Role |
|------|------|
| `internal/dashboard/broker.go` | New — subscriber management, broadcast fan-out |
| `internal/dashboard/server.go` | Modified — SSE handler, broadcast calls in handleReport and handlePutSettings |
| `frontend/src/lib/state.svelte.js` | Modified — SSE consumer, state update from events |
| `frontend/src/App.svelte` | Modified — EventSource lifecycle (open on login, close on logout) |

## Testing

1. Start the dashboard: `drainctl service run` (or `just dev` for Vite + mock)
2. Open the dashboard in two browser windows
3. Trigger a state change (toggle drain mode on a registered server)
4. Both windows should update within 2 seconds
5. Kill the network briefly — on reconnect, the dashboard should resume live updates

## Dependencies

- No new Go dependencies (uses `http.Flusher` from stdlib)
- No new frontend dependencies (`EventSource` is a browser built-in)
