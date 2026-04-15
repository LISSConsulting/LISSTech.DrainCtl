<script>
    import { untrack } from 'svelte';
    import { appState, removeServerMetrics } from '../lib/state.svelte.js';
    import { deleteServer } from '../lib/api.js';
    import { getThresholdColor, resolveThresholds } from '../lib/thresholds.js';
    import { rel, modeLabel } from '../lib/utils.js';
    import ServerDetail from './ServerDetail.svelte';
    import CellSparkline from './CellSparkline.svelte';
    import ConfirmDialog from './ConfirmDialog.svelte';

    let { onhistoryclick } = $props();

    /** @type {string|null} */
    let confirmRemoveHost = $state(null);

    /** @type {Set<string>} */
    let expandedHosts = $state(new Set());
    let sortCol = $state(localStorage.getItem('drainctl-sort-col') || 'host');
    let sortDir = $state(parseInt(localStorage.getItem('drainctl-sort-dir') ?? '1', 10) || 1);
    let search = $state(localStorage.getItem('drainctl-search') || '');
    let page = $state(0);
    const PAGE_SIZES = [15, 30, 50];
    let pageSize = $state(parseInt(localStorage.getItem('drainctl-page-size') ?? '15', 10) || 15);
    let removeError = $state('');
    /** @type {ReturnType<typeof setTimeout>|null} */
    let removeErrorTimer = null;
    // Clear the error-dismiss timer on component destroy so it never fires on
    // an unmounted instance (e.g. navigating away while a remove was in flight).
    $effect(() => () => { clearTimeout(removeErrorTimer); });
    /** @type {Set<string>} */
    let removingHosts = $state(new Set());

    // Prune expandedHosts when servers are removed via SSE (external deletion).
    // When the local UI deletes a server via doRemoveServer(), expandedHosts is
    // cleaned up there. But an SSE server_deleted event from another browser only
    // removes the host from appState.servers — expandedHosts retains the stale
    // entry. This effect tracks appState.servers reactively and removes any host
    // from expandedHosts that is no longer in the server list, so that if the
    // same hostname re-registers later it does not appear pre-expanded.
    $effect(() => {
        const liveHosts = new Set(appState.servers.map((s) => s.host));
        const current = untrack(() => expandedHosts);
        const stale = [...current].filter((h) => !liveHosts.has(h));
        if (stale.length > 0) {
            const next = new Set(current);
            for (const h of stale) next.delete(h);
            expandedHosts = next;
        }
    });

    // Reactive clock — ticks every 10 s so that relative timestamps and the
    // grace-period countdown badge stay fresh between 30-second server refreshes.
    let now = $state(Date.now());
    $effect(() => {
        const t = setInterval(() => {
            now = Date.now();
        }, 10_000);
        return () => clearInterval(t);
    });

    const STATUS_ORDER = { alert: 0, warning: 1, grace: 2, off: 3, ok: 4 };

    /**
     * Returns a human-readable countdown string for a grace deadline ISO timestamp.
     * Takes `now` explicitly so the template tracks it as a reactive dependency.
     * @param {string|null|undefined} iso
     * @param {number} _now - current epoch ms (reactive)
     * @returns {string|null}
     */
    function graceCountdown(iso, _now) {
        if (!iso) return null;
        const d = new Date(iso);
        if (isNaN(d)) return null;
        const ms = d - _now;
        const s = Math.floor(ms / 1000);
        if (s <= 0) return 'expired';
        if (s < 60) return s + 's left';
        const m = Math.floor(s / 60);
        if (m < 60) return m + 'm left';
        const h = Math.floor(m / 60);
        if (h < 24) {
            const rem = m % 60;
            return h + 'h ' + (rem > 0 ? rem + 'm ' : '') + 'left';
        }
        const days = Math.floor(h / 24);
        const remH = h % 24;
        return days + 'd ' + (remH > 0 ? remH + 'h ' : '') + 'left';
    }

    function statusLabel(s) {
        return { ok: 'Healthy', warning: 'Warning', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] || s;
    }

    // Counts per status for the filter pill labels ("Grace (2)").
    let statusCounts = $derived.by(() => {
        const counts = { ok: 0, warning: 0, grace: 0, alert: 0, off: 0 };
        for (const sv of appState.servers) {
            if (sv.status in counts) counts[sv.status]++;
        }
        return counts;
    });

    let sorted = $derived.by(() => {
        let s = appState.servers.filter((sv) => {
            const matchText = !search || sv.host.toLowerCase().includes(search.toLowerCase());
            const matchStatus = appState.serverFilter === 'all' || sv.status === appState.serverFilter;
            return matchText && matchStatus;
        });
        return s.sort((a, b) => {
            let va, vb;
            if (sortCol === 'status') {
                va = STATUS_ORDER[a.status] ?? 4;
                vb = STATUS_ORDER[b.status] ?? 4;
            } else if (sortCol === 'host') {
                va = a.host;
                vb = b.host;
            } else if (sortCol === 'sessions') {
                va = a.sessions || 0;
                vb = b.sessions || 0;
            } else if (sortCol === 'cpu') {
                va = a.perf?.cpu_pct || 0;
                vb = b.perf?.cpu_pct || 0;
            } else if (sortCol === 'mem') {
                va = a.perf?.mem_total_mb > 0 ? (1 - a.perf.mem_avail_mb / a.perf.mem_total_mb) * 100 : 0;
                vb = b.perf?.mem_total_mb > 0 ? (1 - b.perf.mem_avail_mb / b.perf.mem_total_mb) * 100 : 0;
            } else if (sortCol === 'delay') {
                va = a.perf?.input_delay_p95_ms || 0;
                vb = b.perf?.input_delay_p95_ms || 0;
            } else if (sortCol === 'last_seen') {
                va = new Date(a.last_seen || 0);
                vb = new Date(b.last_seen || 0);
            } else {
                va = a[sortCol];
                vb = b[sortCol];
            }
            if (va < vb) return -sortDir;
            if (va > vb) return sortDir;
            return 0;
        });
    });

    function sort(col) {
        if (sortCol === col) sortDir = -sortDir;
        else {
            sortCol = col;
            sortDir = 1;
        }
    }

    function toggleRow(host) {
        const next = new Set(expandedHosts);
        if (next.has(host)) next.delete(host);
        else next.add(host);
        expandedHosts = next;
    }

    function requestRemoveServer(host) {
        confirmRemoveHost = host;
    }

    async function doRemoveServer() {
        const host = confirmRemoveHost;
        confirmRemoveHost = null;
        if (!host || removingHosts.has(host)) return;
        removingHosts = new Set([...removingHosts, host]);
        try {
            await deleteServer(host);
            appState.servers = appState.servers.filter((s) => s.host !== host);
            removeServerMetrics(host);
            if (expandedHosts.has(host)) {
                const next = new Set(expandedHosts);
                next.delete(host);
                expandedHosts = next;
            }
        } catch (e) {
            removeError = 'Remove failed: ' + (e?.message ?? String(e));
            clearTimeout(removeErrorTimer);
            removeErrorTimer = setTimeout(() => (removeError = ''), 5000);
        } finally {
            const next = new Set(removingHosts);
            next.delete(host);
            removingHosts = next;
        }
    }

    // Per-metric thresholds derived from config (same logic as ServerDetail).
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));
    let delayThresh = $derived(resolveThresholds('inputDelay', perfCfg));

    // Session warning threshold — raw session count from alert-sensitivity config.
    // Used for both the sparkline color and (potentially) future session-cell coloring.
    let sessionWarnThresh = $derived(appState.config?.session_warning_threshold ?? 80);

    /**
     * Map a getThresholdColor result to a CSS color variable string.
     * Returns empty string when there is no data ('neutral').
     * @param {'green'|'amber'|'red'|'neutral'} color
     * @returns {string}
     */
    function thresholdStyle(color) {
        if (color === 'green') return 'color:var(--color-green)';
        if (color === 'amber') return 'color:var(--color-amber)';
        if (color === 'red') return 'color:var(--color-red)';
        return '';
    }

    /**
     * Map a getThresholdColor token to the matching CSS color variable.
     * Used to tint sparklines — returns the muted variable for 'neutral'
     * (no data), though the sparkline won't render at all when history is empty.
     * @param {'green'|'amber'|'red'|'neutral'} color
     * @returns {string}
     */
    function sparkColor(color) {
        if (color === 'green') return 'var(--color-green)';
        if (color === 'amber') return 'var(--color-amber)';
        if (color === 'red') return 'var(--color-red)';
        return 'var(--color-muted)';
    }

    // Persist search
    $effect(() => {
        localStorage.setItem('drainctl-search', search);
    });
    $effect(() => {
        localStorage.setItem('drainctl-sort-col', sortCol);
    });
    $effect(() => {
        localStorage.setItem('drainctl-sort-dir', String(sortDir));
    });

    // Reset page when filters/search change.
    $effect(() => {
        search;
        appState.serverFilter;
        page = 0;
    });

    // Persist page size preference.
    $effect(() => {
        localStorage.setItem('drainctl-page-size', String(pageSize));
    });

    let totalPages = $derived(Math.max(1, Math.ceil(sorted.length / pageSize)));
    let paged = $derived(sorted.slice(page * pageSize, (page + 1) * pageSize));
