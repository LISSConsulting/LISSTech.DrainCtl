<p align="center">
  <img src="docs/logo.png" width="96" alt="DrainCtl" />
</p>

<h1 align="center">LISSTech DrainCtl</h1>

<p align="center"><strong>Real-time RDSH drain-mode monitoring for Windows Server.<br/>Knows what changed, who changed it, and when — instantly.</strong></p>

<p align="center">
  <img src="https://img.shields.io/badge/STATUS-STABLE-5d8a6e?style=for-the-badge&labelColor=2d1a1a" alt="Status: stable" />
  <img src="https://img.shields.io/badge/GO-1.26%2B-a3475b?style=for-the-badge&labelColor=2d1a1a&logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/WINDOWS-Server%202016%2B-2d1a1a?style=for-the-badge" alt="Windows" />
  <img src="https://img.shields.io/badge/LICENSE-Apache%202.0-5d8a6e?style=for-the-badge&labelColor=2d1a1a" alt="License" />
</p>

<p align="center">
  <a href="https://github.com/LISSConsulting/LISSTech.DrainCtl/releases/latest"><img src="https://img.shields.io/github/v/release/LISSConsulting/LISSTech.DrainCtl?style=for-the-badge&label=RELEASE&color=a3475b&labelColor=2d1a1a" alt="Latest stable release" /></a>
  <a href="https://www.powershellgallery.com/packages/LISSTech.DrainCtl"><img src="https://img.shields.io/powershellgallery/v/LISSTech.DrainCtl?style=for-the-badge&label=PSGALLERY&color=b87843&labelColor=2d1a1a" alt="Stable PSGallery release" /></a>
</p>

DrainCtl is stable for production deployment. Stable GitHub releases are signed, published as
**Latest**, and tracked by the default auto-update channel.

```powershell
Install-Module LISSTech.DrainCtl
```

---

<h2 id="what-it-is">▎ What it is</h2>

A Windows service that watches `TSServerDrainMode` on RDSH hosts and tells you:

- **What changed** — open/drain/restart/denied, with sub-millisecond detection.
- **Who changed it** — Security event 4657 attribution, joined to the registry change.
- **When it changed** — append-only audit trail, persisted to local SQLite.
- **What it cost you** — CPU, memory, input delay, sessions, RemoteFX, and event-log anomalies, each on a per-poll cycle with threshold alerts.

Query from CLI, PowerShell, or your RMM. Answers come from a named pipe in under 1 ms. No agents, no cloud — and zero per-host config once a host is pointed at a dashboard.

---

<h2 id="toc">▎ Table of contents</h2>

[Architecture](#architecture) · [Install](#install) · [Quick start](#quick-start) · [CLI](#cli) · [PowerShell](#powershell) · [Service](#service) · [Notifications](#notifications) · [evtspike](#evtspike) · [Configuration](#configuration) · [Audit setup](#audit-setup) · [Build](#build) · [Project layout](#project-layout) · [License](#license)

---

<h2 id="architecture">▎ Architecture</h2>

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'primaryColor': '#a3475b', 'primaryTextColor': '#fff', 'primaryBorderColor': '#2d1a1a', 'secondaryColor': '#f5ebe8', 'tertiaryColor': '#fdf8f6', 'lineColor': '#2d1a1a', 'fontFamily': 'monospace', 'fontSize': '13px'}}}%%
graph TB
    subgraph SVC["DrainCtl Windows Service"]
        RNK["RegNotifyChangeKeyValue"] -->|"registry changed"| CHECK["runCheck()"]
        EVT["EvtSubscribe 4657"] -->|"who changed it"| CHECK
        POLL["Poll Ticker 60s"] -->|"safety net"| CHECK
        CFG["config.json watcher"] -->|"config changed"| RELOAD["ReloadConfig()"]
        CHECK --> SESS["WTS Session Enum"]
        SESS --> STORE["SQLite Telemetry Store<br/>(audit + metrics + maintenance)"]
        CHECK --> PERF["PDH Counters"]
        CHECK --> STORE
        CHECK --> ELOG["Event Log + ETW"]
        CHECK --> NOTIFY["Multi-target dispatch"]
        NOTIFY --> WH["Webhook 1..N"]
        NOTIFY --> NTFY["ntfy 1..M"]
        NOTIFY --> EMAIL["SMTP"]
        STORE --> PIPE["Named Pipe"]
        AGG["Aggregator (5m + 1h)"] --> STORE
        RET["Retention + WAL checkpoint"] --> STORE
    end

    CLI["drainctl.exe"] -->|"pipe"| PIPE
    PS["PowerShell module"] -->|"pipe"| PIPE
    RMM["RMM / script monitor"] --> CLI

    CLI -.->|"fallback"| REG["Registry"]
    PS -.->|"fallback"| REG
