# Research: SSE Dashboard

## R1: SSE vs WebSocket for Server-Push

**Decision**: Server-Sent Events (SSE) via `text/event-stream`

**Rationale**: SSE is unidirectional (server → client), which matches the use case exactly — the server pushes updates, the browser only listens. SSE has built-in reconnection (`EventSource` automatically retries), works over standard HTTP (no upgrade handshake), and requires no additional dependencies. The dashboard server already uses Go's `http.Flusher` interface (used by the Swagger UI handler), so SSE is a natural fit.

**Alternatives considered**:
- **WebSocket**: Bidirectional, but we don't need client → server messaging. Adds complexity (upgrade handshake, ping/pong keepalive, fragmentation). Go stdlib doesn't include WebSocket; requires `gorilla/websocket` or similar.
- **Long polling**: Simpler but higher latency and more HTTP overhead per update.

## R2: Broker Pattern for Fan-Out

**Decision**: In-process broker with subscriber channels

**Rationale**: Each connected browser gets a buffered Go channel. On broadcast, the broker iterates subscribers and sends to each channel. Slow/dead subscribers are detected by a non-blocking send (channel full or closed) and removed. This is the standard Go pattern for SSE fan-out — no external dependencies, no shared state beyond a mutex-protected subscriber map.

**Alternatives considered**:
- **sync.Cond**: Too low-level, no per-subscriber backpressure.
- **External pub/sub (Redis, NATS)**: Overkill for a single-process dashboard serving <100 concurrent browsers.

## R3: Event Payload Structure

**Decision**: Reuse existing `ServerView` for server state events and the settings response struct for config events. Each SSE message carries a JSON payload with an `event` type discriminator.

**Rationale**: The browser already knows how to render `ServerView` (it's what `/api/v1/servers` returns). Reusing the same shape means the SSE consumer can update `appState.servers` directly with no transformation. Settings events reuse the same shape as `GET /api/v1/settings`.

**Alternatives considered**:
- **Minimal diff payloads**: Lower bandwidth but requires client-side patch logic. Premature optimization — a single ServerView is ~1KB.
- **Full state snapshot on every event**: Simpler but wasteful when 1 of 50 servers changes.

## R4: Authentication for SSE Endpoint

**Decision**: Require session cookie (`drainctl_session`), same as other dashboard UI endpoints. Use the existing `requireSession` middleware.

**Rationale**: SSE connections are long-lived HTTP requests. The session cookie is sent on the initial request and validated once. If the session expires while the connection is open, the server closes the connection; the browser's `EventSource` will attempt to reconnect, hit the session check, fail with 401, and the frontend auth handler will redirect to login.

**Alternatives considered**:
- **Token-based auth via query param**: Exposes token in server logs and browser history. Unnecessary given cookie auth already works.

## R5: Reconnection and Fallback Strategy

**Decision**: Keep the existing periodic poll as a full-sync fallback. `EventSource` handles reconnection automatically (default 3-second retry). On reconnect, the browser receives incremental updates; the next poll cycle ensures full-state consistency.

**Rationale**: SSE connections can drop silently (proxy timeouts, load balancer idle limits). The poll ensures the dashboard is never more than one poll interval behind reality, even if SSE is completely broken. This is defense-in-depth with zero additional complexity.

**Alternatives considered**:
- **Disable polling when SSE is active**: Saves ~1 HTTP request per interval but loses the consistency guarantee. Not worth the risk.
- **Replay missed events on reconnect using `Last-Event-ID`**: Adds server-side event buffering complexity. The poll fallback already solves this.
