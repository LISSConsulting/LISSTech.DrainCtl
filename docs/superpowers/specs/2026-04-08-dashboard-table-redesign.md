# Dashboard Redesign: Server Table + Notification Targets

**Date:** 2026-04-08
**Status:** Approved
**Prototypes:** Servers: `accordion-v7.html` | Targets: `targets-v1.html` in `.superpowers/brainstorm/36784-1775661376/content/`

## Problem

1. **Server cards** become unwieldy at 14+ servers. Card grid makes it hard to get a quick sitrep across the fleet. Performance data is buried in collapsible sections per card.
2. **Notification targets** in the config dialog are rendered as inline form rows that overwhelm the UI with as few as two targets. No easy way to scan, compare, or manage targets.

## Design: Server List

### Summary Table

Replace the card grid with a dense data table. Columns:

| Column | Source |
|--------|--------|
| Status dot | derived from `last_result.status` |
| Host | `hostname` (first component) |
| Status | badge (Healthy/Grace/Alert/Offline) — brutal style: solid fill, dark border, box-shadow |
| Mode | `last_result.drain_mode` |
| Duration | `last_result.state_duration_seconds` formatted |
| Sessions | `sessions.total_sessions`/`sessions.max_sessions` |
| CPU | `performance.cpu_pct` |
| Mem Free | computed from `mem_avail_mb`/`mem_total_mb` |
| Input Dly | `performance.input_delay_max_ms` |
| Last Seen | relative time from `last_seen` |

- Alternating row backgrounds (subtle: `#f9f2ef`)
- Filter bar (All/Healthy/Grace/Alert/Offline pills + search) above table
- Clickable rows expand an inline accordion detail panel

### Accordion Detail Panel

Clicking a server row expands a detail section below it with 3 tiles side by side:

#### Tile 1: Resource Utilization (flex: 1.4)

**Ring gauges** — 3 side-by-side SVG ring gauges (56px), each `flex: 1` so they center above their legend column:
- **Sessions**: fill = `utilization_pct`, color = green/amber/red by threshold
- **CPU**: fill = `cpu_pct`
- **Memory**: fill = memory used %, computed from `(total - avail) / total * 100`

Ring gauge colors: green (`--green`) when healthy, amber (`--amber`) when elevated, red (`--red`) when critical. Thresholds match the existing notification trigger thresholds from config.

**Sparklines** — tiny canvas area charts below each ring, same 3-column layout. 20 data points from history, showing trend over the last ~100 minutes. Time labels: "{duration} ago" / "now".

Data source: `HandleHistory()` audit records for CPU and sessions. Memory/disk queue require either adding fields to `AuditRecord` or an in-memory ring buffer in the dashboard JS.

**Legend** — 3-column grid below sparklines, aligned to rings:
- Sessions: Active, Disconn., Total/Max
- CPU: Host %, Sess P95 (with tooltip)
- Memory: Free MB, Total MB, Sess P95 (with tooltip)

Alternating row backgrounds in legend.

#### Tile 2: I/O & Network (flex: 1)

Same ring + sparkline + legend pattern:
- **Rings**: Disk Queue (absolute value), Input Delay Max (ms), TCP Retrans (/s)
- **Sparklines**: same 20-point canvas area charts
- **Legend columns**:
  - Input Delay: P50, P95, Max (with tooltips on P50/P95)
  - Disk & Net: Pages/sec, Disk Queue, TCP Retrans

#### Tile 3: Server Details (flex: 1)

Key-value list, logically ordered:
1. Drain Mode
2. State Since (absolute timestamp)
3. Duration
4. Grace Period
5. Grace Left (conditional, amber — only when status = Grace)
6. Grace Exceeded (conditional, red — only when status = Alert)
7. Changed By
8. Last Seen
9. Agent Version

Action buttons pinned to bottom: **History** | **Remove**

Hostname and Status are NOT shown (redundant with the table row above).

#### Offline servers

Only show the Server Details tile (no rings/sparklines/legend — no data available).

### Typography

The entire accordion uses `JetBrains Mono` exclusively. Font sizes:
- Ring percentage: 15px
- Ring unit label: 10px
- Ring category label: 11px
- Tile header: 11px
- Legend header: 10px
- Legend rows: 12px
- Server Details KV rows: 12px
- Sparkline time labels: 9px

### Tooltips

Jargon terms (P50, P95, Sess P95) get hover tooltips explaining them in plain English:
- **P50**: "Median — half of all measurements are below this value. Represents the typical user experience."
- **P95**: "95th percentile — 95% of measurements are below this value. Shows the worst experience most users face, excluding extreme outliers."
- **Sess P95**: "95th percentile across all sessions — 95% of sessions use less than this. Highlights the heaviest users without being skewed by a single outlier."

