# Dashboard Table Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the server card grid with a dense data table with inline accordion detail panels (ring gauges, sparklines, legends), and replace inline notification target form rows with a scannable table plus an add/edit modal.

**Architecture:** All changes are in the single embedded HTML file `internal/dashboard/dashboard.html`. The server rendering replaces the `render()` function's card HTML generation (lines ~5302-5475) and supporting CSS/JS. The notification target redesign replaces `addTargetRow()` (lines ~5939-6135) and the config modal target section. Backend changes add perf fields to `AuditRecord`/`HistoryRecord` for sparkline data.

**Tech Stack:** Go (backend structs), HTML/CSS/JS (dashboard), SVG (ring gauges), Canvas 2D (sparklines)

**Spec:** `docs/superpowers/specs/2026-04-08-dashboard-table-redesign.md`
**Prototypes:** `.superpowers/brainstorm/36784-1775661376/content/accordion-v7.html` (servers), `targets-v1.html` (targets)

---

## Task 1: Add perf fields to AuditRecord and HistoryRecord

Backend data model change — add memory, disk queue, and TCP retrans fields so sparklines can be powered from history data.

**Files:**
- Modify: `audit.go:16-31` (AuditRecord struct)
- Modify: `format.go:180-196` (HistoryRecord struct)
- Modify: `format.go:226-253` (AuditToHistory function)
- Modify: `internal/svc/check.go:111-114` (populate new fields from perfSnap)
- Test: `internal/svc/check_test.go` (if exists), manual verification via `drainctl history --format=json`

- [ ] **Step 1: Add fields to AuditRecord**

In `audit.go`, add after line 29 (`InputDelayMax`):

```go
MemAvailMB    float64   `json:"mem_avail_mb,omitempty"`
MemTotalMB    float64   `json:"mem_total_mb,omitempty"`
DiskQueue     float64   `json:"disk_queue,omitempty"`
TCPRetransSec float64   `json:"tcp_retrans_sec,omitempty"`
```

- [ ] **Step 2: Add fields to HistoryRecord**

In `format.go`, add after line 194 (`InputDelayMax`):

```go
MemAvailMB    *float64 `json:"mem_avail_mb,omitempty"`
MemTotalMB    *float64 `json:"mem_total_mb,omitempty"`
DiskQueue     *float64 `json:"disk_queue,omitempty"`
TCPRetransSec *float64 `json:"tcp_retrans_sec,omitempty"`
```

- [ ] **Step 3: Map new fields in AuditToHistory**

In `format.go`, add after line 248 (the `InputDelayMax` block) in `AuditToHistory()`:

```go
if rec.MemAvailMB != 0 {
    v := rec.MemAvailMB
    hr.MemAvailMB = &v
}
if rec.MemTotalMB != 0 {
    v := rec.MemTotalMB
    hr.MemTotalMB = &v
}
if rec.DiskQueue != 0 {
    v := rec.DiskQueue
    hr.DiskQueue = &v
}
if rec.TCPRetransSec != 0 {
    v := rec.TCPRetransSec
    hr.TCPRetransSec = &v
}
```

- [ ] **Step 4: Populate new fields from perfSnap in svcRunCheck**

In `internal/svc/check.go`, expand the `if perfSnap != nil` block (lines 111-114):

```go
if perfSnap != nil {
    rec.CPUPct = perfSnap.CPUPct
    rec.InputDelayMax = perfSnap.InputDelayMax
    rec.MemAvailMB = perfSnap.MemAvailMB
    rec.MemTotalMB = perfSnap.MemTotalMB
    rec.DiskQueue = perfSnap.DiskQueue
    rec.TCPRetransSec = perfSnap.TCPRetrans
}
```

- [ ] **Step 5: Verify build and tests pass**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: All pass, no errors.

- [ ] **Step 6: Commit**

```bash
git add audit.go format.go internal/svc/check.go
git commit -m "feat: add mem/disk/tcp fields to AuditRecord for sparkline history"
```

