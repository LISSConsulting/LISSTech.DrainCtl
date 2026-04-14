# Data Model: Installer Dialog Redesign

**Date**: 2026-04-10
**Feature**: [spec.md](spec.md)

## MSI Properties

### New Properties

| Property | Type | Default | Validation | Maps To |
|----------|------|---------|------------|---------|
| POLL_INTERVAL | Integer (string) | "300" | ≥10 (enforced at runtime) | `--poll-interval` in ConfigureNotifyAction |
| SESSION_THRESHOLD | Integer (string) | "80" | 0–100 (enforced at runtime) | `--session-warning-threshold` in ConfigureNotifyAction |

### Existing Properties (moved between dialogs)

| Property | Current Dialog | New Dialog | Change |
|----------|---------------|------------|--------|
| INSTALL_MODE | InstallModeDlg | InstallModeDlg | No change — stays |
| GRACE_PERIOD | InstallModeDlg | MonitoringDlg | Moved |
| ENABLE_PERF | InstallModeDlg | MonitoringDlg | Moved |
| ENABLE_RFX | InstallModeDlg | MonitoringDlg | Moved |
| WEBHOOK_URL | InstallModeDlg | NotificationsDlg | Moved |
| NTFY_URL | InstallModeDlg | NotificationsDlg | Moved |

### Custom Action Character Budgets

| Action | Current Length | After Change | Limit | Headroom |
|--------|--------------|--------------|-------|----------|
| ConfigureAction | 212 chars | 212 chars (unchanged) | 255 | 43 chars |
| ConfigureNotifyAction | 139 chars | 219 chars (+80) | 255 | 36 chars |

## Dialog Flow State Machine

```
                    ┌─────────────┐
                    │  WelcomeDlg │
                    └──────┬──────┘
                           │ Next
                    ┌──────▼──────────────┐
                    │ LicenseAgreementDlg │
                    └──────┬──────────────┘
                           │ Next (accepted)
                    ┌──────▼──────────┐
                    │ InstallModeDlg  │  (mode selector only)
                    └──┬───┬───┬──────┘
          dashboard /  │   │   │  \ standalone
                      ▼   │   ▼
        ┌──────────────┐  │  ┌───────────────────────┐
        │DashboardCfg  │  │  │RegistrationCfg        │
        └───────┬──────┘  │  └───────────┬───────────┘
                │         │              │
                └────┬────┘──────────────┘
                     │ (all modes converge)
              ┌──────▼──────────┐
              │  MonitoringDlg  │  (grace, poll, threshold, perf)
              └──────┬──────────┘
                     │ Next
              ┌──────▼───────────────┐
              │  NotificationsDlg    │  (webhook, ntfy)
              └──────┬───────────────┘
                     │ Next
              ┌──────▼──────────┐
              │ VerifyReadyDlg  │
              └─────────────────┘
```

## Localization Strings

### New Strings

| ID | Value |
|----|-------|
| MonitoringDlg_Title | [ProductName] Setup |
| MonitoringDlgTitle | {\WixUI_Font_Title}Monitoring Settings |
| MonitoringDlgDescription | Configure monitoring intervals and performance collection. |
| MonitoringDlgGraceLabel | Grace period: |
| MonitoringDlgGraceHint | minutes (1–1440) |
| MonitoringDlgPollLabel | Poll interval: |
| MonitoringDlgPollHint | seconds (minimum 10) |
| MonitoringDlgThresholdLabel | Session warning threshold: |
| MonitoringDlgThresholdHint | percent (0 = disabled) |
| MonitoringDlgPerfLabel | Enable performance monitoring |
| MonitoringDlgRfxLabel | Collect RemoteFX metrics |
| NotificationsDlg_Title | [ProductName] Setup |
| NotificationsDlgTitle | {\WixUI_Font_Title}Notification Settings |
| NotificationsDlgDescription | Configure optional notification endpoints. |
| NotificationsDlgWebhookLabel | Webhook URL (optional): |
| NotificationsDlgNtfyLabel | ntfy URL (optional): |

### Updated Strings

| ID | Old Value | New Value |
|----|-----------|-----------|
| InstallModeDlgTitle | {\WixUI_Font_Title}Install Mode and Notifications | {\WixUI_Font_Title}Install Mode |
| InstallModeDlgDescription | Choose how this machine participates in monitoring. | Choose how this machine participates in the monitoring network. |

### Removed Strings

| ID | Reason |
|----|--------|
| InstallModeDlgGraceLabel | Moved to MonitoringDlg |
| InstallModeDlgGraceHint | Moved to MonitoringDlg |
| InstallModeDlgWebhookLabel | Moved to NotificationsDlg |
| InstallModeDlgNtfyLabel | Moved to NotificationsDlg |
| InstallModeDlgPerfLabel | Moved to MonitoringDlg |
| InstallModeDlgRfxLabel | Moved to MonitoringDlg |
