# Quick Start: Dashboard Login & Logout Implementation

**Feature**: 004-dashboard-login
**Date**: 2026-04-13

---

## What's changing (30-second overview)

The dashboard currently relies on the browser to handle Windows Negotiate authentication transparently. This causes a browser credential dialog on non-domain machines. This feature:

1. Moves the UI route (`GET /`) to serve the SPA without auth
2. Adds three new auth endpoints (`/negotiate`, `/login`, `/logout`)
3. Adds a server-side session store + cookie middleware
4. Adds `LogonUserW` support for explicit credential validation
5. Wires up the Svelte app to probe for auto-login and fall back to a login form
6. Adds a logout button to the nav

---

## Backend: New files to create

### `internal/dashboard/session.go`

Core responsibilities:
- `type Session struct` — token, username, groups, createdAt, lastSeenAt
- `type SessionStore struct` — `sync.Map` + background reaper goroutine
- `NewSessionStore(ctx) *SessionStore` — starts reaper
- `(s *SessionStore) Create(info *AuthInfo) (token string, err error)` — generates token, stores session
- `(s *SessionStore) Get(token string) (*Session, bool)` — validates idle/absolute expiry, bumps lastSeen
- `(s *SessionStore) Delete(token string)` — logout
- `requireSession(store *SessionStore) func(http.Handler) http.Handler` — middleware

### `internal/dashboard/login_handler.go`

Core responsibilities:
- `handleNegotiate(store *SessionStore) http.Handler` — wraps a handler that gets called after SSPI succeeds (via `NegotiateMiddleware`); creates session + sets cookie
- `handleLogin(store *SessionStore, group string) http.HandlerFunc` — parses `{username,password}` JSON, calls `LogonUserW`, checks group, creates session
- `handleLogout(store *SessionStore) http.HandlerFunc` — reads cookie, deletes session, clears cookie
- `loginLogonUser(domain, user, password string) (*AuthInfo, error)` — calls `advapi32.LogonUserW` and reads token groups

---

## Backend: Files to modify

### `internal/dashboard/server.go`

Changes:
1. Add `sessionStore *SessionStore` field to server struct (or pass as closure)
2. Change `GET /` and SPA routes: remove `wrapGroup` — serve HTML unauthenticated
3. Add routes (before data routes):
   ```
   POST /api/v1/auth/negotiate  → NegotiateMiddleware wrapping handleNegotiate
   POST /api/v1/auth/login      → handleLogin (no auth wrapper)
   POST /api/v1/auth/logout     → handleLogout (no auth wrapper; session read inside handler)
   ```
4. Change dashboard data routes from `wrapGroup(...)` to `requireSession(store)(h)`:
   - `GET /api/v1/servers`
   - `GET /api/v1/servers/{host}`
   - `DELETE /api/v1/servers/{host}`
   - `GET /api/v1/history/{host}`
   - `PUT /api/v1/notify-config`
   - `POST /api/v1/notify-test`

### `internal/dashboard/auth_prod.go` and `auth_dev.go`

Neither `wrapAuth` nor `wrapGroup` is used for dashboard routes after this change. They remain for agent endpoints. No change needed unless `requireSession` needs a dev-mode no-op (handle in `session.go` with a build tag or by checking a dev flag passed at construction).

---

## Frontend: New file to create

### `frontend/src/lib/auth.svelte.js`

```js
// Svelte 5 reactive state for authentication
export const authState = $state({ username: null, loading: true, error: null });

export async function probeNegotiate() { ... }   // POST /api/v1/auth/negotiate
export async function loginWithCredentials(username, password) { ... }  // POST /api/v1/auth/login
export async function logout() { ... }           // POST /api/v1/auth/logout
```

---

## Frontend: Files to modify

### `frontend/src/App.svelte`

Add auth gate:
```svelte
{#if authState.loading}
  <!-- loading screen -->
{:else if !authState.username}
  <Login autoLoginFailed={authState.error === 'auto_login_failed'} />
{:else}
  <!-- existing dashboard markup -->
{/if}
```

Call `probeNegotiate()` on mount (but NOT after explicit logout — set a `skipProbe` flag).

### `frontend/src/components/Login.svelte`

The component already exists. Wire it up:
- Accept `autoLoginFailed` prop → show toast on mount if true
- Call `loginWithCredentials(username, password)` on submit
- Show inline error from `authState.error` on failure
- Match mockup: neobrutal card, key icon badge, DOMAIN\username hint, full-width Sign In button

### `frontend/src/components/Nav.svelte`

Add logout button after the existing CONFIG button:
```svelte
<button class="nav-btn" onclick={logout}>Sign Out</button>
```
Button style should match existing nav buttons (neobrutal, no fill, solid border).

### `frontend/src/lib/api.js`

Add global 401 handler in `apiFetch`:
```js
if (res.status === 401) {
  authState.username = null;
  authState.error = 'session_expired';
  throw new Error('session_expired');
}
```

---

## Dev mode note

`auth_dev.go` has `wrapAuth` and `wrapGroup` as no-ops. The new auth endpoints (`/auth/negotiate`, `/auth/login`, `/auth/logout`) need to work in dev mode too — they should still function (the mock API server handles no auth). Consider: in dev mode, `POST /api/v1/auth/login` with any credentials returns success with username from the submitted form. This can be gated with `//go:build devmode`.

Alternatively, the dev mock API (`frontend/dev/mock-api.js`) handles the auth endpoints directly without a Go backend — mock `negotiate` always returns success, `login` validates any non-empty creds.