---

## Task 2: Server table CSS — replace card styles with table styles

Replace the server card grid CSS with table + accordion CSS. Keep the card CSS classes that are still used by other components (like overview counters).

**Files:**
- Modify: `internal/dashboard/dashboard.html` — CSS section (lines ~395-481 server cards, ~686-750 gauge/perf)

- [ ] **Step 1: Add server table CSS**

After the existing `.srv` card styles block (around line 481), add the new table + accordion styles. Reference the prototype `accordion-v7.html` for the exact CSS — the key classes are:

- `table.srv-tbl` — main server table (full width, border-collapse, 13px font)
- `table.srv-tbl th` — monospace uppercase headers
- `table.srv-tbl td` — 9px 12px padding
- `table.srv-tbl tbody tr:nth-child(4n+3) td` — subtle alternating rows (#f9f2ef)
- `.srv-tbl tr.clickable` — cursor pointer, hover highlight
- `.srv-tbl tr.sel td` — selected row background
- `.detail-row td` — accordion detail container (padding 0, no border)
- `.d-inner` — flex row for 3 tiles, JetBrains Mono font
- `.d-tile`, `.d-tile-label` — tile card styling
- `.d-tile-util`, `.d-tile-io`, `.d-tile-details` — flex sizing
- `.d-ring-row`, `.d-ring-cell`, `.d-ring-lbl` — ring gauge layout
- `.ring`, `.ring-bg`, `.ring-fill`, `.ring-val`, `.ring-pct`, `.ring-unit` — SVG ring styles
- `.d-spark-row`, `.d-spark-cell`, `.d-spark-labels` — sparkline layout
- `.d-legend`, `.d-leg-col`, `.d-leg-row`, `.d-leg-k`, `.d-leg-v`, `.d-leg-head` — legend grid
- `.d-kv-row`, `.d-kv-k`, `.d-kv-v` — server details KV rows
- `.has-tip`, `.has-tip .tip` — hover tooltip for P50/P95 jargon

All CSS values should match the prototype exactly (colors, font sizes, spacing).

- [ ] **Step 2: Update badge styles to brutal**

Replace the existing badge styles (`.badge.ok`, `.badge.grace`, `.badge.alert`, `.badge.off`) with brutal versions:

```css
.badge { font-family: "JetBrains Mono", monospace; font-size: 10px; font-weight: 700;
  padding: 3px 10px; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.8px;
  border: 2px solid var(--border); box-shadow: 2px 2px 0 var(--shadow); display: inline-block; }
.badge.ok { background: var(--green); color: #fff; }
.badge.grace { background: var(--amber); color: #fff; }
.badge.alert { background: var(--red); color: #fff; }
.badge.off { background: var(--subtle); color: #fff; }
```

- [ ] **Step 3: Verify page loads without JS errors**

Open the dashboard in a browser. The old server cards should still render (JS not changed yet) but badges should show the new brutal style.

- [ ] **Step 4: Commit**

```bash
git add internal/dashboard/dashboard.html
git commit -m "feat(dashboard): add server table + accordion CSS, brutal badges"
```

---

## Task 3: Server table JS — replace card rendering with table

Replace the `render()` function's card HTML generation with table + accordion row generation.

**Files:**
- Modify: `internal/dashboard/dashboard.html` — JS `render()` function (lines ~5111-5492)

- [ ] **Step 1: Update the grid container HTML**

Change the `<div id="grid">` container (around line 1654) to hold a `<table>` instead:

```html
<div id="grid">
  <table class="srv-tbl">
    <thead><tr>
      <th></th><th>Host</th><th>Status</th><th>Mode</th><th>Duration</th>
      <th>Sessions</th><th>CPU</th><th>Mem Free</th><th>Input Dly</th><th>Last Seen</th>
    </tr></thead>
    <tbody id="srv-tbody"></tbody>
  </table>
</div>
```

- [ ] **Step 2: Rewrite the card HTML generation in render()**

In the `render()` function, replace the card building loop (lines ~5302-5475) with table row generation. For each server, generate two `<tr>` elements:

1. **Data row** (`.clickable`): status dot, hostname (mono bold), badge, mode, duration, sessions `total/max`, cpu%, mem free%, input delay, last seen
2. **Detail row** (`.detail-row`, hidden by default): colspan=10, contains the 3-tile accordion

The accordion HTML for each row should generate:
- **Tile 1 (Resource Utilization)**: 3 ring gauge SVGs (sessions/cpu/memory) + 3 sparkline canvases + 3-column legend
- **Tile 2 (I/O & Network)**: 3 ring gauge SVGs (disk q/input delay/tcp retrans) + 3 sparkline canvases + 2-column legend
- **Tile 3 (Server Details)**: KV rows for drain mode, state since, duration, grace period, grace left/exceeded (conditional), changed by, last seen, version + History/Remove buttons

Use the prototype `accordion-v7.html` as the exact reference for HTML structure.

Ring gauge SVG generation — create a helper function:

```javascript
function ringSvg(pct, color, size) {
    var r = size / 2 - 5, c = size / 2, circ = 2 * Math.PI * r;
    var offset = circ * (1 - pct / 100);
    return '<svg viewBox="0 0 '+size+' '+size+'" width="'+size+'" height="'+size+'">' +
        '<circle class="ring-bg" cx="'+c+'" cy="'+c+'" r="'+r+'" stroke-width="5"/>' +
        '<circle class="ring-fill" cx="'+c+'" cy="'+c+'" r="'+r+'" stroke="'+color+'" stroke-width="5" ' +
        'stroke-dasharray="'+circ.toFixed(1)+'" stroke-dashoffset="'+offset.toFixed(1)+'"/></svg>';
}
```

Ring color helper:

```javascript
function ringColor(pct, warn, crit) {
    if (crit > 0 && pct >= crit) return 'var(--red)';
    if (warn > 0 && pct >= warn) return 'var(--amber)';
    if (pct > 60) return 'var(--amber)';
    return 'var(--green)';
}
```

Tooltip wrapper for P50/P95:

```javascript
function tip(label, text) {
    return '<span class="has-tip">' + esc(label) + '<span class="tip">' + esc(text) + '</span></span>';
}
```

- [ ] **Step 3: Update filterGrid() for table rows**

Change `filterGrid()` to toggle `.hidden` on `<tr>` pairs (data row + detail row) instead of `.srv` card divs. Filter should match against `data-host` and `data-status` attributes on the data row.

- [ ] **Step 4: Update event delegation for History/Remove buttons**

Change the grid click handler (lines ~6407-6444) to:
- **Row click** → toggle the next `.detail-row` sibling (show/hide + `.sel` class)
- **History button** → call `showHistory(hostname)` (stop propagation so row doesn't toggle)
- **Remove button** → open delete confirmation modal (replaces the 2-stage "Sure?" pattern)

- [ ] **Step 5: Add delete confirmation modal HTML**

Add a new modal overlay for server removal confirmation, following the same pattern as the history modal but smaller:

```html
<div id="rm-overlay" class="hist-overlay" onclick="closeRmModal()">
  <div class="hist-modal" style="max-width:400px" onclick="event.stopPropagation()">
    <h3>Remove Server</h3>
    <p>Are you sure you want to remove <strong id="rm-hostname"></strong> from the dashboard?</p>
    <p style="color:var(--red)">The server will reappear automatically on its next heartbeat.</p>
    <div style="display:flex;gap:8px;justify-content:center;margin-top:16px">
      <button class="btn" onclick="closeRmModal()">Cancel</button>
      <button class="btn" style="background:var(--red);color:#fff;border-color:var(--red)" onclick="confirmRm()">Remove</button>
    </div>
  </div>
</div>
```

- [ ] **Step 6: Verify server table renders correctly**

Open dashboard, confirm:
- All servers appear as table rows with correct data
- Clicking a row expands/collapses the accordion
- Ring gauges show correct fill levels
- Legend data is correct
- Filter pills work
- History/Remove buttons work
- Offline servers show only the Details tile

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/dashboard.html
git commit -m "feat(dashboard): replace server card grid with data table + accordion"
```

---

## Task 4: Sparkline drawing

Add canvas sparkline rendering that draws area charts from history data.

**Files:**
- Modify: `internal/dashboard/dashboard.html` — JS section

- [ ] **Step 1: Add sparkline drawing function**

Add the `drawSparks()` function (from the prototype) that finds all `canvas[data-spark]` elements in a container and draws area charts:

```javascript
function drawSparks(container) {
    container.querySelectorAll('canvas[data-spark]').forEach(function(c) {
        var vals = JSON.parse(c.dataset.values || '[]');
        var max = parseFloat(c.dataset.max) || 100;
        var color = c.dataset.color || 'var(--green)';
        var dpr = window.devicePixelRatio || 1;
        var w = c.offsetWidth, h = c.offsetHeight;
        if (!w || !h) return;
        c.width = w * dpr; c.height = h * dpr;
        var ctx = c.getContext('2d');
        ctx.scale(dpr, dpr);
        var n = vals.length; if (n < 2) return;
        var step = w / (n - 1), pad = 2;
        ctx.beginPath(); ctx.moveTo(0, h);
        for (var i = 0; i < n; i++) {
            var x = i * step;
            var y = h - pad - (Math.min(vals[i], max) / max) * (h - pad * 2);
            ctx.lineTo(x, y);
        }
        ctx.lineTo(w, h); ctx.closePath();
        ctx.fillStyle = color + '22'; ctx.fill();
        ctx.beginPath();
        for (var i = 0; i < n; i++) {
            var x = i * step;
            var y = h - pad - (Math.min(vals[i], max) / max) * (h - pad * 2);
            i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
        }
        ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.stroke();
    });
}
```

- [ ] **Step 2: Fetch history for sparkline data on row expand**

When a row is expanded, fetch `/api/v1/history/{hostname}?limit=20` and extract arrays for each metric:

```javascript
function loadSparkData(hostname, detailRow) {
    fetch('/api/v1/history/' + encodeURIComponent(hostname) + '?limit=20')
        .then(function(r) { return r.json(); })
        .then(function(entries) {
            // entries are newest-first; reverse for chronological sparkline
            var rev = entries.slice().reverse();
            var sessVals = [], cpuVals = [], memVals = [];
            var dqVals = [], idVals = [], tcpVals = [];
            rev.forEach(function(e) {
                sessVals.push(e.max_sessions > 0 ? (e.total_sessions / e.max_sessions * 100) : 0);
                cpuVals.push(e.cpu_pct || 0);
                var memUsed = (e.mem_total_mb && e.mem_avail_mb) ? ((e.mem_total_mb - e.mem_avail_mb) / e.mem_total_mb * 100) : 0;
                memVals.push(memUsed);
                dqVals.push(e.disk_queue || 0);
                idVals.push(e.input_delay_max_ms || 0);
                tcpVals.push(e.tcp_retrans_sec || 0);
            });
            // Set data on canvas elements
            var canvases = detailRow.querySelectorAll('canvas[data-spark]');
            var sets = [sessVals, cpuVals, memVals, dqVals, idVals, tcpVals];
            canvases.forEach(function(c, i) {
                if (sets[i]) c.dataset.values = JSON.stringify(sets[i]);
            });
            // Compute time label from oldest entry
            if (rev.length > 1) {
                var oldest = new Date(rev[0].timestamp);
                var newest = new Date(rev[rev.length - 1].timestamp);
                var diffMin = Math.round((newest - oldest) / 60000);
                var label = diffMin >= 60 ? Math.round(diffMin / 60) + 'h ago' : diffMin + 'm ago';
                detailRow.querySelectorAll('.d-spark-labels span:first-child').forEach(function(s) {
                    s.textContent = label;
                });
            }
            drawSparks(detailRow);
        })
        .catch(function() { /* sparklines just stay empty on error */ });
}
```

- [ ] **Step 3: Call loadSparkData when toggling a row open**

In the row toggle handler, after making the detail row visible:

```javascript
if (!isOpen) {
    detailRow.style.display = 'table-row';
    row.classList.add('sel');
    loadSparkData(hostname, detailRow);
}
```

- [ ] **Step 4: Verify sparklines render with real data**

Open dashboard, expand a server row. Sparklines should draw area charts from the history data. Time labels should show the correct range.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/dashboard.html
git commit -m "feat(dashboard): sparkline area charts from history data"
```

---

## Task 5: Notification targets — table + edit modal + delete modal

Replace the inline target form rows in the config dialog with a scannable table and a separate add/edit modal.

**Files:**
- Modify: `internal/dashboard/dashboard.html` — config modal HTML, CSS, and JS

- [ ] **Step 1: Add notification target table CSS and modal CSS**

Add styles for:
- `.target-tbl` — notification target table (same styling as server table)
- `.pill-type`, `.pill-type-ntfy`, `.pill-type-email` — brutal type badges with type-specific colors
- `.status-dot`, `.status-on`, `.status-off` — enabled/disabled indicator
- `.target-edit-overlay`, `.target-edit-modal` — edit/add modal overlay
- `.target-del-overlay`, `.target-del-modal` — delete confirmation modal
- `.type-pills`, `.type-pill`, `.type-pill.active` — type selector in edit modal
- `.trigger-grid`, `.trigger-check`, `.trigger-check.checked` — trigger checkboxes
- `.repeat-pills`, `.repeat-pill`, `.repeat-pill.active` — repeat interval pills
- `.form-row`, `.form-label`, `.form-input`, `.form-hint` — form elements
- `.test-result`, `.test-result.ok`, `.test-result.err` — test notification result

Reference `targets-v1.html` prototype for exact CSS values.

- [ ] **Step 2: Replace config modal target section HTML**

Replace the `<div id="target-list">` and the "Add Target" button area with:

```html
<div class="settings-section-head">
    <h3>Notification Targets</h3>
    <button class="btn btn-accent" onclick="openTargetEdit(null)">+ Add Target</button>
</div>
<div id="target-tbl-wrap" class="tbl-wrap">
    <table class="target-tbl" id="target-tbl">
        <thead><tr>
            <th></th><th>Type</th><th>Destination</th><th>Triggers</th><th>Repeat</th><th>Actions</th>
        </tr></thead>
        <tbody id="target-tbody"></tbody>
    </table>
</div>
```

- [ ] **Step 3: Add edit modal HTML**

Add the target edit modal overlay (outside the config modal, at document level so it layers on top):

```html
<div id="target-edit-overlay" class="target-edit-overlay" onclick="closeTargetEdit(event)">
    <div class="target-edit-modal" onclick="event.stopPropagation()">
        <!-- Type pills, URL field, secret field, email fields, triggers grid, repeat pills, enabled toggle, Test/Cancel/Save buttons -->
        <!-- Use exact structure from targets-v1.html prototype -->
    </div>
</div>
```

- [ ] **Step 4: Add delete confirmation modal HTML**

```html
<div id="target-del-overlay" class="target-del-overlay" onclick="closeTargetDel(event)">
    <div class="target-del-modal" onclick="event.stopPropagation()">
        <!-- "Delete Target" title, message, type/destination, warning, Cancel/Delete buttons -->
    </div>
</div>
```

- [ ] **Step 5: Rewrite loadNotifyConfig() to populate table rows**

Replace the loop that calls `addTargetRow()` with a function that builds `<tr>` elements for each target:

```javascript
function renderTargetTable(targets) {
    var tbody = document.getElementById('target-tbody');
    tbody.innerHTML = '';
    targets.forEach(function(t, i) {
        var tr = document.createElement('tr');
        // Status dot, type pill, truncated destination, trigger pills, repeat, edit/delete buttons
        // Store target index in data-idx for edit/delete handlers
        tbody.appendChild(tr);
    });
}
```

- [ ] **Step 6: Write openTargetEdit/closeTargetEdit/saveTarget**

- `openTargetEdit(idx)` — if idx is null, blank form for "Add"; otherwise populate from `_cfgTargets[idx]`
- `closeTargetEdit()` — hide overlay
- `saveTargetFromModal()` — read form, validate, update `_cfgTargets[idx]` (or push new), re-render table, mark dirty

- [ ] **Step 7: Write openTargetDel/confirmTargetDel**

- `openTargetDel(idx)` — populate the delete modal with target type and destination, show overlay
- `confirmTargetDel()` — splice target from `_cfgTargets`, re-render table, mark dirty, close modal

- [ ] **Step 8: Write testNotification()**

- Read current form fields, POST to `/api/v1/notify-test` (existing endpoint), show result inline

- [ ] **Step 9: Wire up saveNotifyConfig() to read from the targets array**

Update `saveNotifyConfig()` to use `_cfgTargets` array directly instead of scraping DOM rows via `readTargets()`.

- [ ] **Step 10: Verify full flow**

Test: Load config → targets appear in table → Edit a target → modal opens with correct data → change trigger, save → table updates → Delete a target → confirmation modal → confirm → target removed → Save config → verify via API.

- [ ] **Step 11: Commit**

```bash
git add internal/dashboard/dashboard.html
git commit -m "feat(dashboard): notification targets table with edit/delete modals"
```

---

## Task 6: Clean up old server card and target row code

Remove the now-unused card grid CSS, old `addTargetRow()`, old `readTargets()`, and the 2-stage "Sure?" button pattern.

**Files:**
- Modify: `internal/dashboard/dashboard.html` — CSS and JS cleanup

- [ ] **Step 1: Remove old server card CSS**

Delete the `.srv.card` styles (lines ~395-481), `.srv-gauge` styles, `.srv-perf` styles, `.srv-act` styles that are no longer used by the table layout. Keep any `.srv` styles that are still referenced.

- [ ] **Step 2: Remove old target row CSS**

Delete `.target-row`, `.target-type-pill`, `.target-field`, `.btn-rm-target`, `.trigger-pill`, `.repeat-pill`, `.btn-add-target` styles.

- [ ] **Step 3: Remove old JS functions**

Delete:
- `addTargetRow()` function
- `readTargets()` function
- The 2-stage "Sure?" button handler (replaced by modal)
- `_rmTimers` global

- [ ] **Step 4: Verify nothing is broken**

Full smoke test: dashboard loads, server table works, config dialog works, history modal works, sparklines draw, no console errors.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/dashboard.html
git commit -m "refactor(dashboard): remove old card grid and inline target row code"
```

---

## Task 7: Update tests

Update any existing dashboard tests that reference the old card rendering, target rows, or server removal pattern.

**Files:**
- Modify: `internal/dashboard/dashboard_test.go` (if it tests HTML output)
- Modify: any other test files that assert on server card HTML structure

- [ ] **Step 1: Find tests referencing old patterns**

Run: `grep -r "srv card\|btn-rm\|target-row\|addTargetRow\|Sure?" internal/dashboard/ --include="*_test.go" -l`

- [ ] **Step 2: Update assertions**

Update any tests that match the old card HTML structure to match the new table row structure. If tests check for `.srv.card`, change to `<tr class="clickable"`. If tests check for `btn-rm` 2-stage confirm, update to modal-based confirm.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/dashboard/ -v`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add internal/dashboard/
git commit -m "test(dashboard): update tests for table-based server and target rendering"
```
