# Data Model: Vite + Svelte Dashboard Migration

**Branch**: `002-vite-svelte-dashboard` | **Date**: 2026-04-09

## Overview

The dashboard frontend consumes JSON data from the Go backend API. No new entities are introduced — the migration preserves existing data contracts. This document maps the backend JSON shapes to the Svelte 5 reactive state model.

## Entities

### Server

Represents a monitored RDS server. Returned by `GET /api/v1/servers`.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| hostname | string | `hostname` | Primary key |
| status | string | `status` | `"ok"`, `"grace"`, `"alert"`, `"off"` |
| drain_mode | string | `drain_mode` | `"ALLOW_ALL_CONNECTIONS"`, `"ALLOW_RECONNECTIONS_ONLY"` |
| sessions | object | `sessions` | `{ active, disconnected, total }` |
| grace_remaining_sec | number | `grace_remaining_sec` | 0 when not in grace |
| last_seen | string | `last_seen` | ISO 8601 timestamp |
| registered_at | string | `registered_at` | ISO 8601 timestamp |
| version | string | `version` | Agent version |
| changed_by | string | `changed_by` | Who triggered last state change |
| perf | object? | `perf` | Optional `PerformanceSnapshot` (null when perf disabled) |

### PerformanceSnapshot

Nested within Server when performance monitoring is enabled.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| cpu_pct | number | `cpu_pct` | 0-100 |
| mem_avail_mb | number | `mem_avail_mb` | Available memory |
| mem_total_mb | number | `mem_total_mb` | Total memory |
| pages_sec | number | `pages_sec` | Memory pages/sec |
| disk_queue | number | `disk_queue` | Current disk queue length |
| tcp_retrans_sec | number | `tcp_retrans_sec` | TCP retransmits/sec |
| input_delay_p50_ms | number | `input_delay_p50_ms` | P50 input delay |
| input_delay_p95_ms | number | `input_delay_p95_ms` | P95 input delay |
| input_delay_max_ms | number | `input_delay_max_ms` | Max input delay |

### NotificationTarget

Part of the configuration. Managed via `GET/PUT /api/v1/notify-config`.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| type | string | `type` | `"webhook"`, `"ntfy"`, `"email"` |
| destination | string | `destination` | URL or email address |
| triggers | string[] | `triggers` | Array of trigger names |
| repeat_minutes | number | `repeat_minutes` | 0 = once, 15/60/240/480 |

### NotifyConfig

Returned by `GET /api/v1/notify-config`, sent via `PUT /api/v1/notify-config`.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| grace_period | number | `grace_period` | Minutes |
| session_warning_threshold | number | `session_warning_threshold` | 0-100, 0=disabled |
| performance | PerformanceConfig | `performance` | See below |
| notifications | NotificationTarget[] | `notifications` | Target list |

### PerformanceConfig

Nested within NotifyConfig.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| enabled | boolean | `enabled` | Master toggle |
| force_disabled | boolean | `force_disabled` | Server-side override |
| cpu_warn_pct | number | `cpu_warn_pct` | Default: 70, -1=disabled |
| cpu_crit_pct | number | `cpu_crit_pct` | Default: 85, -1=disabled |
| mem_warn_pct | number | `mem_warn_pct` | Default: 20 (% free), -1=disabled |
| mem_crit_pct | number | `mem_crit_pct` | Default: 10 (% free), -1=disabled |
| input_delay_warn_ms | number | `input_delay_warn_ms` | Default: 50, -1=disabled |
| input_delay_crit_ms | number | `input_delay_crit_ms` | Default: 100, -1=disabled |
| collect_remotefx | boolean | `collect_remotefx` | Default: false |
| collect_per_session | boolean | `collect_per_session` | Default: true |

### HealthResponse

Returned by `GET /api/v1/health` (unauthenticated).

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| version | string | `version` | DrainCtl version |
| servers | number | `servers` | Total registered |
| ok | number | `ok` | Healthy count |
| grace | number | `grace` | Grace count |
| alert | number | `alert` | Alert count |
| off | number | `off` | Offline count |

### HistoryRecord

Returned by `GET /api/v1/history/{host}`.

| Field | Type | JSON Key | Notes |
|---|---|---|---|
| timestamp | string | `timestamp` | ISO 8601 |
| status | string | `status` | Server status at that time |
| drain_mode | string | `drain_mode` | Drain mode at that time |
| sessions | object | `sessions` | Session counts at that time |
| changed_by | string? | `changed_by` | Who triggered change (if transition) |
| perf | object? | `perf` | Performance snapshot (if available) |

## Frontend State Model (Svelte 5 Runes)

### Global Reactive State

```
servers: $state<Server[]>          — All registered servers, refreshed every 30s
events: $state<EventEntry[]>       — Event log entries (append-only, capped)
stateHistory: $state<StateSample[]> — Chart data points (max 60 samples)
config: $state<NotifyConfig>       — Dashboard configuration
theme: $state<'light'|'dark'>      — Current theme, persisted to localStorage
```

### Derived State

```
counters: $derived                 — { total, ok, grace, alert, off } from servers
stateBarSegments: $derived         — Percentage widths from counters
chartData: $derived                — uPlot-formatted array from stateHistory
```

### Component-Local State

```
ConfigModal:  dirty: $state<boolean>, status: $state<'ok'|'err'|null>
TargetEdit:   editIndex: $state<number>, formData: $state<NotificationTarget>
HistoryModal: records: $state<HistoryRecord[]>, changesOnly: $state<boolean>
EventLog:     searchQuery: $state<string>, expanded: $state<boolean>
ServerTable:  expandedRow: $state<string|null>, sortColumn: $state<string>
```

## Ring Gauge Threshold Rules

### Metrics with Configurable Thresholds (from PerformanceConfig)

| Metric | Warn | Crit | Direction |
|---|---|---|---|
| CPU % | `cpu_warn_pct` (70) | `cpu_crit_pct` (85) | Higher = worse |
| Memory % Free | `mem_warn_pct` (20) | `mem_crit_pct` (10) | Lower = worse (inverted) |
| Input Delay (ms) | `input_delay_warn_ms` (50) | `input_delay_crit_ms` (100) | Higher = worse |

### Metrics with Hardcoded Thresholds (no config)

| Metric | Warn | Crit | Direction |
|---|---|---|---|
| Disk Queue | 2 | 5 | Higher = worse |
| TCP Retransmits/sec | 5% | 10% | Higher = worse |
| Session Utilization | `session_warning_threshold` | N/A (warn only) | Higher = worse |

### Color Mapping

| Range | Color | CSS Variable |
|---|---|---|
| Below warn | Green | `--color-green` (`#5d8a6e`) |
| Warn ≤ value < Crit | Amber | `--color-amber` (`#b87843`) |
| ≥ Crit | Red | `--color-red` (`#9e2a3b`) |
| No data / zero | Neutral | `--color-subtle` (`#a68e8e`) |