```

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'primaryColor': '#a3475b', 'primaryTextColor': '#fff', 'primaryBorderColor': '#2d1a1a', 'secondaryColor': '#f5ebe8', 'tertiaryColor': '#fdf8f6', 'lineColor': '#2d1a1a', 'fontFamily': 'monospace', 'fontSize': '13px', 'actorBkg': '#a3475b', 'actorTextColor': '#fff', 'actorBorder': '#2d1a1a', 'signalColor': '#2d1a1a', 'noteBkgColor': '#f5ebe8', 'noteBorderColor': '#2d1a1a'}}}%%
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

<h2 id="install">▎ Install</h2>

### MSI (recommended)

```powershell
msiexec /i LISSTech.DrainCtl.msi /qn
```

Lays down:

```
C:\Program Files\LISS Technologies\LISSTech DrainCtl\
  ├── bin\                 drainctl.exe + drainctld.exe + drainctl.dll
  ├── Scripts\             collect-diags.ps1, install-diag-task.ps1
  └── Tasks\               DrainCtl-Diags.xml (disabled hourly diagnostics)

C:\Program Files\WindowsPowerShell\Modules\LISSTech.DrainCtl\
  ├── LISSTech.DrainCtl.psd1
  └── LISSTech.DrainCtl.psm1

C:\ProgramData\LISS Technologies\LISSTech DrainCtl\
  ├── config.json          atomic-write JSON, hot-reloaded
  └── drainctl.db          SQLite telemetry (WAL)

Service                    DrainCtl, auto-start, LocalSystem
ETW provider               LISS Technologies-DrainCtl (Operational + Debug)
Event log source           DrainCtl
```

Unattended deploy:

```powershell
msiexec /i LISSTech.DrainCtl.msi /qn `
  INSTALL_MODE=registration `
  DASHBOARD_URL=https://dash.example.com:49470 `
  WEBHOOK_URL=https://hooks.example.com/drain
```

> Upgrading from v26-pre-007? First service start auto-migrates `audit.jsonl` into the SQLite store and renames the source file. No manual steps.

### PowerShell Gallery (module only)

```powershell
Install-Module -Name LISSTech.DrainCtl -Scope AllUsers
```

Cmdlets only — no service, no CLI. Queries hit the registry directly. See [PSGallery](https://www.powershellgallery.com/packages/LISSTech.DrainCtl).

### Manual (CLI only)

Copy `drainctl.exe` somewhere on PATH and run it. Service is optional.

---

<h2 id="quick-start">▎ Quick start</h2>

```powershell
PS> drainctl check
2026-04-29T13:03:01-04:00 [INF] source=service
2026-04-29T13:03:01-04:00 [INF] host=RDSH01
2026-04-29T13:03:01-04:00 [INF] drain_mode=ALLOW_ALL_CONNECTIONS value=0
2026-04-29T13:03:01-04:00 [INF] state_since=2026-04-29T11:24:05-04:00 state_duration=1h38m56s
2026-04-29T13:03:01-04:00 [INF] grace_period=1h0m0s
2026-04-29T13:03:01-04:00 [INF] sessions=12 active / 3 disconnected / 15 total (60% of 25)
2026-04-29T13:03:01-04:00 [OK ] status=Healthy connections_allowed=true exit=0

PS> drainctl check --format json | ConvertFrom-Json
PS> drainctl history --limit 5

