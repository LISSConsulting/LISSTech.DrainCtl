# Implementation Plan: SSE Dashboard

**Branch**: `005-sse-dashboard` | **Date**: 2026-04-14 | **Spec**: [spec.md](spec.md)

## Summary

Add Server-Sent Events to the dashboard so connected browsers receive server state updates and settings changes in real-time (~2 seconds) instead of waiting for the 30-second poll cycle. Uses an in-process broker with per-subscriber channels. No new dependencies — Go `http.Flusher` + browser `EventSource`.

## Technical Context

**Language/Version**: Go 1.26+ (backend), Svelte 5 / Vite 8 (frontend)
**Primary Dependencies**: Go stdlib (`net/http`, `encoding/json`), browser `EventSource` API
**Storage**: N/A (in-memory broker, no persistence)
**Testing**: `go test` (Go), manual browser testing (frontend)
**Target Platform**: Windows Server (service), modern browsers (dashboard UI)
**Project Type**: Windows service with embedded web dashboard
**Performance Goals**: <2s update latency, 50 concurrent browsers, <1 MB memory per subscriber
**Constraints**: Must not degrade RDSH server performance; existing poll retained as fallback
**Scale/Scope**: 50 registered servers, 50 concurrent browser sessions

## Constitution Check

Constitution is unpopulated (template only). No gates to check.

## Project Structure

### Documentation (this feature)

```text
specs/005-sse-dashboard/
├── spec.md
├── plan.md              # This file
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   └── sse-api.md
└── checklists/
    └── requirements.md
```

### Source Code (files to create or modify)

```text
internal/dashboard/
├── broker.go            # NEW — Broker struct, Subscribe/Unsubscribe/Broadcast
├── server.go            # MODIFY — add SSE handler, wire broker, broadcast in handleReport + handlePutSettings
└── server_test.go       # MODIFY — add broker + SSE tests

frontend/src/
├── App.svelte           # MODIFY — EventSource lifecycle (open/close on auth state)
└── lib/
    └── state.svelte.js  # MODIFY — add SSE event consumer function

internal/dashboard/
└── openapi.yaml         # MODIFY — add /api/v1/events endpoint
docs/
└── guide.html           # MODIFY — document SSE endpoint
```

**Structure Decision**: No new directories. The broker is a single new file in the existing `internal/dashboard/` package. Frontend changes are minimal — EventSource setup in App.svelte, event routing in state.svelte.js.
