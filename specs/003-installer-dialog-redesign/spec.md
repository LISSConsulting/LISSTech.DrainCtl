# Feature Specification: Installer Dialog Redesign

**Feature Branch**: `003-installer-config`
**Created**: 2026-04-10
**Status**: Draft
**Input**: User description: "Redesign MSI installer dialogs: split InstallModeDlg into dedicated Monitoring and Notifications pages"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Simplified Install Mode Selection (Priority: P1)

An administrator running the MSI installer for the first time sees a clean install mode dialog that presents only the three deployment options: standalone, dashboard, or registration. The dialog is focused and uncluttered, making the most critical first decision — how the service will operate — easy to understand and select.

**Why this priority**: Install mode selection is the foundational choice that drives the rest of the installer flow. A cluttered dialog with unrelated fields creates confusion and increases the chance of misconfiguration.

**Independent Test**: Can be tested by running the installer and verifying that only the mode selector appears on InstallModeDlg — no grace period, no performance checkboxes, no webhook fields.

**Acceptance Scenarios**:

1. **Given** the installer is launched for a first-time install, **When** the user reaches the Install Mode dialog, **Then** only the install mode selector (standalone / dashboard / registration) is displayed with no other settings fields.
2. **Given** the user selects any install mode, **When** they click Next, **Then** the installer navigates to the appropriate mode-specific configuration dialog (DashboardConfigDlg, RegistrationConfigDlg) or directly to the Monitoring dialog for standalone mode.

---

### User Story 2 - Configure Monitoring Settings at Install Time (Priority: P1)

After completing mode-specific configuration, the administrator reaches a dedicated Monitoring dialog where they can configure operational parameters: grace period, poll interval, session warning threshold, and performance monitoring options. These settings are logically grouped and clearly labeled, with sensible defaults pre-filled.

**Why this priority**: Monitoring settings directly affect service behavior and alerting. Exposing poll interval and session warning threshold at install time (previously only available in config.json) eliminates a common post-install configuration step.

**Independent Test**: Can be tested by running the installer through any mode and verifying that the Monitoring dialog appears with all four settings, defaults are pre-populated, and the sub-checkbox for RemoteFX is disabled when performance monitoring is unchecked.

**Acceptance Scenarios**:

1. **Given** the user has completed mode-specific configuration (or selected standalone), **When** they reach the Monitoring dialog, **Then** they see fields for grace period (default 60), poll interval (default 300), session warning threshold (default 80), and performance monitoring checkbox (default checked) with an indented RemoteFX sub-checkbox (default checked).
2. **Given** the performance monitoring checkbox is unchecked, **When** the user looks at the RemoteFX sub-checkbox, **Then** it is visually disabled and cannot be toggled.
3. **Given** the user modifies the poll interval to 30 and the session warning threshold to 90, **When** the install completes, **Then** the resulting config.json reflects `poll_interval: 30` and `session_warning_threshold: 90`.

---

### User Story 3 - Configure Notification Endpoints at Install Time (Priority: P2)

After the Monitoring dialog, the administrator reaches a Notifications dialog with optional fields for webhook URL and ntfy URL. Both fields are clearly labeled as optional.

**Why this priority**: Notifications are important but not required for the service to function. Separating them into their own dialog gives them appropriate visibility without crowding the monitoring settings.

**Independent Test**: Can be tested by running the installer to the Notifications dialog, leaving both fields blank, and verifying the install completes successfully. Alternatively, entering a URL in either field and verifying it appears in the resulting configuration.

**Acceptance Scenarios**:

1. **Given** the user has completed the Monitoring dialog, **When** they reach the Notifications dialog, **Then** they see optional text fields for webhook URL and ntfy URL.
2. **Given** both notification fields are left blank, **When** the user clicks Next, **Then** the installer proceeds to VerifyReadyDlg without error.
3. **Given** the user enters a webhook URL, **When** the install completes, **Then** the notification configuration includes the specified webhook endpoint.

---

### User Story 4 - Consistent Back Navigation (Priority: P2)

The administrator can navigate backward through the entire dialog sequence without losing entered values. The Back button on VerifyReadyDlg always returns to NotificationsDlg regardless of install mode.

**Why this priority**: Predictable navigation builds user confidence and allows reviewing settings before committing to install.