PS> Get-RDSHDrainMode | Format-List
PS> if (Test-RDSHDrainMode) { 'OK' } else { 'ALERT' }
PS> Get-RDSHDrainHistory -ChangesOnly -Limit 10
```

---

<h2 id="cli">▎ CLI</h2>

```
drainctl check                Check drain-mode state
drainctl history              View audit trail
drainctl audit-setup          Configure registry auditing (one-time, admin)
drainctl service install      Install Windows service
drainctl service uninstall    Remove Windows service
drainctl service start        Start the service
drainctl service stop         Stop the service
drainctl notify add|remove|list|test
                              Manage notification targets
drainctl notify set-webhook|set-ntfy
                              Convenience: add/update a single target
drainctl register             Register this host with the dashboard
drainctl dashboard            Dashboard helpers (cert info, etc.)
drainctl configure            Interactive/flag-driven config editor
drainctl sessions             Show live RDSH session enumeration
drainctl baseline reset       Wipe the evtspike anomaly-detector baseline
```

**Global flags**

| Flag | Default | What |
|---|---|---|
| `--db` | `%ProgramData%\…\drainctl.db` | Path to the SQLite DB or its parent dir. Always opened read-only — the service owns the writer. Legacy `audit.jsonl` paths are accepted and resolved to `drainctl.db` in the same folder. |
| `--format` | `plain` (check) / `table` (history) | `plain`, `table`, `csv`, `json` |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

**`check` flags** — `--grace <minutes>` (default 60), `--retention <days>` (legacy; service-side `retention.*` supersedes).
**`history` flags** — `--limit <n>` (default 50), `--changes-only`.

**Exit codes** — `0` healthy or in grace, `1` alert (drain past grace), `2` registry unreadable.

---

<h2 id="powershell">▎ PowerShell</h2>

```powershell
Import-Module LISSTech.DrainCtl
```

| Cmdlet | Returns | What |
|---|---|---|
| `Get-RDSHDrainMode` | `PSObject` | Full drain-mode state with audit data |
| `Test-RDSHDrainMode` | `bool` | `$true` if connections allowed |
| `Get-RDSHDrainHistory` | `PSObject[]` | Audit trail records |
| `Install-RDSHDrainAudit` | — | Configure registry auditing (one-time) |
| `Get-RDSHDrainNotification` | `PSObject` | Current notification configuration |
| `Test-RDSHDrainNotification` | — | Send test notification to all backends |
| `Get-RDSHDrainNotificationTarget` | `PSObject[]` | All configured targets, with detail |
| `Add-RDSHDrainNotificationTarget` | — | Add a target with per-target triggers |
| `Remove-RDSHDrainNotificationTarget` | — | Remove a target by URL |
| `Set-RDSHDrainNotification` | — | *(deprecated — use the target cmdlets)* |

```powershell
Get-RDSHDrainMode

if (-not (Test-RDSHDrainMode)) {
    Send-Alert "RDSH drain mode active on $env:COMPUTERNAME"
}

Get-RDSHDrainHistory -ChangesOnly -Limit 5 | Format-Table

Get-RDSHDrainMode -GraceMinutes 120
```

---

<h2 id="service">▎ Service</h2>

The `DrainCtl` service does the watching:

- `RegNotifyChangeKeyValue` — sub-millisecond change detection
- `EvtSubscribe` on Security 4657 — change attribution (~200 ms after the event)
- Poll ticker — safety net (default 5 min)
- `WTSEnumerateSessionsW` — session enumeration (active / disconnected / total / utilization%)
- SQLite telemetry store — single-writer service, read-only CLI, WAL mode
- Named pipe — instant CLI/PS answers without touching disk
- Event Log + ETW — every transition logged through both sinks
- Auto-discovery — DNS SRV `_drainctl._tcp.<domain>` finds the dashboard with zero per-machine config
- HTTPS by default — auto-generated self-signed cert or your own PEM files

Internally the service is split into lifecycle-owned subsystems with bounded
`Start`/`Stop` behavior: dashboard, telemetry, registry/config watchers, named
pipe IPC, auto-update, self-metrics, pprof, spike forwarding, registration, and
performance collection each own their workers and shutdown path. The remaining
Windows service loop is wiring and dispatch only.

### Diagnostic profiling (opt-in, loopback-only)

```powershell
[Environment]::SetEnvironmentVariable('DRAINCTL_PPROF_PORT','6060','Machine')
Restart-Service DrainCtl

