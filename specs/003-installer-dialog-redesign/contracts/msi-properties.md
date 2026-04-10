# MSI Property Contracts

**Date**: 2026-04-10
**Feature**: [spec.md](../spec.md)

## Public MSI Properties

These properties can be set via `msiexec` command line for silent/automated installs:

```
msiexec /i LISSTech.DrainCtl.msi /quiet ^
  INSTALL_MODE=standalone ^
  GRACE_PERIOD=60 ^
  ENABLE_PERF=1 ^
  ENABLE_RFX=1 ^
  POLL_INTERVAL=300 ^
  SESSION_THRESHOLD=80 ^
  WEBHOOK_URL=https://hooks.example.com/drainctl ^
  NTFY_URL=https://ntfy.sh/drainctl-alerts
```

### New Properties (this feature)

| Property | Type | Default | Range | CLI Flag |
|----------|------|---------|-------|----------|
| POLL_INTERVAL | Integer | 300 | ≥10 | `--poll-interval` |
| SESSION_THRESHOLD | Integer | 80 | 0–100 | `--session-warning-threshold` |

### Existing Properties (unchanged behavior)

| Property | Type | Default | CLI Flag |
|----------|------|---------|----------|
| INSTALL_MODE | Enum | standalone | `--mode` |
| GRACE_PERIOD | Integer | 60 | `--grace-period` |
| ENABLE_PERF | Boolean (0/1) | 1 | `--perf-enabled` |
| ENABLE_RFX | Boolean (0/1) | 1 | `--perf-rfx` |
| WEBHOOK_URL | String | (empty) | `--webhook-url` |
| NTFY_URL | String | (empty) | `--ntfy-url` |
| DASHBOARD_PORT | Integer | 49470 | `--dashboard-port` |
| DASHBOARD_GROUP | String | Domain Admins | `--dashboard-group` |
| DASHBOARD_URL | String | (empty) | `--dashboard-url` |
| AUTODISCOVER | Boolean (0/1) | 1 | (not passed to CLI) |

## Custom Action Command Lines

### ConfigureAction (unchanged, 212 chars)

```
"[BinFolder]drainctl.exe" configure --mode=[INSTALL_MODE] --grace-period=[GRACE_PERIOD] --dashboard-port=[DASHBOARD_PORT] --dashboard-group="[DASHBOARD_GROUP]" --perf-enabled=[ENABLE_PERF] --perf-rfx=[ENABLE_RFX]
```

### ConfigureNotifyAction (updated, 219 chars)

```
"[BinFolder]drainctl.exe" configure --mode=[INSTALL_MODE] --dashboard-url=[DASHBOARD_URL] --webhook-url=[WEBHOOK_URL] --ntfy-url=[NTFY_URL] --poll-interval=[POLL_INTERVAL] --session-warning-threshold=[SESSION_THRESHOLD]
```
