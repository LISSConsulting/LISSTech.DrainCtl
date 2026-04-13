# Tasks: Dashboard Login & Logout

**Input**: Design documents from `/specs/004-dashboard-login/`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓, quickstart.md ✓

**Tests**: Not requested — no test tasks included.

**Organization**: Tasks grouped by user story for independent implementation and validation.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no blocking dependency on an incomplete task)
- **[Story]**: Which user story this task belongs to
- Exact file paths included in all descriptions

---

## Phase 1: Setup (File Creation)

**Purpose**: Create the new Go and Svelte files with build tags and package declarations so parallel editing can begin without file-not-found errors.

- [ ] T001 Create `internal/dashboard/session.go` — `//go:build windows` tag, `package dashboard`, empty imports block
- [ ] T002 [P] Create `internal/dashboard/login_handler.go` — `//go:build windows` tag, `package dashboard`, empty imports block
- [ ] T003 [P] Create `frontend/src/lib/auth.svelte.js` — empty Svelte 5 module with exported `authState` placeholder

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Session infrastructure and route restructuring that every user story depends on. No story work can begin until T008 is complete.

**⚠️ CRITICAL**: T004 → T005 → T008 is the blocking chain. T006 and T007 can be done in parallel with T005.

- [ ] T004 Implement `Session` struct, `SessionStore` (Create/Get/Delete, 8 h idle + 24 h absolute expiry, 15-min background reaper) and `requireSession(store *SessionStore) func(http.Handler) http.Handler` middleware in `internal/dashboard/session.go` — see data-model.md for fields and cookie contract
- [ ] T005 [P] Add `sessionStore *SessionStore` field to server struct and initialize it (via `NewSessionStore(ctx)`) in `internal/dashboard/server.go`
- [ ] T006 [P] Scaffold `authState` reactive object, and stub exports `probeNegotiate`, `loginWithCredentials`, `logout` (empty async functions) in `frontend/src/lib/auth.svelte.js`
- [ ] T007 Update `apiFetch` in `frontend/src/lib/api.js` — add 401 intercept that sets `authState.username = null` and `authState.error = 'session_expired'` before re-throwing
- [ ] T008 Modify `internal/dashboard/server.go` — (a) remove `wrapGroup` from `GET /` and SPA static routes so HTML is served unauthenticated; (b) switch `/api/v1/servers`, `/api/v1/servers/{host}` (GET + DELETE), `/api/v1/history/{host}`, `/api/v1/notify-config` (PUT), `/api/v1/notify-test` from `wrapGroup(ctx, h, cfg.Group)` to `requireSession(s.sessionStore)(h)`

**Checkpoint**: Session store compiles; data endpoints protected by session middleware; SPA HTML served without auth; frontend has auth state stub.

---

## Phase 3: User Story 1 — Silent Auto-Login (Priority: P1) 🎯 MVP

**Goal**: On domain machines the dashboard loads directly with no user interaction. On non-domain browsers the app shows a loading indicator then transitions to the login form.

**Independent Test**: Open the dashboard from a domain-joined browser — the dashboard loads without any login form. Confirm a `drainctl_session` cookie is set in DevTools.

- [ ] T009 [P] [US1] Implement `handleNegotiate(store *SessionStore, group string) http.Handler` in `internal/dashboard/login_handler.go` — this handler is called after `NegotiateMiddleware` succeeds; reads `AuthInfo` from context via `GetAuthInfo(r)`, checks group membership (reuse `RequireGroup` logic), calls `store.Create(info)`, sets `Set-Cookie` header per cookie contract in data-model.md, returns `200 {"username":"…"}`
- [ ] T010 [US1] Add `POST /api/v1/auth/negotiate` route in `internal/dashboard/server.go` wrapping `NegotiateMiddleware(ctx, handleNegotiate(s.sessionStore, cfg.Group))` — agent endpoints (`/register`, `/report`, `/notify-config GET`) must remain on their existing SSPI middleware and must not be changed
- [ ] T011 [P] [US1] Implement `probeNegotiate()` in `frontend/src/lib/auth.svelte.js` — sets `authState.loading = true`; fetches `POST /api/v1/auth/negotiate` with `credentials: 'include'`; on 200 sets `authState.username` from response JSON and `authState.loading = false`; on 401/error sets `authState.error = 'auto_login_failed'` and `authState.loading = false`
- [ ] T012 [US1] Update `frontend/src/App.svelte` — on mount call `probeNegotiate()`; gate rendering: show a centred loading indicator while `authState.loading`; show `<Login autoLoginFailed={true} />` when `!authState.username && !authState.loading`; show existing dashboard markup when `authState.username` is set; import from `auth.svelte.js`