Styled with dotted underline, dark tooltip with arrow, 220px width.

### Status Badges (Brutal)

Solid filled backgrounds, white text, 2px dark border, 2px box-shadow offset:
- Healthy: `--green`
- Grace: `--amber`
- Alert: `--red`
- Offline: `--subtle`

## Design: Notification Targets

### Config Dialog — Target Table

Replace the inline form rows with a data table:

| Column | Content |
|--------|---------|
| Enabled | Green/grey status dot |
| Type | Pill badge: `webhook` / `ntfy` / `email` (brutal, type-colored) |
| Destination | Truncated URL/topic/email (monospace, `max-width: 200px`, ellipsis) |
| Triggers | Pill badges for each enabled trigger |
| Repeat | Interval value (monospace) |
| Actions | Edit / Delete buttons |

**+ Add Target** button in the section header, right-aligned.

Status column shows a green/grey dot indicating enabled/disabled state. Disabled targets render with reduced opacity on destination, triggers, and repeat columns.

Type pills use brutal badge style with type-specific colors:
- Webhook: `--accent` (dusty rose)
- Ntfy: `--green` (sage)
- Email: `--amber` (warm brown)

Alternating row backgrounds (`#f9f2ef`).

### Add/Edit Target Modal

Clicking Edit or "+ Add Target" opens a separate modal overlay with:

**Form fields (dynamic by type):**
- **Type selector** — pill buttons: Webhook / Ntfy / Email. Switching type changes all field labels and hints dynamically:
  - Webhook: URL field ("Webhook URL"), HMAC Secret field
  - Ntfy: URL field ("Ntfy Topic URL"), Access Token field
  - Email: SMTP Server field, SMTP Password field, From Address, To Addresses (comma-separated)
- **Triggers** — 2-column checkbox grid with highlight on checked state. Triggers: Drain On, Drain Off, Alert (Grace Exceeded), Healthy (Recovered), Grace Entered, Session Warning, CPU Warning, Memory Warning, Input Delay Warning
- **Repeat interval** — pill selector: Once / 15m / 1h / 4h / 8h
- **Enabled** — toggle checkbox with description text

**Actions (bottom bar, separated by border):**
- Left: **Test** button (green) — sends a test notification and shows inline result with status code and latency
- Right: **Cancel** / **Save** buttons

### Delete Confirmation Modal

Clicking Delete opens a small centered confirmation modal:
- Title: "Delete Target"
- Message: "Are you sure you want to delete this notification target?"
- Shows the target type (bold) and destination (mono, word-break)
- Red warning: "This action cannot be undone. Active notifications using this target will stop immediately."
- Buttons: **Cancel** / **Delete Target** (solid red fill)

Uses the same overlay pattern (click outside or Cancel to dismiss).

### Prototypes

- Server accordion: `.superpowers/brainstorm/36784-1775661376/content/accordion-v7.html`
- Notification targets: `.superpowers/brainstorm/36784-1775661376/content/targets-v1.html`

## Data Requirements

### Available Now
- All ring gauge data: sessions, CPU, memory, disk queue, input delay, TCP retrans — from `CheckResult.Sessions` and `CheckResult.Performance`
- Session/CPU history for sparklines: from `AuditRecord` fields `ActiveSessions`, `TotalSessions`, `MaxSessions`, `CPUPct`, `InputDelayMax`

### Needs Implementation
- **Memory sparkline**: `MemAvailMB` not persisted in `AuditRecord`. Options:
  - (a) Add `mem_avail_mb` and `mem_total_mb` fields to `AuditRecord`
  - (b) In-memory ring buffer in dashboard JS from live heartbeats (resets on reload)
  - Recommendation: option (a) for persistence
- **Disk Queue sparkline**: same situation — `DiskQueue` not in `AuditRecord`
  - Add `disk_queue` field to `AuditRecord`
- **TCP Retrans sparkline**: same — add `tcp_retrans_sec` to `AuditRecord`

## Implementation Notes

- Dashboard is a single embedded HTML file (`internal/dashboard/dashboard.html`)
- All rendering is plain HTML string generation (no framework)
- Sparklines use canvas 2D — no additional library needed
- Ring gauges are inline SVG with `stroke-dasharray`/`stroke-dashoffset`
- uPlot (already embedded ~50KB) could replace canvas sparklines for consistency with the main chart, but canvas is lighter for these tiny area charts
- The existing card grid, filter bar, history modal, and config modal patterns remain — this redesign replaces the server card rendering and notification target section within the config modal