go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine
curl -o heap.pprof http://127.0.0.1:6060/debug/pprof/heap
```

Bound to `127.0.0.1` only. Off by default. Unset the variable and restart to disable.

### Event log

Source `DrainCtl` on the Application channel:

| ID | Level | Meaning |
|---|---|---|
| 1000 | Info | Service started |
| 1001 | Info | Service stopped |
| 1002 | Info | Check: healthy |
| 1003 | Info | Configuration reloaded |
| 1004 | Info | State transition detected |
| 2000 | Warning | Drain mode in grace period |
| 3000 | Error | Drain mode alert (grace exceeded) |
| 3001 | Error | Registry read failure |

### Logging sinks

`slog`-based, two sinks in parallel:

- **File** — `…\drainctl.log`. Rotates at local midnight to `drainctl-YYYY-MM-DD.log`; archives older than 7 days are pruned. Level: `log_file_level` in `config.json`.
- **ETW** — manifest provider `LISS Technologies-DrainCtl` with Operational (INF+) and Debug (DBG) channels. Disabled by default; `wevtutil sl /e:true` to enable. Level: `log_event_level`.

Both accept `debug`, `info`, `warn`, `error`. CLI verbosity is separate (`--log-level`).

### Dashboard-authoritative configuration

When an agent is registered with a dashboard, the dashboard owns: grace period, session/perf thresholds, all of `performance.*`, `evtspike.enabled`, and the notification target list. Agents pull on every poll cycle and synchronously on local `config.json` reload — dashboard edits propagate within one poll, local edits don't diverge silently.

---

<h2 id="notifications">▎ Notifications</h2>

Multi-target. Any number of webhook + ntfy + email (SMTP) targets, each with its own trigger filter and repeat cadence.

```powershell
drainctl notify add webhook https://hooks.slack.com/services/T.../B.../xxx `
  --triggers drain_on,drain_off,alert,healthy --repeat 30
drainctl notify add ntfy https://ntfy.sh/my-alerts `
  --triggers alert,session_warning --repeat 0
drainctl notify add email smtp://smtp.example.com:587 `
  --to ops@example.com --from drainctl@example.com `
  --secret smtp-password --triggers drain_on,drain_off,alert

drainctl notify list
drainctl notify remove https://hooks.slack.com/services/T.../B.../xxx
drainctl notify test
```

### Triggers

| Name | When |
|---|---|
| `drain_on` | Drain activated (new connections blocked) |
| `drain_off` | Drain deactivated |
| `grace_entered` | Drain entered grace period |
| `alert` | Drain exceeded grace period |
| `healthy` | Returned to healthy |
| `session_warning` | Session utilization above threshold |
| `cpu_warning` / `cpu_critical` | CPU above threshold (2 consecutive polls) |
| `memory_warning` / `memory_critical` | Available memory below threshold (2 consecutive polls) |
| `input_delay_warning` / `input_delay_critical` | Input delay P95 above threshold |
| `event_spike` | Confirmed anomalous activity on a watched event-log channel (requires `evtspike.enabled: true`; not in the default trigger set) |

Omit `--triggers` to receive everything.

