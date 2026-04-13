# Data Model: Dashboard Login & Logout

**Feature**: 004-dashboard-login
**Date**: 2026-04-13

---

## Entity: Session

Represents an authenticated user's active access grant within the dashboard.

**Fields**:

| Field | Type | Description |
|---|---|---|
| `token` | string (64 hex chars) | Primary key. Cryptographically random 32-byte value, hex-encoded. Stored in `drainctl_session` cookie. |
| `username` | string | Authenticated user in `DOMAIN\username` format (from SSPI or LogonUserW). |
| `groups` | []string | AD group memberships at login time. Not re-fetched per-request. |
| `createdAt` | time.Time | When the session was created. Used for absolute max-age enforcement (24h). |
| `lastSeenAt` | time.Time | Updated on each authenticated request. Used for idle timeout (8h). |

**State transitions**:
- `active` → created on successful authenticate (Negotiate probe or manual login)
- `active` → `invalidated` on logout (store entry deleted)
- `active` → `expired` when idle timeout or absolute max-age exceeded (reaped by background goroutine)

**Validation rules**:
- Token must be exactly 64 hex characters; rejected if malformed
- Session is invalid if `now - lastSeenAt > 8h` (idle)
- Session is invalid if `now - createdAt > 24h` (absolute)
- On expiry, cookie is not automatically cleared on the client; the next request receives a 401 and the frontend re-routes to login

**Storage**: In-memory `sync.Map` (token → `*Session`). Background reaper runs every 15 minutes.

---

## Entity: LoginRequest (inbound)

Transient — not persisted. Represents the payload from a manual login form submission.

**Fields**:

| Field | Type | Validation |
|---|---|---|
| `username` | string | Required. Accepts `DOMAIN\user` or `user@domain.com`. Max 256 chars. |
| `password` | string | Required. Min 1 char. Max 256 chars. Never logged. |

---

## Entity: AuthInfo (existing — extended context)

Already defined in `sspi.go`. Used as the identity payload attached to a session after authentication. No changes to the struct.

**Fields**:

| Field | Type | Description |
|---|---|---|
| `Username` | string | `DOMAIN\username` |
| `Groups` | []string | AD groups at time of authentication |

---

## Cookie Contract

| Attribute | Value |
|---|---|
| Name | `drainctl_session` |
| Value | 64-char hex session token |
| Path | `/` |
| HttpOnly | yes |
| SameSite | Strict |
| Secure | yes if TLS enabled, no otherwise |
| Max-Age | not set (session cookie — cleared on browser close; server enforces idle timeout) |
