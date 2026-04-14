# Feature Specification: SSE Dashboard

**Feature Branch**: `005-sse-dashboard`
**Created**: 2026-04-14
**Status**: Draft
**Input**: User description: "Add Server-Sent Events to the dashboard for real-time updates instead of 30-second polling"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Instant Server State Updates (Priority: P1)

An administrator has the DrainCtl dashboard open and a colleague disables drain mode on a server. Within 2 seconds, the administrator sees the server's status change from "Alert" to "Healthy" on their dashboard without refreshing the page or waiting for the next polling cycle.

**Why this priority**: This is the core value proposition. The current 30-second polling delay means state changes are invisible for up to half a minute, which is unacceptable during coordinated maintenance windows or incident response.

**Independent Test**: Can be fully tested by triggering a drain mode change on any registered server and verifying the dashboard updates within 2 seconds. Delivers immediate visibility into farm state changes.

**Acceptance Scenarios**:

1. **Given** a dashboard is open with 15 registered servers, **When** one server reports a drain mode transition, **Then** the dashboard reflects the new status within 2 seconds of the report arriving at the dashboard server.
2. **Given** a dashboard is open, **When** a server's performance metrics cross a warning or critical threshold, **Then** the status pill, gauges, and chart update within 2 seconds.
3. **Given** a dashboard is open, **When** the server receives a report, **Then** no full-page refresh or manual action is required to see the update.

---

### User Story 2 - Resilient Connection with Graceful Fallback (Priority: P2)

An administrator's browser temporarily loses connectivity (network blip, laptop sleep, VPN reconnect). When connectivity resumes, the dashboard automatically reconnects and shows current state without requiring a manual page refresh.

**Why this priority**: Real-time connections are inherently fragile. Without automatic recovery, the feature creates a worse experience than polling — users see stale data and don't know it.

**Independent Test**: Can be tested by disconnecting the network briefly, then reconnecting and verifying the dashboard resumes live updates automatically.

**Acceptance Scenarios**:

1. **Given** the live connection drops, **When** it is re-established, **Then** the dashboard reconnects automatically within 5 seconds and resumes receiving updates.
2. **Given** the live connection is down for an extended period, **When** the periodic fallback poll fires, **Then** the dashboard still shows reasonably current data.
3. **Given** a reconnection occurs, **When** the first update arrives, **Then** the dashboard shows current server states (not stale data from before the disconnect).

---

### User Story 3 - Multi-Browser Support (Priority: P3)

Multiple administrators each have the dashboard open in their own browser. All of them see updates simultaneously when a server reports in. No administrator's connection degrades the experience for others.

**Why this priority**: In an MSP or multi-admin environment, several operators may monitor the same farm. The system must handle concurrent viewers without resource exhaustion.

**Independent Test**: Can be tested by opening 5+ browser tabs and verifying all receive the same update within 2 seconds of each other.

**Acceptance Scenarios**:

1. **Given** 10 administrators have the dashboard open simultaneously, **When** a server reports a state change, **Then** all 10 dashboards update within 2 seconds.
2. **Given** one administrator closes their browser, **When** they do so, **Then** the server releases resources for that connection within 30 seconds.
3. **Given** the maximum number of concurrent viewers is reached, **When** a new viewer connects, **Then** they still receive updates (the system does not refuse connections).

---

### Edge Cases

- What happens when the dashboard server restarts while browsers are connected?
  - Browsers detect the dropped connection and attempt automatic reconnection.
- What happens when 50+ browsers are connected simultaneously?
  - Each connection consumes minimal resources; the system handles it without measurable performance impact on the dashboard server.
- What happens when an agent sends a malformed or oversized report?
  - The update is rejected by the report handler (existing validation); no event is broadcast to connected browsers.
- What happens when a browser connects but the user's session has expired?
  - The connection is rejected with an authentication error; the browser falls back to the login screen.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The dashboard server MUST push state updates to connected browsers when an agent reports via the existing report endpoint.
- **FR-002**: The dashboard server MUST push state updates when the local service check produces a new result.
- **FR-003**: The connection MUST require a valid authenticated session (same session cookie used by the dashboard UI).
- **FR-004**: The browser MUST automatically reconnect after connection loss without user intervention.
- **FR-005**: The existing periodic poll MUST be retained as a fallback to synchronize full state after reconnection gaps.
- **FR-006**: The dashboard server MUST clean up resources for disconnected browsers within 30 seconds.
- **FR-007**: Each connected browser MUST receive the same update simultaneously (broadcast, not point-to-point).
- **FR-008**: The event stream MUST include enough context for the browser to update its local server state (host, status, sessions, performance metrics).
- **FR-009**: The dashboard server MUST broadcast settings changes (thresholds, notification targets, performance config) to all connected browsers when an administrator updates settings via the dashboard UI.

### Key Entities

- **Event**: A broadcast message to all connected browsers. Two types: server state updates (containing host identity, check result, and timestamp) and settings changes (containing the updated configuration).
- **Subscriber**: A connected browser session. Managed by the dashboard server; created on connection, removed on disconnect or session expiry.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Dashboard displays server state changes within 2 seconds of the dashboard server receiving the agent report.
- **SC-002**: After a network interruption of up to 60 seconds, the dashboard reconnects and resumes live updates within 5 seconds of connectivity restoration.
- **SC-003**: 50 concurrent browser sessions receive simultaneous updates with no measurable degradation in update latency.
- **SC-004**: Dashboard server memory usage increases by less than 1 MB per connected browser session.
- **SC-005**: The periodic poll fallback fires at the existing interval and produces a full state sync, ensuring data integrity even if the live connection misses events.

## Clarifications

### Session 2026-04-14

- Q: Should settings changes (thresholds, notification targets, perf config) also be broadcast via the live stream? → A: Yes, stream both server state updates and settings changes so all open browsers reflect config updates immediately.

## Assumptions

- The dashboard already has a working session authentication system that validates browser sessions via cookies.
- The existing 30-second (configurable) poll will be retained as-is; the live connection supplements but does not replace it.
- The dashboard server is already an in-process HTTP server capable of handling long-lived connections.
- Only authenticated dashboard users (session holders) may subscribe to the event stream; machine accounts (agents) do not subscribe.
- The event payload reuses the existing data structures (server view, check result) already served by the REST API.