### Webhook payload

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
  "timestamp": "2026-04-29T13:03:01-04:00",
  "sessions": {
    "active_sessions": 12, "disconnected_sessions": 3,
    "total_sessions": 15, "max_sessions": 25, "utilization_pct": 60
  },
  "performance": {
    "cpu_pct": 78.3, "mem_avail_mb": 2048, "mem_total_mb": 16384,
    "input_delay_p95_ms": 42, "input_delay_max_ms": 88, "disk_queue": 0.3
  }
}
```

ntfy uses priority `high` for alerts, `default` otherwise. Email renders through a card template — subject emoji (⚠️ / 🚨) reflects severity.

---

<h2 id="evtspike">▎ evtspike</h2>

Opt-in event-log anomaly detector. Watches 54 curated Windows channels (Winlogon, Kerberos, NTLM, LSA, SMB, FSLogix, TerminalServices, PrintService, User Profile Service, DNS, TCP/IP, Defender, …) and fires `event_spike` notifications on confirmed deviations from a learned per-slot Bayesian baseline. Designed to catch what fixed thresholds can't — auth bursts, SMB storms, print floods, profile-load failures.

- 96 time-of-day slots × per-channel baseline. Robust update cap blocks a single flood from poisoning the model — a follow-on smaller anomaly still flags.
- 2-of-3 confirmation across 10-second scoring windows. One-shot transients don't page.
- Persists `baseline.json` every 15 min and on shutdown. Restarts skip warm-up.
- Routes through the existing notification pipeline. Severity is set per-target, not by the detector.

### Enable

```json
{
  "evtspike": { "enabled": true },
  "notifications": [
    {
      "type": "webhook",
      "url": "https://hooks.example.com/drainctl-spikes",
      "triggers": ["event_spike"],
      "repeat_minutes": 30
    }
  ]
}
```

`event_spike` is not in the default trigger set — existing targets that upgrade do not silently start receiving spike notifications. Explicit opt-in required.

Scalar tunables hot-reload. Channel-list changes restart the subsystem.

### Channel tuning

```json
{
  "evtspike": {
    "enabled": true,
    "disabled_channels": ["Microsoft-Windows-Crashdump/Operational"],
    "added_channels": ["Microsoft-Windows-PowerShell/Operational"]
  }
}
```

The 54-channel default is in `internal/evtspike/channels.go`. Operators tailor by naming what to remove / add — no need to restate the whole list.

### Security channel opt-in

```json
{ "evtspike": { "enabled": true, "security_channel_enabled": true } }
```

> **Read first.** This grants the service the ability to read and clear the Security log, manage audit policy, and set SACLs on this host.

`LocalSystem` already has `SeSecurityPrivilege` in its token; the subsystem enables it at Start. A dedicated service account must be granted the privilege manually (User Rights Assignment → "Manage auditing and security log") before the subscription succeeds.

### Webhook payload

```json
{
  "event": "event_spike",
  "host": "RDSH01",
  "status": "warning",
  "message": "Event spike on Application (20 vs ~0.1)",
  "timestamp": "2026-04-29T13:03:01-04:00",
  "spike": {
    "channel": "Application",
    "observed": 20, "expected": 0.1, "tail_probability": 0,
    "window_start": "2026-04-29T13:02:51-04:00",
    "window_end":   "2026-04-29T13:03:01-04:00",
    "confirmation_count": 2
  }
}
```

ntfy uses priority 3 for `warning`, 4 for `alert`, with tags `["evtspike", <host>, <channel-basename>]`.

### Config

| Key | Type | Default | What |
|---|---|---|---|
| `evtspike.enabled` | bool | `false` | Master opt-in. When `false`, the subsystem is constructed but quiescent. |
| `evtspike.security_channel_enabled` | bool | `false` | Adds `Security` to the watched list; enables `SeSecurityPrivilege` on the service token at Start. |
| `evtspike.disabled_channels` | string[] | `[]` | Remove from the default list (case-insensitive). |
| `evtspike.added_channels` | string[] | `[]` | Extra channels beyond the default. |
| `evtspike.threshold` | float | `1e-4` | Negative-binomial tail probability threshold for "anomalous". |
| `evtspike.min_count` | int | `10` | Lower observed-event floor; below this never flags. |
| `evtspike.cooldown_minutes` | int | `10` | Min time between spikes for the same `(host, channel)`. |
| `evtspike.slot_maturity_observations` | int | `90` | Observations before a slot's posterior is "mature" (≈ one full 15-min visit at 10-s scoring). |
| `evtspike.persist_interval_seconds` | int | `900` | Baseline file write cadence. |
| `evtspike.half_life_buckets` | int | `360` | Exponential-forgetting half-life in 10-s buckets (~1 h). |
| `evtspike.prior_strength` | float | `60.0` | Gamma prior α/β strength (bucket-equivalents of "pretend evidence"). |
| `evtspike.mean_per_bucket_prior` | float | `0.1` | Gamma prior mean: expected events per 10-s bucket before learning. |

All scalar fields hot-reload. See `specs/006-evtspike-detection/quickstart.md` for end-to-end setup, injection recipes, and troubleshooting.

---

<h2 id="configuration">▎ Configuration</h2>

JSON file, hot-reloaded via `ReadDirectoryChangesW` with poll fallback.

**Path** — `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`

- Atomic writes with cross-process mutex.
- Auto-migrates from the v26-pre-007 registry layout on first run.
- No service restart required for any documented change.

```json
{
  "grace_period_minutes": 60,
  "retention_days": 90,
  "retention": { "metrics_days": 30, "audit_days": 365 },
  "telemetry": { "aggregator_interval_seconds": 60, "retention_interval_minutes": 15 },
  "poll_interval_seconds": 300,
  "session_warning_threshold": 80,
  "performance": {
    "enabled": true,
    "cpu_warn_pct": 70, "cpu_crit_pct": 85,
    "mem_warn_pct": 20, "mem_crit_pct": 10,
    "input_delay_warn_ms": 50, "input_delay_crit_ms": 100,
    "collect_remotefx": false, "collect_per_session": true
  },
  "dashboard": { "url": "" },
  "notifications": [
    {
      "type": "webhook",
      "url": "https://hooks.slack.com/services/T.../B.../xxx",
      "triggers": ["drain_on", "drain_off", "alert", "healthy"],
      "repeat_minutes": 30
    },
    {
      "type": "ntfy", "url": "https://ntfy.sh/my-alerts",
      "triggers": ["alert", "session_warning"], "repeat_minutes": 0
    },
    {
      "type": "email", "url": "smtp://smtp.example.com:587",
      "to": ["ops@example.com", "oncall@example.com"],
      "from": "drainctl@example.com", "secret": "smtp-password",
      "triggers": ["drain_on", "drain_off", "alert"]
    }
  ]
}
```

| Key | Type | Default | What |
|---|---|---|---|
| `grace_period_minutes` | int | `60` | Minutes drain must persist before alerting. |
| `retention_days` | int | `90` | Legacy audit-retention knob, preserved for pre-007 installs. New `retention.*` fields supersede. |
| `retention.metrics_days` | int | `30` | Hourly rollup retention, 1–365. Raw + 5-min tiers have fixed retention (25 h and 6 d) — only hourly is operator-tunable. |
| `retention.audit_days` | int | `365` | Audit-record retention, 1–3650. |
| `telemetry.aggregator_interval_seconds` | int | `60` | Tick interval for 5-min and hourly rollups. |
| `telemetry.retention_interval_minutes` | int | `15` | Cadence of the retention + WAL-checkpoint worker. |
| `poll_interval_seconds` | int | `300` | Safety-net poll interval. |
| `session_warning_threshold` | int | `80` | Session utilization % that triggers `session_warning` (0 = disabled). |
| `dashboard.url` | string | *(empty)* | Dashboard URL for auto-registration; empty = SRV discovery. |
| `dashboard.tls_cert` / `tls_key` | string | *(empty)* | PEM paths; auto-generated self-signed if empty. |
| `dashboard.tls_fingerprint` | string | *(empty)* | SHA-256 cert fingerprint for agent-side pinning. |
| `performance.enabled` | bool | `false` | Master switch for PDH counter collection. |
| `performance.cpu_warn_pct` / `cpu_crit_pct` | int | `70` / `85` | CPU thresholds (`0` = use default, `-1` = disabled). |
| `performance.mem_warn_pct` / `mem_crit_pct` | int | `20` / `10` | Memory % free thresholds. |
| `performance.input_delay_warn_ms` / `input_delay_crit_ms` | int | `50` / `100` | Input delay P95 thresholds. |
| `performance.collect_remotefx` | bool | `false` | RemoteFX Graphics + Network counters. |
| `performance.collect_per_session` | bool | `true` | Per-session CPU, memory, input delay. |
| `notifications` | array | `[]` | Notification targets — see below. |

**Notification target fields**

| Field | Type | Required | What |
|---|---|---|---|
| `type` | string | yes | `"webhook"`, `"ntfy"`, or `"email"` |
| `url` | string | yes | Endpoint (`https://` for webhook/ntfy, `smtp://` or `smtps://` for email) |
| `to` | string[] | email | Recipient addresses |
| `from` | string | email | Sender address |
| `secret` | string | no | HMAC-SHA256 signing secret (webhook) or SMTP password (email) |
| `triggers` | string[] | no | Event types to notify on (omit for all) |
| `repeat_minutes` | int | no | Re-alert interval while condition persists (`0` = once) |