**Independent Test**: Can be tested by advancing through the full dialog flow, then clicking Back repeatedly to verify each dialog is revisited in reverse order with previously entered values preserved.

**Acceptance Scenarios**:

1. **Given** the user is on VerifyReadyDlg during a first-time install (any mode), **When** they click Back, **Then** they return to NotificationsDlg.
2. **Given** the user navigated back to MonitoringDlg and changed the grace period, **When** they click Next through to VerifyReadyDlg, **Then** the updated grace period value is preserved.

---

### Edge Cases

- What happens when the user enters a poll interval below the minimum (less than 10 seconds)? The installer should accept the input but the service clamps it to the minimum at runtime.
- What happens when the user enters a session warning threshold of 0? It disables session utilization alerts, which is valid behavior.
- What happens when the user enters a grace period outside the 1–1440 range? The installer accepts the input; the service applies `ClampRetention()` logic at runtime.
- What happens during a maintenance/repair install? The new MonitoringDlg and NotificationsDlg are not shown — the maintenance flow remains unchanged (MaintenanceWelcomeDlg → MaintenanceTypeDlg → VerifyReadyDlg).

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The Install Mode dialog MUST display only the install mode selector (standalone, dashboard, registration) with no other configuration fields.
- **FR-002**: The installer MUST present a dedicated Monitoring dialog after mode-specific configuration, containing grace period, poll interval, session warning threshold, and performance monitoring controls.
- **FR-003**: The Monitoring dialog MUST pre-populate grace period with 60, poll interval with 300, session warning threshold with 80, performance monitoring as enabled, and RemoteFX collection as enabled.
- **FR-004**: The RemoteFX metrics sub-checkbox MUST be disabled when the performance monitoring checkbox is unchecked.
- **FR-005**: The installer MUST present a dedicated Notifications dialog after the Monitoring dialog, containing optional webhook URL and ntfy URL fields.
- **FR-006**: The installer MUST pass the poll interval value to the configure command via `--poll-interval` flag during installation.
- **FR-007**: The installer MUST pass the session warning threshold value to the configure command via `--session-warning-threshold` flag during installation.
- **FR-008**: The VerifyReadyDlg Back button MUST navigate to NotificationsDlg for all install modes during first-time install.
- **FR-009**: The maintenance/repair dialog flow MUST remain unchanged by this feature.
- **FR-010**: All user-visible text in the new dialogs MUST use localization strings.
- **FR-011**: The dialog navigation flow MUST follow: Welcome → License → InstallModeDlg → [DashboardConfigDlg | RegistrationConfigDlg] → MonitoringDlg → NotificationsDlg → VerifyReady.
- **FR-012**: For standalone mode, the flow MUST skip mode-specific configuration and go directly from InstallModeDlg to MonitoringDlg.

### Key Entities

- **Monitoring Settings**: Grace period (minutes, 1–1440), poll interval (seconds, minimum 10), session warning threshold (percent, 0–100), performance monitoring toggle, RemoteFX metrics toggle.
- **Notification Settings**: Webhook URL (optional endpoint), ntfy URL (optional endpoint).
- **MSI Properties**: POLL_INTERVAL and SESSION_THRESHOLD as new installer properties that map to CLI configure flags.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The Install Mode dialog contains exactly one interactive control (the mode selector) plus standard navigation buttons, down from seven interactive controls in the current design.
- **SC-002**: Administrators can configure poll interval and session warning threshold during installation without editing config.json post-install.
- **SC-003**: All five monitoring settings are visible on a single dialog page without scrolling.
- **SC-004**: The installer completes successfully with default values when the user clicks Next through all dialogs without modifying any field.
- **SC-005**: The configured poll interval and session warning threshold values persist correctly to config.json after installation.

## Assumptions

- The existing CLI flags `--poll-interval` and `--session-warning-threshold` in `configure_cmd.go` are stable and require no changes.
- The `ConfigureAction` custom action's 255-character limit per SetProperty is respected by adding the two new flags; if the limit is exceeded, the flags will be moved to `ConfigureNotifyAction` or a new custom action.
- The maintenance/repair flow does not need to expose the new Monitoring or Notifications dialogs — reconfiguration is done via CLI or config.json.
- MSI property values entered by the user are passed as strings to the CLI configure command; type validation and clamping happen at the service/CLI layer, not in the installer.
