# Research: Installer Dialog Redesign

**Date**: 2026-04-10
**Feature**: [spec.md](spec.md)

## R1: 255-Character Custom Action Limit

**Decision**: Add `--poll-interval` and `--session-warning-threshold` to ConfigureNotifyAction, not ConfigureAction.

**Rationale**: ConfigureAction is already 212 characters. Adding both flags pushes it to 292 characters (exceeds 255-char ICE03 limit). ConfigureNotifyAction is at 139 characters; adding both flags brings it to 219 characters — safely within the limit. The second custom action loads config saved by the first, so the new flags layer on top of the existing configuration correctly.

**Alternatives considered**:
- Add to ConfigureAction: Rejected — 292 chars exceeds limit.
- Create a third custom action (ConfigureMonitorAction): Rejected — unnecessary complexity when ConfigureNotifyAction has 116 chars of headroom.

## R2: Dialog Navigation After Mode-Specific Config

**Decision**: All three modes (standalone, dashboard, registration) converge to MonitoringDlg after their mode-specific step.

**Rationale**: The current flow has standalone skip directly to VerifyReadyDlg, while dashboard/registration go through ModeConfigDlg then to VerifyReadyDlg. The new flow inserts MonitoringDlg and NotificationsDlg between the mode-specific step and VerifyReadyDlg. This requires:
- InstallModeDlg "standalone" Next → MonitoringDlg (was VerifyReadyDlg)
- DashboardConfigDlg Next → MonitoringDlg (was VerifyReadyDlg)
- RegistrationConfigDlg Next → MonitoringDlg (was VerifyReadyDlg)
- MonitoringDlg Next → NotificationsDlg
- NotificationsDlg Next → VerifyReadyDlg
- VerifyReadyDlg Back → NotificationsDlg (all modes, first-time install)

**Alternatives considered**:
- Show Monitoring/Notifications only for certain modes: Rejected — these settings apply to all modes equally.

## R3: Dialog Back Navigation from MonitoringDlg

**Decision**: MonitoringDlg Back navigates to the mode-specific dialog the user came from.

**Rationale**: MonitoringDlg must support three Back targets depending on install mode:
- standalone → InstallModeDlg
- dashboard → DashboardConfigDlg
- registration → RegistrationConfigDlg

This uses the same `Condition` pattern already established in VerifyReadyDlg's current Back button implementation.

**Alternatives considered**:
- Always go back to InstallModeDlg: Rejected — skips mode-specific config, user loses those entries.

## R4: MSI Property Defaults for New Properties

**Decision**: POLL_INTERVAL defaults to 300, SESSION_THRESHOLD defaults to 80.

**Rationale**: These match the defaults in `configure_cmd.go` (`dc.DefaultPollInterval` and `dc.DefaultSessionWarningThreshold`). When the user doesn't change the values, the configure command receives the same values it would use as defaults, ensuring consistent behavior.

**Alternatives considered**:
- Empty defaults (let CLI use its defaults): Rejected — empty string would be passed as `--poll-interval=` which would cause a parse error.

## R5: Controls Removed from InstallModeDlg

**Decision**: Remove grace period, performance monitoring, RemoteFX, webhook URL, and ntfy URL from InstallModeDlg. Keep only the mode selector combo box.

**Rationale**: The dialog currently has 7 interactive controls plus navigation — too crowded. Grace period and perf monitoring move to MonitoringDlg. Webhook and ntfy URLs move to NotificationsDlg. The mode selector remains because it's the foundational choice.

**Alternatives considered**:
- Keep grace period on InstallModeDlg: Rejected — it's a monitoring setting, logically belongs with poll interval and session threshold.

## R6: Localization String Strategy

**Decision**: Reuse existing localization string IDs where the text remains identical. Create new IDs prefixed with `MonitoringDlg` and `NotificationsDlg` for the new dialogs. Remove unused InstallModeDlg string IDs that no longer have corresponding controls.

**Rationale**: The grace period, perf, and RemoteFX strings currently use `InstallModeDlg*` prefixes. Since they're moving to MonitoringDlg, they should get new `MonitoringDlg*` IDs for clarity. The InstallModeDlg title/description strings need updated text (no longer mentions notifications).

**Alternatives considered**:
- Keep old string IDs and just change values: Rejected — string IDs should reflect their dialog for maintainability.
