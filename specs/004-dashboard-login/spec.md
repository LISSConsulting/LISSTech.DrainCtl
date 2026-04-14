# Feature Specification: Dashboard Login & Logout

**Feature Branch**: `004-dashboard-login`
**Created**: 2026-04-13
**Status**: Draft
**Input**: User description: "Add a login page and a logout button to the dashboard. The login page should be the entry point, and if automatic login fails, the app should show the login page, not built-in basic auth credential prompt."

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Silent Auto-Login on Dashboard Load (Priority: P1)

A user on the same domain opens the dashboard in their browser. The app silently attempts to authenticate using their existing Windows network session (negotiated credentials). On success, the dashboard loads directly — no login form is shown and no credentials are prompted.

**Why this priority**: This is the "happy path" for the target audience (MSPs, RDSH admins on domain-joined machines). It must work invisibly; any friction defeats the purpose.

**Independent Test**: Open the dashboard from a browser on a domain-joined machine with valid credentials. The dashboard loads directly without showing a login form or credential prompt.

**Acceptance Scenarios**:

1. **Given** a user with valid network credentials opens the dashboard URL, **When** the app loads, **Then** automatic authentication succeeds silently and the main dashboard view is shown immediately
2. **Given** a user is already authenticated and reloads the page, **When** the app loads, **Then** the session is recognized and the dashboard displays without re-prompting
3. **Given** automatic login is in progress, **When** the authentication request is pending, **Then** a loading state is shown (not a blank screen)

---

### User Story 2 — Login Form Fallback When Auto-Login Fails (Priority: P1)

A user whose automatic authentication fails (e.g., non-domain browser, expired credentials, unsupported environment) is shown a login page — not a browser-native credential dialog. They can enter their network username (in DOMAIN\username format) and password and submit.

**Why this priority**: Equal priority to P1. The failure path must be handled gracefully. Showing the browser's built-in HTTP authentication dialog is unacceptable — it breaks the visual identity and confuses users.

**Independent Test**: Open the dashboard from a browser where automatic authentication will fail (e.g., private/incognito window or non-domain machine). A styled login page appears instead of a browser prompt. Enter valid credentials and submit; the dashboard loads.

**Acceptance Scenarios**:

1. **Given** automatic login has been attempted and failed, **When** the login page is displayed, **Then** a notification banner explains that automatic sign-in failed and prompts for manual credentials
2. **Given** the login page is displayed, **When** a user enters valid credentials and submits, **Then** authentication succeeds and the dashboard loads
3. **Given** the login page is displayed, **When** a user enters invalid credentials and submits, **Then** an error message is shown on the login page and the form remains visible
4. **Given** the login page is displayed, **When** a user submits empty or incomplete credentials, **Then** inline validation feedback is shown and the form is not submitted
5. **Given** the login page is displayed, **When** the authentication service is unreachable, **Then** a clear error message is shown indicating the failure reason

---

### User Story 3 — Logout from Dashboard (Priority: P2)

An authenticated user can log out of the dashboard using a logout button in the application header. After logout, the login page is shown and the session is invalidated — the user cannot navigate back to protected pages without re-authenticating.

**Why this priority**: Necessary for shared workstations and compliance scenarios (MSP environments often have multiple operators sharing a machine). Lower priority than login because it doesn't block initial access.

**Independent Test**: Log in, click the logout button in the header, confirm the login page appears and the session is gone (pressing back or navigating to the dashboard URL shows the login page, not the dashboard).

**Acceptance Scenarios**:

1. **Given** a user is authenticated and viewing the dashboard, **When** the logout button is clicked, **Then** the session is ended and the login page is displayed
2. **Given** a user has logged out, **When** they navigate back to the dashboard URL, **Then** the login page is shown — the dashboard is not accessible
3. **Given** a user has logged out, **When** the login page is shown, **Then** no notification about automatic sign-in failure is displayed (this is a voluntary logout, not a failure)
4. **Given** a user is authenticated, **When** the logout button is visible in the header, **Then** it is clearly identifiable without cluttering the existing header layout

---

### Edge Cases

- What happens when the automatic login request takes longer than expected (slow network, overloaded server)?
- How does the app behave if the session expires while the dashboard is open (mid-session token expiry)?
- What happens if the user navigates directly to a deep link (e.g., a specific server or tab) while unauthenticated?
- What if the user's credentials are correct but the account has no access rights to the dashboard?
- What if cookies or session storage are cleared while the user is logged in?

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The app MUST attempt automatic authentication using the user's existing network session before showing any login UI
- **FR-002**: The automatic authentication attempt MUST NOT trigger a browser-native credential dialog under any circumstances
- **FR-003**: If automatic authentication succeeds, the dashboard MUST load directly without showing any login form
- **FR-004**: If automatic authentication fails, the app MUST display a styled login page as the entry point
- **FR-005**: The login page MUST display an informational notification indicating that automatic sign-in failed and manual credentials are required
- **FR-006**: The login page MUST include a username field that accepts credentials in DOMAIN\username format, with a visible format hint
- **FR-007**: The login page MUST include a password field
- **FR-008**: The login page MUST include a sign-in submit button
- **FR-009**: On failed login (bad credentials), the login page MUST display an error message without navigating away
- **FR-010**: On successful manual login, the dashboard MUST load and the login page MUST not be shown again within the same session
- **FR-011**: The dashboard header MUST include a logout button when the user is authenticated
- **FR-012**: Clicking the logout button MUST invalidate the current session and return the user to the login page
- **FR-013**: After logout, accessing any protected dashboard route MUST redirect to the login page
- **FR-014**: The login page MUST support both light and dark themes consistent with the existing dashboard theme toggle
- **FR-015**: The login page visual design MUST match the neobrutal style used throughout the application (solid borders, bold shadows, high-contrast)

### Key Entities

- **Session**: Represents an authenticated user's active access token. Has a state (active/expired/invalidated) and an identity (the authenticated user). Created on successful login; destroyed on logout or expiry.
- **Credential**: A username (in domain format) and password pair submitted by the user to authenticate manually.
- **Auto-Login Attempt**: A background authentication request made on app load using the user's existing network identity. Produces either a valid session or a failure signal that triggers the login page.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Users on domain-joined machines with valid credentials reach the dashboard without any manual interaction in 100% of cases
- **SC-002**: Users who must authenticate manually can complete the login flow in under 30 seconds
- **SC-003**: No browser-native credential dialog (HTTP authentication prompt) is ever shown to users — 0 occurrences in any authentication failure scenario
- **SC-004**: After logout, 100% of protected routes redirect to the login page — no unauthorized access is possible via back-navigation or direct URL
- **SC-005**: The login page correctly displays the "automatic sign-in failed" notification whenever it is shown as a result of failed auto-login (not voluntary logout)

## Assumptions

- The dashboard is deployed in environments where Windows-integrated (negotiate/NTLM/Kerberos) authentication is available on the server side
- Non-domain browsers (e.g., personal devices, incognito mode) will always fall through to the manual login form
- Session lifetime follows existing dashboard configuration; no new session expiry settings are introduced in this feature
- The existing dashboard header has sufficient space to accommodate a logout button without a full redesign
- Mobile/responsive layout for the login page is out of scope for v1; desktop browser use is the target
- The login page is only shown for the web dashboard — the CLI and PowerShell module authentication are unaffected