**Checkpoint**: Domain-browser opens dashboard → auto-login succeeds → dashboard renders. Non-domain browser → loading → Login component renders.

---

## Phase 4: User Story 2 — Login Form Fallback (Priority: P1)

**Goal**: When auto-login fails a styled login form is shown. Valid credentials load the dashboard; invalid credentials show an inline error without navigating away.

**Independent Test**: Disable auto-login (block the negotiate endpoint in DevTools or simulate a 401) — the login form appears with the "automatic sign-in failed" toast. Enter correct credentials → dashboard loads. Enter wrong credentials → inline error, form stays visible.

- [ ] T013 [P] [US2] Implement `loginLogonUser(username, password string) (*AuthInfo, error)` in `internal/dashboard/login_handler.go` — parses `DOMAIN\user` and `user@domain.com` formats; calls `advapi32.LogonUserW` (LOGON32_LOGON_NETWORK=3, LOGON32_PROVIDER_DEFAULT=0) via `golang.org/x/sys/windows`; on success opens the resulting token, calls `token.GetTokenUser()` and `token.GetTokenGroups()` to build `*AuthInfo`; closes token; returns `nil, err` on failure — password must never be written to any log
- [ ] T014 [US2] Implement `handleLogin(store *SessionStore, group string) http.HandlerFunc` in `internal/dashboard/login_handler.go` — decodes `{"username":"…","password":"…"}` JSON body (max 512 bytes); calls `loginLogonUser`; on `LogonUserW` error returns `401 {"error":"invalid credentials"}`; checks group membership; on group fail returns `403 {"error":"access denied: not a member of <group>"}`; on success calls `store.Create(info)`, sets cookie, returns `200 {"username":"…"}`
- [ ] T015 [US2] Add `POST /api/v1/auth/login` route in `internal/dashboard/server.go` — no auth wrapper; rate limiter already covers it; handler is `handleLogin(s.sessionStore, cfg.Group)`
- [ ] T016 [P] [US2] Implement `loginWithCredentials(username, password)` in `frontend/src/lib/auth.svelte.js` — POSTs `{"username","password"}` JSON to `/api/v1/auth/login`; on 200 sets `authState.username` and clears `authState.error`; on 401/403 sets `authState.error` to the server's `error` string; on 400/500 sets `authState.error = 'service_unavailable'`
- [ ] T017 [US2] Update `frontend/src/components/Login.svelte` — wire `onlogin` prop to call `loginWithCredentials(username, password)` from `auth.svelte.js`; show info toast ("Automatic sign-in failed — please enter your credentials.") on mount when `autoLoginFailed` prop is true; display `authState.error` as an inline error below the submit button when present; ensure username placeholder reads `DOMAIN\username` with format hint; match neobrutal mockup layout (440 px card, 4 px border, 10 px shadow, key icon badge, full-width accent Sign In button)

**Checkpoint**: Login form visible → enter wrong credentials → inline error → enter correct credentials → dashboard loads. Back button from dashboard to login page is not possible (SPA state, not URL navigation).

---

## Phase 5: User Story 3 — Logout (Priority: P2)

**Goal**: Authenticated user can log out via a button in the nav header. After logout the login page is shown and the session is invalidated.

**Independent Test**: Log in via any method → click Sign Out in the nav → login form appears (no auto-login probe, no "automatic sign-in failed" toast) → navigate to `/` in a new tab (same browser session, cookie cleared) → login form shown.

- [ ] T018 [P] [US3] Implement `handleLogout(store *SessionStore) http.HandlerFunc` in `internal/dashboard/login_handler.go` — reads `drainctl_session` cookie; if present and found in store, calls `store.Delete(token)`; always responds with `Set-Cookie: drainctl_session=; Max-Age=0; HttpOnly; SameSite=Strict; Path=/` and `200 {"ok":true}` (idempotent — missing or unknown cookie is not an error)
- [ ] T019 [US3] Add `POST /api/v1/auth/logout` route in `internal/dashboard/server.go` — no auth wrapper; handler is `handleLogout(s.sessionStore)`
- [ ] T020 [P] [US3] Implement `logout()` in `frontend/src/lib/auth.svelte.js` — POSTs to `/api/v1/auth/logout`; on response (any status) sets `authState.username = null`, clears `authState.error`, sets `authState.skipProbe = true` (new flag) so `App.svelte` shows the login form without triggering a new auto-login probe
- [ ] T021 [US3] Add Sign Out button to `frontend/src/components/Nav.svelte` — visible only when `authState.username` is set; calls `logout()` on click; neobrutal style matching existing nav buttons (solid 2 px border, no fill, same height); position after the existing CONFIG button