> SMTP transport: authenticated send (`secret` set) requires STARTTLS or implicit TLS (`smtps://`). DrainCtl refuses to transmit `AUTH` over cleartext — the error names the offending host.

### Retention & storage

Telemetry: `…\drainctl.db` (SQLite, WAL). Service is the single writer; CLI opens read-only.

- **Raw samples** — fixed **25 h** (sized to back zoom-to-raw on the dashboard).
- **5-min rollups** — fixed **6 d** (mid-range zoom).
- `retention.metrics_days` (1–365) — hourly rollups, the long-horizon tier.
- `retention.audit_days` (1–3650) — drain-mode audit events (including reconciliation rows).

Retention worker runs every `telemetry.retention_interval_minutes` (default 15), deletes expired rows in chunks, then `PRAGMA incremental_vacuum`. The same worker owns a dedicated WAL-checkpoint connection — `PASSIVE` every pass, escalating to `TRUNCATE` when WAL > 16 MB.

> **The DB file does not shrink on disk after a purge.** `incremental_vacuum` returns freed pages to SQLite's free-list; subsequent inserts reuse them. A stable file size after a large purge is expected — not a sign that retention is broken. Verify via the `maintenance_jobs` table or the dashboard maintenance widget. Full compaction (`VACUUM INTO`) is a planned follow-up.

