# Contract: Authentication API Endpoints

**Feature**: 004-dashboard-login
**Date**: 2026-04-13

These are the three new HTTP endpoints added to the dashboard server. All existing endpoints are unchanged except: dashboard-facing data endpoints (`/api/v1/servers*`, `/api/v1/history*`, `/api/v1/notify-config PUT`, `/api/v1/notify-test`) switch from SSPI group middleware to session-cookie middleware. The SPA route (`GET /`) is served without authentication.

---

## POST /api/v1/auth/negotiate

**Purpose**: Attempt silent Windows Negotiate (SSPI) authentication. Called by the Svelte app on load to perform auto-login without user interaction.

**Auth**: SSPI Negotiate middleware (same as existing agent endpoints). This endpoint IS the authentication mechanism — it has no pre-existing session requirement.

**Request**: No body. Browser must be configured with `credentials: 'include'` on the fetch call so it attaches Windows credentials automatically.

**Success Response** (`200 OK`):
```json
{
  "username": "CONTOSO\\jsmith"
}
```
Side effect: `Set-Cookie: drainctl_session=<token>; HttpOnly; SameSite=Strict; Path=/`

**Failure Response** (`401 Unauthorized`):
```
WWW-Authenticate: Negotiate
```
No body. Frontend receives this 401 (no browser dialog for fetch requests) and shows the login form with the "automatic sign-in failed" notification.

**Notes**:
- Multi-leg NTLM is handled transparently by the existing `NegotiateMiddleware`
- If SSPI succeeds but the user is not in the required AD group, returns `403 Forbidden` with a JSON error body
- If a valid session cookie is already present, this endpoint returns `200` immediately without re-running SSPI

---

## POST /api/v1/auth/login

**Purpose**: Validate explicit username/password credentials. Called when the user submits the login form.

**Auth**: None (this is the credential submission endpoint). Rate-limited by the existing per-IP limiter.

**Request**:
```json
{
  "username": "CONTOSO\\jsmith",
  "password": "hunter2"
}
```
Content-Type: `application/json`

**Success Response** (`200 OK`):
```json
{
  "username": "CONTOSO\\jsmith"
}
```
Side effect: `Set-Cookie: drainctl_session=<token>; HttpOnly; SameSite=Strict; Path=/`

**Failure Responses**:

| Status | Condition | Body |
|---|---|---|
| `401 Unauthorized` | Credentials rejected by Windows (`LogonUserW` returned error) | `{"error":"invalid credentials"}` |
| `403 Forbidden` | Credentials valid but user not in required AD group | `{"error":"access denied: not a member of <group>"}` |
| `400 Bad Request` | Malformed JSON or missing required fields | `{"error":"<description>"}` |
| `500 Internal Server Error` | `LogonUserW` system call failure | `{"error":"authentication service unavailable"}` |

**Notes**:
- Accepts `DOMAIN\username` and `user@domain.com` formats; server normalizes before calling `LogonUserW`
- Password is never logged at any level
- Same group check as SSPI path (`RequireGroup`) is applied after `LogonUserW` succeeds
- If a valid session already exists (cookie present), still processes the request and issues a new session (allows credential change without logout)

---

## POST /api/v1/auth/logout

**Purpose**: Invalidate the current session.

**Auth**: Requires a valid `drainctl_session` cookie. If the cookie is absent or the session is not found (already expired/revoked), returns `200` anyway (idempotent logout).

**Request**: No body.

**Response** (`200 OK`):
```json
{
  "ok": true
}
```
Side effect: `Set-Cookie: drainctl_session=; Max-Age=0; HttpOnly; SameSite=Strict; Path=/` (clears the cookie)

**Notes**:
- Idempotent: calling logout when already logged out is not an error
- Session store entry is deleted synchronously; no race condition with session reaper

---

## Session Middleware (existing endpoints)

All dashboard-facing data endpoints now use `requireSession` middleware instead of `wrapGroup`. The behavior is:

1. Read `drainctl_session` cookie from request
2. Look up token in session store
3. If not found or expired → `401 Unauthorized` with `{"error":"session expired"}` body (no `WWW-Authenticate` header — prevents browser dialog)
4. If found → refresh `lastSeenAt`, attach `AuthInfo` to request context via `authInfoKey` (same context key used by SSPI middleware — handlers are unaffected)
5. Continue to handler

The `authInfoKey` context value is populated identically to the SSPI path, so all existing handlers that call `GetAuthInfo(r)` continue to work without modification.

---

## Frontend Integration Contract

The Svelte frontend interacts with these endpoints as follows:

**On app load (auth probe)**:
```
POST /api/v1/auth/negotiate  (credentials: 'include')
→ 200: store username, show dashboard
→ 401: show login form + "automatic sign-in failed" toast
```

**On login form submit**:
```
POST /api/v1/auth/login  (JSON body)
→ 200: store username, show dashboard
→ 401/403: show inline error on form
→ 400/500: show "service unavailable" error
```

**On logout button click**:
```
POST /api/v1/auth/logout
→ 200: clear username from state, show login form (NO auto-login probe)
```

**On any data API call returning 401**:
```
→ Clear auth state, show login form + "session expired" toast
```