</script>

<div class="grid">
    <div class="section-label">SERVERS</div>

    <!-- Filter bar -->
    <div class="filter-bar">
        <input
            class="srv-search settings-input"
            type="search"
            placeholder="Filter by hostname..."
            aria-label="Filter servers by hostname"
            bind:value={search}
            style="max-width:300px"
        />
        <div class="filter-pills" role="group" aria-label="Filter servers by status">
            {#each ['all', 'ok', 'warning', 'grace', 'alert', 'off'] as f}
                {@const count = f === 'all' ? appState.servers.length : statusCounts[f]}
                <button
                    class="filter-pill {f === 'all' ? '' : f} {appState.serverFilter === f ? 'active' : ''}"
                    aria-pressed={appState.serverFilter === f}
                    onclick={() => (appState.serverFilter = f)}
                >
                    {f === 'all' ? 'All' : statusLabel(f)}{count ? ' (' + count + ')' : ''}
                </button>
            {/each}
        </div>
    </div>

    {#if removeError}
        <div class="grid-toast">{removeError}</div>
    {/if}

    {#if !appState.servers.length}
        <div class="empty">
            <h2 class="serif">No servers registered</h2>
            <p>Waiting for agents to connect...</p>
        </div>
    {:else if !sorted.length}
        <div class="empty"><p>No servers match the current filter.</p></div>
    {:else}
        <div class="card">
            <table class="srv-tbl">
                <thead>
                    <tr>
                        <th></th>
                        <th
                            onclick={() => sort('host')}
                            class="sortable"
                            aria-sort={sortCol === 'host' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Host {sortCol === 'host' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('status')}
                            class="sortable"
                            aria-sort={sortCol === 'status' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Status {sortCol === 'status' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th>Mode</th>
                        <th>Since</th>
                        <th
                            onclick={() => sort('sessions')}
                            class="sortable"
                            aria-sort={sortCol === 'sessions' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Sessions {sortCol === 'sessions' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('cpu')}
                            class="sortable"
                            aria-sort={sortCol === 'cpu' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >CPU {sortCol === 'cpu' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('mem')}
                            class="sortable"
                            aria-sort={sortCol === 'mem' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >MEM {sortCol === 'mem' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('delay')}
                            class="sortable"
                            aria-sort={sortCol === 'delay' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Input Delay {sortCol === 'delay' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th
                            onclick={() => sort('last_seen')}
                            class="sortable"
                            aria-sort={sortCol === 'last_seen' ? (sortDir === 1 ? 'ascending' : 'descending') : 'none'}
                            >Last Seen {sortCol === 'last_seen' ? (sortDir === 1 ? '↑' : '↓') : ''}</th
                        >
                        <th></th>
                    </tr>
                </thead>
                <tbody>
                    {#each paged as srv (srv.host)}
                        {@const memPct =
                            srv.perf?.mem_total_mb > 0
                                ? (1 - srv.perf.mem_avail_mb / srv.perf.mem_total_mb) * 100
                                : null}
                        {@const cpuColor = srv.perf
                            ? getThresholdColor(srv.perf.cpu_pct, cpuThresh.warn, cpuThresh.crit)
                            : 'neutral'}
                        {@const memColor =
                            memPct != null ? getThresholdColor(memPct, memThresh.warn, memThresh.crit) : 'neutral'}
                        {@const delayColor = srv.perf
                            ? getThresholdColor(srv.perf.input_delay_p95_ms, delayThresh.warn, delayThresh.crit)
                            : 'neutral'}
                        {@const sessPct = srv.max_sessions > 0 ? ((srv.sessions ?? 0) / srv.max_sessions) * 100 : null}
                        {@const sessColor = getThresholdColor(sessPct, sessionWarnThresh, 100)}
                        {@const cpuStyle = thresholdStyle(cpuColor)}
                        {@const memStyle = thresholdStyle(memColor)}
                        {@const delayStyle = thresholdStyle(delayColor)}
                        {@const srvHistory = appState.serverMetrics.get(srv.host)}
                        <tr
                            class="clickable {expandedHosts.has(srv.host) ? 'sel' : ''}"
                            data-host={srv.host}
                            data-status={srv.status}
                            tabindex="0"
                            aria-expanded={expandedHosts.has(srv.host)}
                            onclick={() => toggleRow(srv.host)}
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    toggleRow(srv.host);
                                }
                            }}
                        >
                            <td><span class="dot {srv.status}"></span></td>
                            <td class="mono fw7">{srv.host.split('.')[0]}</td>
                            <td>
                                <span class="pill {srv.status}">{statusLabel(srv.status)}</span>
                                {#if srv.status === 'grace'}
                                    {@const cd = graceCountdown(srv.grace_deadline, now)}
                                    {#if cd}
                                        <span class="grace-cd {cd === 'expired' ? 'grace-cd--expired' : ''}">{cd}</span>
                                    {/if}
                                {/if}
                            </td>
                            <td class="mono">{modeLabel(srv.drain_mode)}</td>
                            <td class="mono muted">{rel(srv.state_changed_at ?? srv.registered_at, now)}</td>
                            <td class="mono spark-cell">
                                <CellSparkline
                                    data={srvHistory?.map((s) => s.sessions) ?? []}
                                    color={sparkColor(sessColor)}
                                />
                                {srv.sessions ?? '—'}
                            </td>
                            <td class="mono spark-cell" style={cpuStyle}>
                                <CellSparkline
                                    data={srvHistory?.map((s) => s.cpu) ?? []}
                                    color={sparkColor(cpuColor)}
                                />
                                {srv.perf?.cpu_pct != null ? srv.perf.cpu_pct.toFixed(1) + '%' : '—'}
                            </td>
                            <td class="mono spark-cell" style={memStyle}>
                                <CellSparkline
                                    data={srvHistory?.map((s) => s.mem) ?? []}
                                    color={sparkColor(memColor)}
                                />
                                {memPct != null ? memPct.toFixed(0) + '%' : '—'}
                            </td>
                            <td class="mono spark-cell" style={delayStyle}>
                                <CellSparkline
                                    data={srvHistory?.map((s) => s.inputDelay) ?? []}
                                    color={sparkColor(delayColor)}
                                />
                                {srv.perf?.input_delay_p95_ms != null ? srv.perf.input_delay_p95_ms.toFixed(1) + 'ms' : '—'}
                            </td>
                            <td class="mono muted">{rel(srv.last_seen, now)}</td>
                            <td onclick={(e) => e.stopPropagation()}>
                                <div class="btn-row">
                                    <button
                                        class="btn-hist"
                                        onclick={() => {
                                            appState.eventHostFilter = srv.host.split('.')[0];
                                            appState.currentView = 'events';
                                        }}>History</button
                                    >
                                    <button
                                        class="btn-rm"
                                        onclick={() => requestRemoveServer(srv.host)}
                                        aria-label="Remove {srv.host}"
                                        disabled={removingHosts.has(srv.host)}
                                        >{removingHosts.has(srv.host) ? '…' : '✕'}</button
                                    >
                                </div>
                            </td>
                        </tr>
                        {#if expandedHosts.has(srv.host)}
                            <tr class="detail-row">
                                <td colspan="11">
                                    <ServerDetail server={srv} {onhistoryclick} onremove={requestRemoveServer} />
                                </td>
                            </tr>
                        {/if}
                    {/each}
                </tbody>
            </table>
        </div>

        <div class="pager">
            {#if totalPages > 1}
                <button
                    class="pager-btn btn-brutal"
                    disabled={page === 0}
                    onclick={() => page--}>← Prev</button
                >
                <span class="pager-info"
                    >{page + 1} / {totalPages} <span class="pager-total">({sorted.length} servers)</span></span
                >
                <button
                    class="pager-btn btn-brutal"
                    disabled={page >= totalPages - 1}
                    onclick={() => page++}>Next →</button
                >
            {/if}
            {#each PAGE_SIZES as sz}
                <button
                    class="btn-brutal pager-pill"
                    class:active={pageSize === sz}
                    onclick={() => { pageSize = sz; page = 0; }}
                >{sz}</button>
            {/each}
        </div>
    {/if}
</div>

{#if confirmRemoveHost}
    <ConfirmDialog
        title="Remove Server"
        message="Remove {confirmRemoveHost} from the dashboard? This cannot be undone."
        confirmLabel="Remove"
        cancelLabel="Cancel"
        onconfirm={doRemoveServer}
        oncancel={() => (confirmRemoveHost = null)}
    />
{/if}

<style>
    .grid {
        margin-bottom: 24px;
    }
    .grid > .card {
        overflow: hidden;
    }
    .filter-bar {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 12px;
        flex-wrap: wrap;
    }
    .filter-pills {
        display: flex;
        gap: 6px;
    }
    .filter-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        font-weight: 700;
        padding: 4px 10px;
        border-radius: 20px;
        border: var(--spacing-bw) solid var(--color-border);
        background: var(--color-card);
        color: var(--color-muted);
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        transition: all 0.1s linear;
    }
    .filter-pill.active {
        background: var(--color-fg);
        color: var(--color-bg);
    }
    .filter-pill.ok.active {
        background: var(--color-green);
        color: #fff;
        border-color: var(--color-green);
    }
    .filter-pill.warning.active {
        background: var(--color-amber);
        color: #fff;
        border-color: var(--color-amber);
    }
    .filter-pill.grace.active {
        background: var(--color-amber);
        color: #fff;
        border-color: var(--color-amber);
    }
    .filter-pill.alert.active {
        background: var(--color-red);
        color: #fff;
        border-color: var(--color-red);
    }
    .filter-pill.off.active {
        background: var(--color-subtle);
        color: #fff;
        border-color: var(--color-subtle);
    }
    .grid-toast {
        padding: 8px 14px;
        margin-bottom: 10px;
        background: var(--color-card);
        color: var(--color-red);
        border: 1.5px solid var(--color-red);
        border-radius: var(--radius-default);
        font-size: 0.8rem;
        font-family: 'JetBrains Mono', monospace;
    }
    .pager {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 14px 0 4px;
        position: relative;
        z-index: 2;
    }
    .pager-btn {
        font-size: 0.7rem;
        padding: 5px 14px;
        background: var(--color-card);
        color: var(--color-fg);
    }
    .pager-btn:disabled {
        opacity: 0.35;
        pointer-events: none;
    }
    .pager-info {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.7rem;
        font-weight: 700;
        color: var(--color-muted);
    }
    .pager-total {
        font-weight: 400;
        opacity: 0.6;
    }
    .pager-pill {
        font-size: 0.65rem;
        font-weight: 700;
        padding: 4px 10px;
        color: var(--color-muted);
    }
    .pager-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .empty {
        text-align: center;
        padding: 50px 20px;
        color: var(--color-muted);
    }
    .empty h2 {
        font-size: 1.3rem;
        font-weight: 400;
        margin-bottom: 4px;
        color: var(--color-fg);
    }
    table.srv-tbl {
        width: 100%;
        border-collapse: separate;
        border-spacing: 0;
        font-size: 13px;
    }
    table.srv-tbl th {
        font-family: 'JetBrains Mono', monospace;
        font-size: 11px;
        font-weight: 500;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-muted);
        text-align: left;
        padding: 8px 12px;
        border-bottom: var(--spacing-bw) solid var(--color-border);
        white-space: nowrap;
        background: var(--color-card);
    }
    table.srv-tbl td {
        padding: 9px 12px;
        border-bottom: 1px solid var(--color-border);
        vertical-align: middle;
        color: var(--color-fg);
    }
    th.sortable {
        cursor: pointer;
        user-select: none;
    }
    th.sortable:hover {
        color: var(--color-accent);
    }
    .clickable {
        cursor: pointer;
    }
    .clickable:hover td {
        background: var(--color-surface);
    }
    .clickable:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }
    .clickable:focus-visible td {
        background: var(--color-surface);
    }
    .sel td {
        background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card)) !important;
    }
    .dot {
        display: inline-block;
        width: 8px;
        height: 8px;
        border-radius: 50%;
        margin-right: 6px;
        vertical-align: middle;
    }
    .dot.ok {
        background: var(--color-green);
    }
    .dot.warning {
        background: var(--color-amber);
    }
    .dot.grace {
        background: var(--color-amber);
    }
    .dot.alert {
        background: var(--color-red);
    }
    .dot.off {
        background: var(--color-subtle);
    }
    .mono {
        font-family: 'JetBrains Mono', monospace;
    }
    /* Cells that carry a sparkline background — SVG is position:absolute inside */
    .spark-cell {
        position: relative;
        overflow: hidden;
    }
    .muted {
        color: var(--color-muted);
    }
    .fw7 {
        font-weight: 700;
    }
    .btn-row {
        display: flex;
        gap: 8px;
    }
    .btn-hist {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        font-weight: 700;
        padding: 4px 10px;
        background: var(--color-card);
        color: var(--color-accent);
        border: var(--spacing-bw) solid var(--color-accent);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .btn-hist:hover {
        transform: translate(-1px, -1px);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-accent);
        color: #fff;
    }
    .btn-hist:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .btn-rm {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.7rem;
        font-weight: 700;
        padding: 4px 10px;
        background: var(--color-card);
        color: var(--color-red);
        border: var(--spacing-bw) solid var(--color-red);
        border-radius: var(--radius-default);
        box-shadow: 3px 3px 0 var(--color-shadow);
        cursor: pointer;
        transition:
            transform 0.1s,
            box-shadow 0.1s,
            background 0.1s,
            color 0.1s;
    }
    .btn-rm:hover {
        transform: translate(-1px, -1px);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-red);
        color: #fff;
    }
    .btn-rm:active {
        transform: translate(1px, 1px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .detail-row td {
        padding: 0 !important;
        border-bottom: var(--spacing-bw) solid var(--color-surface) !important;
    }
    .pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        padding: 2px 8px;
        border-radius: 4px;
        color: #fff;
    }
    .pill.ok {
        background: var(--color-green);
    }
    .pill.warning {
        background: var(--color-amber);
    }
    .pill.grace {
        background: var(--color-amber);
    }
    .pill.alert {
        background: var(--color-red);
    }
    .pill.off {
        background: var(--color-subtle);
    }
    .section-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.65rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.12em;
        color: var(--color-muted);
        margin-bottom: 10px;
    }
    .grace-cd {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        color: var(--color-amber);
        margin-left: 6px;
        white-space: nowrap;
    }
    .grace-cd--expired {
        color: var(--color-red);
    }
</style>