#### Upgrading from the JSONL store

Installs that previously wrote `audit.jsonl` migrate automatically on the first service start after 007: records imported into the SQLite audit table (idempotent — duplicates ignored), source file renamed to `audit.jsonl.bak.<UTC-timestamp>`. Migration progress is recorded in `maintenance_jobs` under `name="jsonl_migration"`.

### Dashboard API

Authenticated (Kerberos SSO via `Negotiate`) HTTP API on the dashboard listener. JSON. Schemas in `internal/dashboard/openapi.yaml`.

| Endpoint | Returns |
|---|---|
| `GET /api/v1/metrics/{host}` | Per-host time-series. `resolution=raw\|1min\|5min\|hourly\|auto`. Auto picks a tier from the requested window. |
| `GET /api/v1/metrics/_fleet` | Same shape, aggregated across every known host. Includes the synthetic `mem_used_pct` counter — per-host pressure averaged across hosts (not the total-weighted ratio). |
| `GET /api/v1/audit` | Time-range query over drain-mode audit events. `host`, `actor`, `changes_only` filters. Cursor pagination. |
| `GET /api/v1/maintenance/status` | Last-run timestamp, duration, outcome, `overdue` flag for every background job. |

The legacy `GET /api/v1/history/{host}` endpoint was removed in 007 and now returns **HTTP 410 Gone** with `{"error":"use /api/v1/metrics/{host} or /api/v1/audit"}`.

### Dashboard charts

LayerCake + Svelte 5 frontend embedded into the service binary:

- **Overview** — fleet charts driven by `/api/v1/metrics/_fleet`: **LOAD** (CPU / memory used / disk queue), **Health Indicators** (input-delay p95, session utilization), **Sessions** (active / disconnected / total), and **RemoteFX** (when `collect_remotefx` is on for any host). Window presets: `1H / 1D / 3D / 5D` (the 15M preset was retired in v26.119.10 — at default 60-s polling it added no resolution over 1H).
- **Server Detail → HostLoadChart** — single-host multi-counter view of LOAD, stacked against drain-mode audit events on the same time axis.
- **Event Spikes** — status pill and recent-spikes list on Server Detail.

---

<h2 id="audit-setup">▎ Audit setup</h2>

To attribute drain-mode changes to specific users, run once as admin:

```powershell
drainctl audit-setup
# or
Install-RDSHDrainAudit
```

This configures:

1. `auditpol` — enables Registry subcategory auditing
2. SACL on `HKLM\…\Terminal Server` — tracks `SetValue` operations

