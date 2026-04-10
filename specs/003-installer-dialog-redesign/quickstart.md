# Quickstart: Installer Dialog Redesign

**Date**: 2026-04-10
**Feature**: [spec.md](spec.md)

## Implementation Order

### Step 1: Add MSI Properties

**File**: `installer/LISSTech.DrainCtl.wxs`

Add two new `<Property>` elements alongside existing ones:
```xml
<Property Id="POLL_INTERVAL" Value="300" Secure="yes" />
<Property Id="SESSION_THRESHOLD" Value="80" Secure="yes" />
```

Update ConfigureNotifyAction `<SetProperty>` to append:
```
--poll-interval=[POLL_INTERVAL] --session-warning-threshold=[SESSION_THRESHOLD]
```

### Step 2: Create MonitoringDlg.wxs

**File**: `installer/dialogs/MonitoringDlg.wxs` (new)

Dialog layout (Width="370"):
- Banner + header (standard pattern from existing dialogs)
- Grace period: label + edit (GRACE_PERIOD) + hint
- Poll interval: label + edit (POLL_INTERVAL) + hint
- Session threshold: label + edit (SESSION_THRESHOLD) + hint
- Performance monitoring checkbox (ENABLE_PERF)
- RemoteFX sub-checkbox (ENABLE_RFX, indented, conditional on ENABLE_PERF)
- Standard Back/Next/Cancel navigation

### Step 3: Create NotificationsDlg.wxs

**File**: `installer/dialogs/NotificationsDlg.wxs` (new)

Dialog layout (Width="370"):
- Banner + header (standard pattern)
- Webhook URL: label + edit (WEBHOOK_URL)
- ntfy URL: label + edit (NTFY_URL)
- Standard Back/Next/Cancel navigation

### Step 4: Strip InstallModeDlg.wxs

**File**: `installer/dialogs/InstallModeDlg.wxs`

Remove: GraceLabel, GraceEdit, GraceHint, PerfCheck, RfxCheck, WebhookLabel, WebhookEdit, NtfyLabel, NtfyEdit. Keep: banner, header, ModeCombo, navigation. Reduce dialog height (300 → 200 or similar).

### Step 5: Update Dialog Navigation

**File**: `installer/dialogs/DrainCtlUI.wxs`

Rewire all `<Publish>` elements:
- InstallModeDlg "standalone" Next → MonitoringDlg
- DashboardConfigDlg Next → MonitoringDlg
- RegistrationConfigDlg Next → MonitoringDlg
- Add MonitoringDlg Back (conditional on mode) and Next → NotificationsDlg
- Add NotificationsDlg Back → MonitoringDlg and Next → VerifyReadyDlg
- VerifyReadyDlg Back (first-time, all modes) → NotificationsDlg

### Step 6: Update Localization

**File**: `installer/LISSTech.DrainCtl.wxl`

- Add MonitoringDlg* and NotificationsDlg* strings
- Update InstallModeDlgTitle/Description
- Remove unused InstallModeDlg* strings

### Verification

Build the MSI with `just all` and test the dialog flow:
1. Launch installer → verify InstallModeDlg shows only mode selector
2. Select each mode → verify correct mode config dialog → MonitoringDlg → NotificationsDlg → VerifyReadyDlg
3. Verify Back navigation works through entire flow
4. Complete install with defaults → verify config.json has poll_interval=300 and session_warning_threshold=80
5. Complete install with modified values → verify config.json reflects changes
6. Run maintenance/repair → verify old flow unchanged