**Checkpoint**: Authenticated → click Sign Out → login form shown (no auto-login toast) → navigate to any dashboard URL → login form shown again.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Dev mode compatibility, build verification.

- [ ] T022 [P] Add mock auth endpoints to `frontend/dev/mock-api.js` — `POST /api/v1/auth/negotiate` returns `200 {"username":"DEV\\mockuser"}`; `POST /api/v1/auth/login` with non-empty username/password returns `200 {"username": body.username}` else `401 {"error":"invalid credentials"}`; `POST /api/v1/auth/logout` returns `200 {"ok":true}`
- [ ] T023 [P] Review `internal/dashboard/auth_dev.go` — confirm `//go:build windows && devmode` tag doesn't conflict with session.go; `requireSession` in dev mode should be a pass-through no-op (add to `auth_dev.go` if the production implementation's build tag excludes devmode)
- [ ] T024 Run `just lint` and `just all` to confirm zero warnings and successful build across all targets

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 completion — **BLOCKS all user stories**
- **US1 (Phase 3)**: Depends on Phase 2 completion (especially T004, T005, T008)
- **US2 (Phase 4)**: Depends on Phase 3 completion (T012 sets up the conditional rendering that US2's Login.svelte slots into)
- **US3 (Phase 5)**: Depends on Phase 2 completion; can be developed in parallel with US2 if staffed
- **Polish (Phase 6)**: Depends on all user stories being complete

### User Story Dependencies

- **US1 (P1)**: Foundational complete → can start. No dependency on US2 or US3.
- **US2 (P1)**: US1 complete (needs App.svelte auth gate from T012 before Login.svelte is wired in).
- **US3 (P2)**: Foundational complete → can start in parallel with US1/US2 on the backend side (T018, T019); frontend side (T020, T021) can be done after T006 scaffolds auth state.

### Within Each Phase

- Models/store before middleware
- Backend handler before route registration
- Frontend state before component wiring

### Parallel Opportunities

**Phase 2**: T005 ‖ T006 (different files; both depend only on T004)

**Phase 3**: T009 ‖ T011 (Go handler vs Svelte state — different files, no dependency)

**Phase 4**: T013 ‖ T016 (Go credential validator vs Svelte login action — different files)

**Phase 5**: T018 ‖ T020 (Go logout handler vs Svelte logout action — different files)

**Phase 6**: T022 ‖ T023 (mock-api.js vs auth_dev.go — different files)

---

## Parallel Example: Phase 3 (US1)

```
# These two tasks can be executed concurrently:
Task T009: handleNegotiate in internal/dashboard/login_handler.go
Task T011: probeNegotiate() in frontend/src/lib/auth.svelte.js

# Then sequentially:
Task T010: Add route to server.go  (depends on T009)
Task T012: Update App.svelte        (depends on T011)
```

---

## Implementation Strategy

### MVP First (User Story 1 only — Silent Auto-Login)

1. Complete Phase 1: Setup (T001–T003)
2. Complete Phase 2: Foundational (T004–T008) — **CRITICAL**
3. Complete Phase 3: US1 (T009–T012)
4. **STOP and VALIDATE**: Open dashboard from domain browser — loads without form. Open from non-domain browser — loading spinner then Login component renders (form not yet wired, which is fine for MVP validation)
5. Deploy/demo if ready

### Incremental Delivery

1. Setup + Foundational → session infrastructure live
2. US1 complete → domain users have zero-friction access (**MVP**)
3. US2 complete → non-domain users and fallback path covered
4. US3 complete → logout available for shared workstations
5. Polish → dev mode works end-to-end

### Parallel Team Strategy

With two developers after Phase 2 completes:
- **Dev A**: US1 backend (T009, T010) → then US1 frontend (T011, T012)
- **Dev B**: US3 backend (T018, T019) → then US3 frontend (T020, T021)
- US2 follows US1 completion (Dev A continues)

---

## Notes

- `[P]` tasks share no file with each other — safe to run concurrently
- Password must never be logged — enforced at code review
- `requireSession` and `wrapGroup` / `wrapAuth` are now on different endpoint groups — do not conflate them
- Agent endpoints (`/register`, `/report`, `/notify-config GET`) must remain on SSPI middleware throughout
- Session cookie must have `Secure` flag when TLS is active — check `cfg.TLSCert != ""` when building the cookie
- Dev build tag is `devmode` (from `auth_dev.go`: `//go:build windows && devmode`) — session.go `requireSession` production code should use `//go:build windows && !devmode`
- Commit after each checkpoint for clean rollback points
