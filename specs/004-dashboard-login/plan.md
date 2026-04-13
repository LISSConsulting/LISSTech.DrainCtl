# Implementation Plan: Dashboard Login & Logout

**Branch**: `004-dashboard-login` | **Date**: 2026-04-13 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/004-dashboard-login/spec.md`

## Summary

Add a login page as the dashboard entry point with silent SSPI auto-login and a manual credential fallback form. When the dashboard loads, the browser silently attempts Windows Negotiate (Kerberos/NTLM) authentication; success creates a server-side session and loads the dashboard directly. If auto-login fails the Svelte app shows a styled login form (matching neobrutal design) instead of the browser's built-in credential dialog. A logout button in the nav header invalidates the session and returns the user to the login page.

## Technical Context

**Language/Version**: Go 1.26 (backend), Svelte 5 + Vite (frontend)
**Primary Dependencies**: `github.com/alexbrainman/sspi` (SSPI Negotiate, already used), `golang.org/x/sys/windows` (LogonUserW, already used), `crypto/rand` (session tokens, stdlib)
**Storage**: In-memory session store (map with mutex); no persistence needed — sessions are intentionally lost on server restart
**Testing**: `go test ./...` (unit), manual browser testing (Playwright for UI)
**Target Platform**: Windows Server (dashboard) + modern desktop browsers (Chrome, Edge, Firefox)
**Project Type**: Web service with SPA frontend
**Performance Goals**: Auto-login should complete in under 2 seconds on a domain machine (single Negotiate round-trip)
**Constraints**: Session tokens must be cryptographically random (32 bytes); HttpOnly + SameSite=Strict cookies; sessions expire after 8 hours idle
**Scale/Scope**: Single-tenant, typically <10 concurrent dashboard users; in-memory store is appropriate

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution file is unfilled (template placeholders only) — no project-specific principles are defined. All gates pass by default. No violations to justify.

## Project Structure

### Documentation (this feature)

```text
specs/004-dashboard-login/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code (repository root)

```text
internal/dashboard/
├── server.go               # MODIFY: route changes (SPA unauthenticated, add auth routes, swap dashboard API to session auth)
├── sspi.go                 # MODIFY: expose NegotiateProbe() for the /auth/negotiate endpoint
├── auth_prod.go            # MODIFY: add requireSession() production implementation
├── auth_dev.go             # MODIFY: add requireSession() dev no-op
├── session.go              # NEW: session store, cookie helpers, session middleware
└── login_handler.go        # NEW: /auth/negotiate, /auth/login, /auth/logout handlers

frontend/src/
├── App.svelte              # MODIFY: integrate auth flow (auto-login probe → show login or dashboard)
├── lib/
│   ├── auth.svelte.js      # NEW: auth state (isAuthenticated, currentUser, login/logout actions)
│   └── api.js              # MODIFY: add login/logout fetch calls, handle 401 globally
└── components/
    ├── Login.svelte         # MODIFY: wire up to auth state, add toast for auto-login failure, match mockup
    └── Nav.svelte           # MODIFY: add logout button
```

**Structure Decision**: Web application (Option 2). Backend is `internal/dashboard/` (existing). Frontend is `frontend/src/` (existing Svelte 5 + Vite). New files follow the existing pattern: one Go file per concern, one Svelte file per component.

## Complexity Tracking

No constitution violations to justify.
