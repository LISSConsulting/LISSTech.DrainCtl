# Research: Dashboard Login & Logout

**Feature**: 004-dashboard-login
**Date**: 2026-04-13

---

## Decision 1: How to prevent the browser's native credential dialog

**Decision**: Serve the SPA HTML without authentication. The Svelte app performs a fetch-based Negotiate probe (`POST /api/v1/auth/negotiate`) instead of relying on browser-level auth on the document request.

**Rationale**: Browser-native credential dialogs (HTTP Basic/Negotiate) only appear during document-level navigation. `fetch()` requests that receive a 401 do not trigger a dialog — the response is simply returned to JavaScript. By moving authentication to a fetch call after the SPA is loaded, we guarantee the browser dialog never appears regardless of auth outcome.

**Alternatives considered**:
- Keep `WWW-Authenticate: Negotiate` on `GET /` → rejected because this triggers the browser dialog on document load for non-domain users before JS can intercept it
- Use `WWW-Authenticate: X-DrainCtl` (custom scheme) → technically works but non-standard; adds complexity with no benefit since the fetch approach is cleaner
- Serve SPA on a different port from the API → rejected as unnecessary complexity

---

## Decision 2: Session mechanism (server-side cookie vs JWT)

**Decision**: Server-side in-memory session store with random 32-byte token in an `HttpOnly` + `SameSite=Strict` cookie.

**Rationale**: The dashboard is single-tenant and has few concurrent users. Server-side sessions are simpler to implement correctly (no signing keys to manage, no token refresh logic, revocation is trivial), and logout works instantly by deleting the store entry. An in-memory store is acceptable because sessions should not survive server restarts — users simply re-authenticate.

**Alternatives considered**:
- JWT with HS256 → rejected because it requires a persistent signing key, token revocation is complex, and there's no benefit for a single-tenant desktop tool
- Database-backed sessions → rejected; overkill for this scale; introduces a dependency
- Browser sessionStorage token (no server store) → rejected; not revocable on logout

**Session properties**:
- Token: `crypto/rand.Read` → 32 bytes → hex-encoded (64 chars) — avoids base64 padding edge cases
- Cookie name: `drainctl_session`
- Cookie flags: `HttpOnly`, `SameSite=Strict`, `Secure` (when TLS is enabled), `Path=/`
- Expiry: 8 hours idle timeout (bumped on each authenticated request); absolute max 24 hours

---

## Decision 3: Validating explicit username/password credentials on Windows

**Decision**: Use `LogonUserW` from `advapi32.dll` with logon type `LOGON32_LOGON_NETWORK` (3) and provider `LOGON32_PROVIDER_DEFAULT` (0).

**Rationale**: `LogonUserW` is the standard Windows API for validating a username/password pair against Active Directory (or local accounts). `LOGON32_LOGON_NETWORK` creates a network logon token — it validates the credentials against the domain controller without creating a full interactive session or loading a user profile. This is the correct type for web authentication scenarios. The resulting token is used only to read group memberships (via `GetTokenGroups`), then closed immediately.

**Alternatives considered**:
- SSPI `AcquireCredentialsHandle` with `SEC_WINNT_AUTH_IDENTITY` → more complex multi-step SSPI negotiation for explicit creds; `LogonUserW` is simpler and better documented for this purpose
- LDAP bind against AD → requires network access to LDAP, additional dependency, and LDAP credentials; `LogonUserW` delegates to Windows which already has a DC connection
- Accept credentials but don't validate → rejected; unacceptable security posture

**Username format**: Accept both `DOMAIN\username` and `username@domain.com` (UPN). Split on `\` or `@` to separate domain/username for the `LogonUserW` call. If no domain prefix is provided, pass an empty string for the domain parameter (Windows uses the current machine's domain).

---

## Decision 4: Which endpoints keep SSPI vs switch to session auth

**Decision**: Agent endpoints keep SSPI; dashboard-facing endpoints switch to session-cookie middleware.

**Rationale**: Agents (the `drainctl agent` process) authenticate using machine credentials via SSPI in their HTTP client. They never use cookies. Dashboard users authenticate once via the login flow and use the session cookie for subsequent calls. These are separate authentication planes and should remain independent.

**Endpoint mapping**:

| Endpoint | Auth method | Reason |
|---|---|---|
| `GET /api/v1/health` | None | Already public |
| `POST /api/v1/register` | SSPI (Negotiate) | Agent endpoint |
| `POST /api/v1/report` | SSPI (Negotiate) | Agent endpoint |
| `GET /api/v1/notify-config` | SSPI (Negotiate) | Agents fetch this |
| `GET /api/v1/servers` | Session cookie | Dashboard only |
| `GET /api/v1/servers/{host}` | Session cookie | Dashboard only |
| `DELETE /api/v1/servers/{host}` | Session cookie | Dashboard only |
| `GET /api/v1/history/{host}` | Session cookie | Dashboard only |
| `PUT /api/v1/notify-config` | Session cookie | Dashboard only |
| `POST /api/v1/notify-test` | Session cookie | Dashboard only |
| `POST /api/v1/auth/negotiate` | SSPI → creates session | New auth endpoint |
| `POST /api/v1/auth/login` | Username+password → creates session | New auth endpoint |
| `POST /api/v1/auth/logout` | Session cookie (to invalidate) | New auth endpoint |
| `GET /` (SPA HTML) | None | Served unauthenticated; SPA handles auth |
| `GET /assets/*` | None (immutable, already open) | No change |

---

## Decision 5: Auto-login Negotiate probe mechanism

**Decision**: `POST /api/v1/auth/negotiate` acts as the probe. The SPA calls this endpoint on load with `credentials: 'include'`. The server runs `NegotiateMiddleware` on this endpoint. On success it creates a session and returns `200 {"username":"DOMAIN\\user"}`. On failure (no token / SSPI fails) the middleware returns `401 WWW-Authenticate: Negotiate`. Since this is a fetch call (not a document navigation), the browser does not show a dialog; the 401 is returned to JavaScript, which shows the login form.

**Key nuance**: The Negotiate handshake is normally two legs (browser receives 401 → sends token). The `fetch` API handles this transparently via browser credential re-sends for trusted intranet/domain hosts. For non-domain machines or browsers where silent Negotiate is not supported, the 401 propagates to JS normally.

**Frontend loading states**:
1. App loads → show neutral loading screen (no login form yet)
2. Probe in flight → loading indicator
3. Probe success → transition to dashboard
4. Probe 401 → show login form + "automatic sign-in failed" toast (info style)
5. Manual form submit → show loading state on button
6. Form submit success → transition to dashboard
7. Form submit failure → inline error on form (do not navigate away)

---

## Decision 6: Logout behavior

**Decision**: `POST /api/v1/auth/logout` deletes the session entry from the store and sets `Set-Cookie: drainctl_session=; Max-Age=0` to clear the client-side cookie. The frontend navigates to the login page without the "automatic sign-in failed" toast (this is a voluntary logout).

**Post-logout navigation**: After logout, the login page is shown but the app does NOT immediately re-probe Negotiate — if the user clicked logout intentionally on a domain machine, re-probing would just auto-login them again. Instead, the login page is shown with no probe. The user can reload the page if they want auto-login to re-run.

**Alternatives considered**:
- Re-probe on logout → rejected; defeats the purpose of logging out on domain machines
- Redirect to `/` with a flag → handled via Svelte state, no server redirect needed
