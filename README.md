# 🚰 LISSTech DrainCtl

**Real-time Remote Desktop Session Host drain mode monitoring for Windows Server.**

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/Platform-Windows_Server_2016+-0078D4?logo=windows&logoColor=white)
![License](https://img.shields.io/badge/License-Apache_2.0-blue)
![Version](https://img.shields.io/badge/Version-27.93.0-green)
[![PSGallery](https://img.shields.io/powershellgallery/v/LISSTech.DrainCtl?label=PSGallery&color=blue)](https://www.powershellgallery.com/packages/LISSTech.DrainCtl)

Know the instant someone blocks new connections on your RDSH servers. DrainCtl runs as a Windows Service that detects drain mode changes in real time, maintains a 90-day audit trail, and tells you exactly who made the change. Query it from the CLI, PowerShell, or your RMM &mdash; the answer is always instant.

---

## 📑 Table of Contents

- [🔭 Overview](#-overview)
- [🏗️ Architecture](#️-architecture)
- [📦 Installation](#-installation)
- [🚀 Quick Start](#-quick-start)
- [⌨️ CLI Reference](#️-cli-reference)
- [🐚 PowerShell Module](#-powershell-module)
- [⚙️ Service](#️-service)
- [🔔 Notifications](#-notifications)
- [🔧 Configuration](#-configuration)
- [🔒 Audit Setup](#-audit-setup)
- [🛠️ Building from Source](#️-building-from-source)
- [📁 Project Structure](#-project-structure)
- [📄 License](#-license)

---

## 🔭 Overview

DrainCtl monitors the `TSServerDrainMode` registry value on RDSH servers and answers one question: **are new connections allowed?**

### Key Features

| Feature | Description |
|---------|-------------|
| 🔔 **Real-time detection** | `RegNotifyChangeKeyValue` — instant notification when drain mode changes |
| 📋 **Audit trail** | 90-day JSONL history of every state observation and transition |
| 👤 **Change attribution** | `EvtSubscribe` on Event ID 4657 — knows *who* changed drain mode |
| 🖥️ **Windows Service** | Runs as `DrainCtl`, auto-start, polls as safety net |
| ⚡ **Named pipe IPC** | CLI and PowerShell query the service instantly via `\\.\pipe\drainctl` |
| 📊 **N-central ready** | Exit codes + structured stdout for AMP threshold monitoring |
| 🐚 **PowerShell native** | `Get-RDSHDrainMode`, `Test-RDSHDrainMode`, `Get-RDSHDrainHistory` |
| 🔔 **Multi-target notifications** | N webhook + M ntfy.sh targets, each with individual triggers and repeat intervals |
| 🎯 **Granular triggers** | Per-event notification control: `drain_on`, `drain_off`, `alert`, `healthy`, `session_warning`, and more |
| 📈 **Session tracking** | WTS session enumeration — active, disconnected, and total counts with utilization percentage |
| ⚠️ **Session utilization alerts** | Configurable threshold fires `session_warning` when utilization is too high |

---

## 🏗️ Architecture

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontFamily': 'monospace', 'fontSize': '13px', 'primaryBorderColor': '#2d1a1a', 'lineColor': '#7a5a5a', 'primaryColor': '#f5e0e4', 'primaryTextColor': '#2d1a1a', 'secondaryColor': '#e8f0eb', 'tertiaryColor': '#fce8df'}}}%%
graph TB
    subgraph SVC["DrainCtl Windows Service"]
        RNK["RegNotifyChangeKeyValue"] -->|"registry changed"| CHECK["runCheck()"]
        EVT["EvtSubscribe 4657"] -->|"who changed it"| CHECK
        POLL["Poll Ticker 5 min"] -->|"safety net"| CHECK
        CFG["config.json Watcher (RDCW+poll)"] -->|"config changed"| RELOAD["ReloadConfig()"]
        CHECK --> SESS["WTS Session Enum"]
        SESS --> STORE["MemAuditStore"]
        CHECK --> STORE
        CHECK --> ELOG["Event Log"]
        CHECK --> NOTIFY["Multi-Target Dispatch"]
        NOTIFY --> WH["Webhook 1..N"]
        NOTIFY --> NTFY["ntfy 1..M"]
        STORE --> PIPE["Named Pipe"]
    end

    CLI["drainctl.exe"] -->|"pipe"| PIPE
    PS["PowerShell Module"] -->|"pipe"| PIPE
    NC["N-central AMP"] --> CLI

    CLI -.->|"fallback"| REG["Registry"]
    PS -.->|"fallback"| REG
```

### How Detection Works

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'fontFamily': 'monospace', 'fontSize': '13px', 'primaryBorderColor': '#2d1a1a', 'lineColor': '#7a5a5a'}}}%%
sequenceDiagram
    participant Admin
    participant Registry
    participant RegNotify
    participant EventLog
    participant EvtSubscribe
    participant Service

    Admin->>Registry: chglogon /drain
    Registry-->>RegNotify: value changed (~0ms)
    Registry-->>EventLog: Event 4657 (~200ms)
    RegNotify->>Service: trigger check
    EventLog-->>EvtSubscribe: push attribution
    Service->>Service: ReadDrainMode()
    Service->>EvtSubscribe: WaitAttribution(3s)
    EvtSubscribe-->>Service: DOMAIN\admin
    Service->>Service: Record audit + Event Log
```

---

## 📦 Installation

### MSI Installer (recommended)

```powershell
msiexec /i LISSTech.DrainCtl.msi /qn
```

The MSI installs:

| Component | Location |
|-----------|----------|
| CLI + DLL | `C:\Program Files\LISS Technologies\LISSTech DrainCtl\bin\` |
| PowerShell module | `C:\Program Files\WindowsPowerShell\Modules\LISSTech.DrainCtl\` |
| Data directory | `C:\ProgramData\LISS Technologies\LISSTech DrainCtl\` |
| Windows Service | `DrainCtl` — auto-start as LocalSystem |
| System PATH | `bin\` folder added |
| Event Log source | `DrainCtl` with custom message file |
| JSON config | `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` |

> **Upgrading from v26?** The first run auto-migrates your registry configuration to `config.json`. Existing installs upgrade seamlessly — no manual steps required.

The bundle installer (`LISSTech.DrainCtl.exe`) provides a branded setup wizard with license acceptance and configuration options. For unattended deployment, MSI properties can be passed directly:

```powershell
msiexec /i LISSTech.DrainCtl.msi /qn INSTALL_MODE=registration DASHBOARD_URL=https://dash.example.com:49470 WEBHOOK_URL=https://hooks.example.com/drain
```

### PowerShell Gallery (module only)

```powershell
Install-Module -Name LISSTech.DrainCtl -Scope AllUsers
```

Just the cmdlets — no service, no CLI. Queries go directly to the registry. See [PSGallery](https://www.powershellgallery.com/packages/LISSTech.DrainCtl).

### Manual (CLI only, no service)

Copy `drainctl.exe` anywhere and run it. Works standalone — the service is optional.

---

## 🚀 Quick Start

```powershell
# Check current state (instant if service is running)
PS> drainctl check
2026-04-02T20:08:56-04:00 [INF] source=service
2026-04-02T20:08:56-04:00 [INF] host=RDSH01
2026-04-02T20:08:56-04:00 [INF] drain_mode=ALLOW_ALL_CONNECTIONS value=0
2026-04-02T20:08:56-04:00 [INF] state_since=2026-04-02T18:30:00-04:00 state_duration=1h38m56s
2026-04-02T20:08:56-04:00 [INF] grace_period=1h0m0s
2026-04-02T20:08:56-04:00 [INF] sessions=12 active / 3 disconnected / 15 total (60% of 25)
2026-04-02T20:08:56-04:00 [OK ] status=Healthy connections_allowed=true exit=0

# JSON output
PS> drainctl check --format json

# View audit trail
PS> drainctl history --limit 5

# PowerShell
PS> Get-RDSHDrainMode | Format-List
PS> if (Test-RDSHDrainMode) { "OK" } else { "ALERT" }
PS> Get-RDSHDrainHistory -ChangesOnly -Limit 10
```

---

## ⌨️ CLI Reference

```
drainctl check              Check drain mode state
drainctl history            View audit trail
drainctl audit-setup        Configure registry auditing (one-time, admin)
drainctl service install    Install Windows service
drainctl service uninstall  Remove Windows service
drainctl service start      Start the service
drainctl service stop       Stop the service
drainctl notify add         Add a notification target
drainctl notify remove      Remove a notification target
drainctl notify list        List configured notification targets
drainctl notify test        Send a test notification to all targets
```

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `%ProgramData%\...\audit.jsonl` | Audit trail path |
| `--format` | `plain` (check) / `table` (history) | Output: `plain`, `table`, `csv`, `json` |
| `--quiet` | `false` | Suppress intermediate log lines |

### Check Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--grace` | `60` | Minutes drain mode must persist before alerting |
| `--retention` | `90` | Days to retain audit records (max: 365) |

### History Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--limit` | `50` | Maximum records to display |
| `--changes-only` | `false` | Show only state transitions |

### Notify Subcommands

| Command | Description |
|---------|-------------|
| `drainctl notify add <type> <url> [--triggers ...] [--repeat N]` | Add a webhook or ntfy target with optional trigger filter and repeat interval |
| `drainctl notify remove <url>` | Remove a notification target by URL |
| `drainctl notify list` | List all configured notification targets and their settings |
| `drainctl notify test` | Send a test notification to all configured targets |
| `drainctl notify set-webhook <url>` | Convenience: add/update a single webhook target |
| `drainctl notify set-ntfy <url>` | Convenience: add/update a single ntfy target |

### Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Healthy or within grace period |
| `1` | Alert — drain mode active beyond grace period |
| `2` | Error — registry unreadable |

---

## 🐚 PowerShell Module

```powershell
Import-Module LISSTech.DrainCtl
```

### Cmdlets

| Cmdlet | Returns | Description |
|--------|---------|-------------|
| `Get-RDSHDrainMode` | `PSObject` | Full drain mode state with audit data |
| `Test-RDSHDrainMode` | `bool` | `$true` if connections allowed, `$false` if alert |
| `Get-RDSHDrainHistory` | `PSObject[]` | Audit trail records |
| `Install-RDSHDrainAudit` | — | Configure registry auditing (one-time) |
| `Get-RDSHDrainNotification` | `PSObject` | Current notification configuration |
| `Set-RDSHDrainNotification` | — | Update notification settings *(deprecated — use target cmdlets below)* |
| `Test-RDSHDrainNotification` | — | Send test notification to configured backends |
| `Get-RDSHDrainNotificationTarget` | `PSObject[]` | Lists all configured notification targets with full detail |
| `Add-RDSHDrainNotificationTarget` | — | Adds a notification target (webhook or ntfy) with per-target triggers |
| `Remove-RDSHDrainNotificationTarget` | — | Removes a notification target by URL |

### Examples

```powershell
# Rich status
Get-RDSHDrainMode

# Boolean check for scripts
if (-not (Test-RDSHDrainMode)) {
    Send-Alert "RDSH drain mode active on $env:COMPUTERNAME"
}

# Recent changes with attribution
Get-RDSHDrainHistory -ChangesOnly -Limit 5 | Format-Table

# Custom grace period
Get-RDSHDrainMode -GraceMinutes 120
```

---

## ⚙️ Service

The `DrainCtl` Windows Service provides:

- **Instant detection** via `RegNotifyChangeKeyValue` (~0ms)
- **Change attribution** via `EvtSubscribe` on Security Event ID 4657 (~200ms)
- **Safety-net polling** every 5 minutes (configurable)
- **Session tracking** via `WTSEnumerateSessionsW` — active, disconnected, and total counts
- **Exclusive audit file** ownership — no corruption from concurrent access
- **In-memory store** — CLI/PS queries are instant (named pipe, no file I/O)
- **Event Log entries** — warnings for grace, errors for alerts, info for transitions
- **Auto-registration** with dashboard on startup when `dashboard.url` is set (idempotent)

### Event Log

Events are written to `Application` log under source `DrainCtl`:

| Event ID | Level | Description |
|----------|-------|-------------|
| 1000 | Info | Service started |
| 1001 | Info | Service stopped |
| 1002 | Info | Check: healthy |
| 1003 | Info | Configuration reloaded |
| 1004 | Info | State transition detected |
| 2000 | Warning | Check: drain mode in grace period |
| 3000 | Error | Check: drain mode alert (grace exceeded) |
| 3001 | Error | Registry read failure |

---

## 🔔 Notifications

DrainCtl supports **multi-target notifications** — configure any number of webhook and ntfy.sh targets, each with its own trigger filter and repeat interval.

### Setup

```powershell
# Add notification targets
drainctl notify add webhook https://hooks.slack.com/services/T.../B.../xxx --triggers drain_on,drain_off,alert,healthy --repeat 30
drainctl notify add ntfy https://ntfy.sh/my-drainctl-alerts --triggers alert,session_warning --repeat 0

# List configured targets
drainctl notify list

# Remove a target
drainctl notify remove https://hooks.slack.com/services/T.../B.../xxx

# Test all targets
drainctl notify test

# Convenience shortcuts (add/update a single target)
drainctl notify set-webhook https://hooks.example.com/drainctl
drainctl notify set-ntfy https://ntfy.sh/my-alerts

# PowerShell
Get-RDSHDrainNotification
Test-RDSHDrainNotification
```

### Granular Triggers

Each notification target can subscribe to specific event types:

| Trigger | Description |
|---------|-------------|
| `drain_on` | Drain mode activated (new connections blocked) |
| `drain_off` | Drain mode deactivated (connections restored) |
| `grace_entered` | Drain mode entered grace period |
| `alert` | Drain mode exceeded grace period |
| `healthy` | Returned to healthy state |
| `session_warning` | Session utilization threshold exceeded |

If no triggers are specified, the target receives all events.

### Config Example

Notification targets are defined in `config.json`:

```json
{
  "notifications": [
    {
      "type": "webhook",
      "url": "https://hooks.slack.com/services/T.../B.../xxx",
      "triggers": ["drain_on", "drain_off", "alert", "healthy"],
      "repeat_minutes": 30
    },
    {
      "type": "ntfy",
      "url": "https://ntfy.sh/my-alerts",
      "triggers": ["alert", "session_warning"],
      "repeat_minutes": 0
    }
  ]
}
```

### Webhook Payload

```json
{
  "event": "drain_on",
  "host": "RDSH01",
  "drain_mode": "ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS",
  "previous_mode": "ALLOW_ALL_CONNECTIONS",
  "status": "Grace",
  "message": "Drain mode active, within grace period (45m remaining).",
  "changed_by": "DOMAIN\\admin",
  "state_duration_seconds": 900,
  "timestamp": "2026-04-03T14:30:00-04:00",
  "sessions": {
    "active_sessions": 12,
    "disconnected_sessions": 3,
    "total_sessions": 15,
    "max_sessions": 25,
    "utilization_pct": 60
  }
}
```

The `event` field uses the trigger name (`drain_on`, `drain_off`, `grace_entered`, `alert`, `healthy`, `session_warning`). ntfy messages use priority `high` for alerts, `default` for other events.

---

## 🔧 Configuration

Configuration lives in a JSON file, hot-reloaded via event-based (ReadDirectoryChangesW) with poll fallback:

**Path:** `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`

- Atomic writes with cross-process mutex — safe for concurrent access
- Auto-migrates from registry on first run (existing v26 installs upgrade seamlessly)
- No service restart needed — changes are picked up automatically

### Full config.json Example

```json
{
  "grace_period_minutes": 60,
  "retention_days": 90,
  "poll_interval_seconds": 300,
  "audit_path": "C:\\ProgramData\\LISS Technologies\\LISSTech DrainCtl\\audit.jsonl",
  "session_warning_threshold": 80,
  "dashboard": {
    "url": ""
  },
  "notifications": [
    {
      "type": "webhook",
      "url": "https://hooks.slack.com/services/T.../B.../xxx",
      "triggers": ["drain_on", "drain_off", "alert", "healthy"],
      "repeat_minutes": 30
    },
    {
      "type": "ntfy",
      "url": "https://ntfy.sh/my-alerts",
      "triggers": ["alert", "session_warning"],
      "repeat_minutes": 0
    }
  ]
}
```

### Config Reference

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `grace_period_minutes` | int | `60` | Minutes before alerting on drain mode |
| `retention_days` | int | `90` | Days to keep audit records (1–365) |
| `poll_interval_seconds` | int | `300` | Seconds between safety-net polls |
| `audit_path` | string | `%ProgramData%\...\audit.jsonl` | Audit file path |
| `session_warning_threshold` | int | `80` | Session utilization % that triggers `session_warning` (0 = disabled) |
| `dashboard.url` | string | *(empty)* | Dashboard URL for auto-registration on service startup |
| `notifications` | array | `[]` | Notification targets (see below) |

### Notification Target Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"webhook"` or `"ntfy"` |
| `url` | string | yes | Endpoint URL |
| `triggers` | string[] | no | Event types to notify on (omit for all) |
| `repeat_minutes` | int | no | Re-alert interval while condition persists (0 = notify once) |

---

## 🔒 Audit Setup

To attribute drain mode changes to specific users, run once as admin:

```powershell
drainctl audit-setup
# or
Install-RDSHDrainAudit
```

This configures:
1. `auditpol` — enables Registry subcategory auditing
2. SACL on `HKLM\...\Terminal Server` — tracks `SetValue` operations

> **Domain-joined machines:** Local `auditpol` settings are overwritten by Group Policy refresh (~90 min). Configure the equivalent GPO:
> `Computer Configuration > Policies > Windows Settings > Security Settings > Advanced Audit Policy Configuration > Object Access > Audit Registry > Success`

---

## 🛠️ Building from Source

### Prerequisites

- Go 1.22+
- MinGW (for CGo / DLL build): `scoop install mingw`
- WiX Toolset 5.x: `dotnet tool install -g wix`
- .NET SDK 8.0+

### Build

```bash
just all          # Build CLI + DLL + PS module + MSI (unsigned)
just release      # Build + sign everything (requires CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env)
just lint         # go vet + gofmt + golangci-lint
just test         # Test PowerShell module
just clean        # Remove dist/
just resource     # Recompile icon/version (after changing .rc or .ico)
```

### Signing

Create `.env` from the example:

```bash
cp .env.example .env
# Set CODE_SIGNING_CERTIFICATE_THUMBPRINT=<your-cert-sha1>
```

`just release` signs in the correct order: binaries + PS module first, then builds MSI (with signed contents), then signs the MSI.

---

## 📁 Project Structure

```
LISSTech.DrainCtl/
├── drainctl.go              # Package root: version, defaults
├── registry.go              # ReadDrainMode(), DrainMode, RegistryState
├── eventlog.go              # QueryRegistryChangeUser() (wevtutil fallback)
├── audit.go                 # AuditRecord, AuditStore (file-based)
├── check.go                 # Check() — core monitoring logic
├── history.go               # GetHistory() — audit trail query
├── audit_setup.go           # RunAuditSetup() — auditpol + SACL
├── format.go                # Output formatting (plain/table/csv/json)
├── log.go                   # LogFunc, DefaultLogger, DiscardLogger
├── config.go                # ServiceConfig, NotifyConfig, JSON file with atomic writes
├── notify.go                # Multi-target webhook + ntfy.sh notification dispatch
├── sessions.go              # WTS session enumeration via wtsapi32.dll
├── internal/
│   ├── svc/                 # Windows Service handler + install/uninstall
│   ├── pipe/                # Named pipe IPC server + client
│   ├── store/               # MemAuditStore (in-memory + JSONL flush)
│   └── watcher/             # RegNotifyChangeKeyValue + EvtSubscribe
├── cmd/
│   ├── drainctl/            # CLI entry point (cobra)
│   └── cshared/             # C-shared DLL exports
├── powershell/
│   ├── LISSTech.DrainCtl.psd1
│   └── LISSTech.DrainCtl.psm1
├── installer/               # WiX 5 MSI project
├── assets/                  # Icon, event log message file
├── docs/                    # Landing page (GitHub Pages)
├── justfile                 # Build recipes
└── .env.example             # Signing configuration template
```

---

## 📄 License

**Apache License 2.0** — see [LICENSE](LICENSE).


---

<p align="center">
  <sub>Built by <a href="https://lisstech.com">LISS Consulting, Corp.</a></sub>
</p>