> **Domain-joined hosts:** local `auditpol` settings are overwritten by Group Policy refresh (~90 min). Configure the equivalent GPO:
>
> *Computer Configuration → Policies → Windows Settings → Security Settings → Advanced Audit Policy Configuration → Object Access → Audit Registry → Success*

---

<h2 id="build">▎ Build</h2>

**Tested build toolchain** — Go 1.26.5, Node.js 24 LTS, pnpm 11.20.0, MinGW (`scoop install mingw`), and .NET SDK 10. The project restores WiX 7.0.0 and its extensions from NuGet; `installer/LISSTech.DrainCtl.wixproj` records the accepted [`wix7` EULA](https://docs.firegiant.com/wix/osmf/).

```bash
cd frontend && pnpm install --frozen-lockfile
cd ..

just all          # CLI + DLL + PS module + MSI (unsigned)
just release      # build + sign everything (needs CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env)
just lint         # go vet + gofmt + golangci-lint
just gotest       # Go tests
just vulncheck    # govulncheck
just clean        # nuke dist/
just resource     # recompile drainctl.syso (after changing .rc or .ico)
just version      # print the version the next build will embed
```

**Signing**

```bash
cp .env.example .env
# Set CODE_SIGNING_CERTIFICATE_THUMBPRINT=<your-cert-sha1>
```

`just release` signs in the right order: binaries + PS module → build MSI → sign MSI → sign the release manifest.

**Versioning** — git-derived CalVer `YY.MM.BUILD` from `scripts/version.ps1`. Injected into Go via ldflags, into `drainctl.syso` via `just resource`, into the WiX project via `-p:ProductVersion=`, into the PS module via `.psd1.tmpl` rendering. Nothing to bump by hand.

---

<h2 id="project-layout">▎ Project layout</h2>

```
LISSTech.DrainCtl/
├── drainctl.go              package root: version, defaults
├── registry.go              ReadDrainMode(), DrainMode, RegistryState
├── eventlog.go              QueryRegistryChangeUser() (wevtutil fallback)
├── audit.go                 AuditRecord (marshaled across DLL/CLI)
├── check.go                 Check() — core monitoring logic
├── history.go               GetHistory() — reads audit trail from SQLite
├── audit_setup.go           RunAuditSetup() — auditpol + SACL
├── format.go                output formatting (plain/table/csv/json)
├── log.go                   LogFunc, DefaultLogger, DiscardLogger
├── config.go                ServiceConfig, NotifyConfig, atomic-write JSON
├── notify.go                multi-target webhook + ntfy + SMTP dispatch
├── sessions.go              WTS session enum via wtsapi32.dll
├── internal/
│   ├── svc/                 Windows service loop, pipe RPC, dashboard sync,
│   │                        perf/evtspike supervisors, install/uninstall
│   ├── pipe/                named-pipe IPC server + client
│   ├── telemetry/           SQLite store: db, schema, audit, metrics,
│   │                        aggregator, retention, maintenance,
│   │                        reconcile, migrate_jsonl
│   ├── dashboard/           HTTPS listener + JSON API
│   ├── evtspike/            event-log anomaly detection (Bayesian)
│   ├── perfmon/             PDH performance counter collection
│   ├── selfmetrics/         service-process self-telemetry
│   ├── lifecycle/           subsystem start/stop scaffolding
│   └── watcher/             RegNotifyChangeKeyValue + EvtSubscribe
├── cmd/
│   ├── drainctl/            CLI entry point (cobra)
│   └── cshared/             C-shared DLL exports
├── powershell/              PS module (.psd1, .psm1)
├── installer/               WiX 7 MSI project + managed C# CA + PS scripts
├── frontend/                Svelte 5 dashboard (LayerCake charts)
├── assets/                  icon, ETW manifest, message file
├── docs/                    landing page + guide (GitHub Pages)
├── justfile                 build recipes
└── .env.example             signing configuration template
```

---

<h2 id="license">▎ License</h2>

**Apache License 2.0** — see [LICENSE](LICENSE).

---

<p align="center">
  <sub>Built by <a href="https://lisstech.com">LISS Consulting, Corp.</a> — d/b/a LISS Technologies.</sub>
</p>
