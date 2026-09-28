<script>
    import { authState } from '../lib/auth.svelte.js';
    import { fetchSessions } from '../lib/api.js';
    import { appState } from '../lib/state.svelte.js';
    import {
        deriveSessionFreshness,
        effectiveSessionHeartbeatMs,
        normalizeSessionsQuery,
        SESSION_PAGE_SIZES,
        sessionServerTimeOffsetMs,
    } from '../lib/sessions.js';
    import { groupServersByRdSessionCollection } from '../lib/server-groups.js';
    import CellSparkline from './CellSparkline.svelte';
    import SessionHostDetail from './SessionHostDetail.svelte';

    const COLUMNS = [
        ['host', 'Host'],
        ['status', 'Status'],
        ['mode', 'Mode'],
        ['sessions', 'Sessions'],
        ['active', 'Active'],
        ['idle', 'Idle'],
        ['disconnected', 'Disconnected'],
        ['users', 'Users'],
        ['last_activity', 'Last activity'],
    ];
    const FILTERS = [
        ['all', 'All'],
        ['active', 'Active'],
        ['disconnected', 'Disconnected'],
        ['idle', 'Idle'],
    ];

    let search = $state('');
    let appliedSearch = $state('');
    let state = $state('all');
    let sort = $state('host');
    let dir = $state('asc');
    let page = $state(1);
    let pageSize = $state(30);
    let response = $state(null);
    let serverTimeOffsetMs = $state(null);
    let currentTimeMs = $state(Date.now());
    let loading = $state(false);
    let error = $state('');
    let expandedHost = $state(null);

    let query = $derived(normalizeSessionsQuery({ q: appliedSearch, state, sort, dir, page, page_size: pageSize }));
    let heartbeatMs = $derived(effectiveSessionHeartbeatMs(appState.config, response));
    let items = $derived(
        (response?.items ?? []).map((item) => ({
            ...item,
            rd_session_collection:
                appState.servers.find((server) => server.host.toLowerCase() === item.host.toLowerCase())
                    ?.rd_session_collection ?? null,
            freshness: deriveSessionFreshness(item, { serverTimeOffsetMs, heartbeatMs, nowMs: currentTimeMs }),
        })),
    );
    let groups = $derived(groupServersByRdSessionCollection(items));
    let total = $derived(response?.total ?? 0);
    let pageInfo = $derived(response?.page ?? { number: 1, size: pageSize, pages: 0 });

    $effect(() => {
        const value = search;
        const timer = setTimeout(() => {
            appliedSearch = value;
            page = 1;
        }, 250);
        return () => clearTimeout(timer);
    });
    $effect(() => {
        const timer = setInterval(() => {
            currentTimeMs = Date.now();
        }, 1000);
        return () => clearInterval(timer);
    });
    $effect(() => {
        const revision = appState.sessionRevision;
        const isAdmin = authState.isAdmin;
        const requestQuery = query;
        if (!isAdmin) {
            response = null;
            error = '';
            loading = false;
            return;
        }
        const controller = new AbortController();
        loading = true;
        error = '';
        fetchSessions({
            q: requestQuery.q,
            state: requestQuery.state,
            sort: requestQuery.sort,
            dir: requestQuery.dir,
            page: requestQuery.page,
            pageSize: requestQuery.page_size,
            signal: controller.signal,
        })
            .then((next) => {
                if (!controller.signal.aborted) {
                    serverTimeOffsetMs = sessionServerTimeOffsetMs(next.server_now_ms);
                    response = next;
                    if (expandedHost && !next.items.some((row) => row.host === expandedHost)) expandedHost = null;
                }
            })
            .catch((reason) => {
                if (!controller.signal.aborted)
                    error =
                        reason.detail === 'sessions_disabled'
                            ? 'Fleet Sessions is disabled in Configuration.'
                            : reason.detail === 'sessions_unavailable'
                              ? 'Session data is temporarily unavailable.'
                              : 'Unable to load fleet sessions.';
            })
            .finally(() => {
                if (!controller.signal.aborted) loading = false;
            });
        return () => controller.abort();
    });

    function selectFilter(next) {
        state = next;
        page = 1;
    }
    function sortBy(column) {
        if (sort === column) dir = dir === 'asc' ? 'desc' : 'asc';
        else {
            sort = column;
            dir = 'asc';
        }
        page = 1;
    }
    function sortAria(column) {
        return sort !== column ? 'none' : dir === 'asc' ? 'ascending' : 'descending';
    }
    function sortLabel(column, label) {
        return `Sort within each RD Session Collection by ${label}, ${sort === column ? dir : 'ascending'}`;
    }
    function toggleHost(host) {
        expandedHost = expandedHost === host ? null : host;
    }
    function count(value) {
        return value == null ? '—' : value;
    }
    function formatTime(value) {
        return Number.isFinite(value)
            ? new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
            : '—';
    }
    function freshnessLabel(row) {
        return row.freshness === 'fresh' ? 'Fresh' : row.freshness === 'stale' ? 'Stale' : 'Unavailable';
    }
    function statusClass(status) {
        return ['ok', 'warning', 'grace', 'alert', 'off'].includes(status) ? status : 'off';
    }
</script>

<section class="grid sessions-grid" aria-label="Sessions">
    <div class="section-label">SESSIONS</div>
    <div class="filter-bar">
        <input
            id="session-search"
            class="settings-input session-search"
            type="search"
            bind:value={search}
            placeholder="Host or session ID"
            maxlength="128"
            autocomplete="off"
            aria-label="Search hosts or session ID"
        />
        <div class="filter-pills" role="group" aria-label="Session state filter">
            {#each FILTERS as [value, label]}
                <button
                    class:active={state === value}
                    class="filter-pill"
                    aria-pressed={state === value}
                    onclick={() => selectFilter(value)}>{label}</button
                >
            {/each}
        </div>
        <p class="session-result" aria-live="polite">
            {loading ? 'Loading sessions…' : `${total} ${total === 1 ? 'host' : 'hosts'}`}
        </p>
    </div>

    {#if error}
        <div class="grid-toast" role="alert">{error}</div>
    {:else if !loading && items.length === 0}
        <div class="card empty">
            <h2>{total === 0 ? 'No matching session hosts' : 'No hosts on this page'}</h2>
            <p>{total === 0 ? 'Change the search or filter, then try again.' : 'Return to an earlier page.'}</p>
        </div>
    {:else}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex: this named overflow region must accept keyboard scrolling -->
        <div
            class="card session-table-wrap scrollbar-styled"
            role="region"
            tabindex="0"
            aria-label="Scrollable session host table"
            aria-describedby="session-table-scroll-hint"
            aria-busy={loading}
        >
            <p id="session-table-scroll-hint" class="sr-only">Scroll horizontally to view all session host columns.</p>
            <table class="session-table">
                <colgroup>
                    <col class="col-expand" />
                    <col class="col-host" />
                    <col class="col-status" />
                    <col class="col-mode" />
                    <col class="col-total" />
                    <col class="col-count" />
                    <col class="col-count" />
                    <col class="col-disconnected" />
                    <col class="col-count" />
                    <col class="col-activity" />
                </colgroup>
                <thead
                    ><tr>
                        <th class="expand-col" scope="col"><span class="sr-only">Expand details</span></th>
                        {#each COLUMNS as [column, label]}
                            <th scope="col" aria-sort={sortAria(column)}
                                ><button
                                    class="sort-button"
                                    onclick={() => sortBy(column)}
                                    aria-label={sortLabel(column, label)}
                                    >{label}{sort === column ? (dir === 'asc' ? ' ↑' : ' ↓') : ''}</button
                                ></th
                            >
                        {/each}
                    </tr></thead
                >
                <tbody>
                    {#each groups as group (group.collection)}
                        <tr class="collection-group-header"
                            ><th colspan="10" scope="rowgroup"
                                ><span>RD SESSION COLLECTION</span><strong>{group.collection}</strong><span
                                    class="collection-group-count"
                                    >{group.servers.length} host{group.servers.length === 1 ? '' : 's'}</span
                                ></th
                            ></tr
                        >
                        {#each group.servers as row (row.host)}
                            {@const expanded = expandedHost === row.host}
                            {@const status = statusClass(row.status)}
                            {@const history = appState.serverMetrics.get(row.host) ?? []}
                            <tr class:sel={expanded} class="session-row">
                                <td
                                    ><button
                                        class="expand-button"
                                        aria-expanded={expanded}
                                        aria-controls={`session-detail-${row.host}`}
                                        aria-label={`${expanded ? 'Collapse' : 'Expand'} sessions for ${row.host}`}
                                        onclick={() => toggleHost(row.host)}>{expanded ? '−' : '+'}</button
                                    ></td
                                >
                                <td class="host-cell"
                                    ><strong class="mono">{row.host}</strong><span
                                        class="host-meta freshness-{row.freshness}">{freshnessLabel(row)}</span
                                    >{#if !row.detail_available}<span class="host-meta">Detail unavailable</span
                                        >{/if}{#if row.collection_status && row.collection_status !== 'ok'}<span
                                            class="host-meta error-meta"
                                            >{row.collection_error_code ?? row.collection_status}</span
                                        >{/if}</td
                                >
                                <td
                                    ><span class={`dot ${status}`}></span><span class={`pill ${status}`}
                                        >{row.status ?? 'unknown'}</span
                                    ></td
                                >
                                <td class="mono">{row.mode ?? '—'}</td>
                                <td class="mono spark-cell"
                                    ><CellSparkline
                                        data={history.map((sample) => sample.sessions)}
                                        color="var(--color-accent)"
                                    />{count(row.session_count)}</td
                                >
                                <td class="mono">{count(row.active_count)}</td><td class="mono"
                                    >{count(row.idle_count)}</td
                                ><td class="mono">{count(row.disconnected_count)}</td><td class="mono"
                                    >{count(row.user_count)}</td
                                ><td class="mono muted">{formatTime(row.last_activity_at_ms)}</td>
                            </tr>
                            {#if expanded}
                                <tr class="detail-row"
                                    ><td colspan="10"
                                        ><div id={`session-detail-${row.host}`}>
                                            <SessionHostDetail
                                                host={row.host}
                                                summary={row}
                                                {serverTimeOffsetMs}
                                                revision={appState.sessionRevision}
                                            />
                                        </div></td
                                    ></tr
                                >
                            {/if}
                        {/each}
                    {/each}
                </tbody>
            </table>
        </div>
    {/if}

    {#if !error && (pageInfo.pages > 1 || total > 0)}
        <nav class="pager" aria-label="Session hosts pagination">
            <button class="pager-btn btn-brutal" disabled={pageInfo.number <= 1} onclick={() => page--}>Previous</button
            >
            <span class="pager-info">Page {pageInfo.number} of {pageInfo.pages || 1}</span>
            <button
                class="pager-btn btn-brutal"
                disabled={pageInfo.pages === 0 || pageInfo.number >= pageInfo.pages}
                onclick={() => page++}>Next</button
            >
            <span class="page-size-label">Rows:</span>
            {#each SESSION_PAGE_SIZES as size}<button
                    class:active={pageSize === size}
                    class="btn-brutal pager-pill"
                    aria-pressed={pageSize === size}
                    onclick={() => {
                        pageSize = size;
                        page = 1;
                    }}>{size}</button
                >{/each}
        </nav>
    {/if}
</section>

<style>
    .sessions-grid {
        margin-bottom: 24px;
        min-width: 0;
        max-inline-size: 100%;
    }
    .filter-bar {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 12px;
        flex-wrap: wrap;
    }
    .session-search {
        max-width: 320px;
    }
    .filter-pills {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
    }
    .filter-pill {
        font:
            700 0.68rem 'JetBrains Mono',
            monospace;
        padding: 4px 10px;
        border-radius: 20px;
        border: var(--spacing-bw) solid var(--color-border);
        background: var(--color-card);
        color: var(--color-muted);
        cursor: pointer;
        text-transform: uppercase;
        letter-spacing: 0.06em;
    }
    .filter-pill.active {
        background: var(--color-fg);
        color: var(--color-bg);
    }
    .session-result {
        margin-left: auto;
        color: var(--color-muted);
        font:
            700 0.72rem 'JetBrains Mono',
            monospace;
        white-space: nowrap;
    }
    .grid-toast {
        padding: 8px 14px;
        margin-bottom: 10px;
        background: var(--color-card);
        color: var(--color-red);
        border: 1.5px solid var(--color-red);
        border-radius: var(--radius-default);
        font:
            0.8rem 'JetBrains Mono',
            monospace;
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
    .session-table-wrap {
        min-width: 0;
        max-inline-size: 100%;
        overflow-x: auto;
        overflow-y: hidden;
    }
    .session-table-wrap:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
    .session-table {
        width: 100%;
        min-width: 1100px;
        table-layout: fixed;
        border-collapse: separate;
        border-spacing: 0;
        font-size: 13px;
    }
    .col-expand {
        width: 36px;
    }
    .col-host {
        width: 35%;
    }
    .col-status {
        width: 92px;
    }
    .col-mode {
        width: 72px;
    }
    .col-total {
        width: 82px;
    }
    .col-count {
        width: 58px;
    }
    .col-disconnected {
        width: 104px;
    }
    .col-activity {
        width: 142px;
    }
    .session-table th {
        font:
            500 11px 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        color: var(--color-muted);
        text-align: left;
        padding: 8px;
        border-bottom: var(--spacing-bw) solid var(--color-border);
        white-space: nowrap;
        background: var(--color-card);
    }
    .session-table td {
        padding: 9px 8px;
        border-bottom: 1px solid var(--color-border);
        vertical-align: middle;
        white-space: nowrap;
        color: var(--color-fg);
    }
    .session-table th:nth-child(n + 5):nth-child(-n + 9),
    .session-table td:nth-child(n + 5):nth-child(-n + 9) {
        text-align: center;
    }
    .collection-group-header th {
        padding: 7px 8px;
        background: var(--color-surface);
        border-bottom: 1px solid var(--color-border);
    }
    .collection-group-header strong {
        margin-left: 10px;
        color: var(--color-fg);
        font-weight: 700;
    }
    .collection-group-count {
        margin-left: 8px;
        font-weight: 400;
        opacity: 0.75;
    }
    .sort-button {
        padding: 0;
        border: 0;
        background: none;
        color: inherit;
        cursor: pointer;
        font: inherit;
        text-transform: inherit;
    }
    .sort-button:hover {
        color: var(--color-accent);
    }
    .expand-col {
        width: 36px;
    }
    .expand-button {
        min-width: 24px;
        min-height: 24px;
        padding: 0;
        border: 0;
        background: transparent;
        color: var(--color-accent);
        cursor: pointer;
        font:
            700 1rem 'JetBrains Mono',
            monospace;
    }
    .expand-button:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
    .session-row:hover td {
        background: var(--color-surface);
    }
    .sel td {
        background: color-mix(in srgb, var(--color-accent) 8%, var(--color-card)) !important;
    }
    .host-cell {
        min-width: 180px;
    }
    .host-meta {
        display: inline-block;
        margin: 3px 0 0 6px;
        color: var(--color-muted);
        font:
            700 0.6rem 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
    }
    .freshness-fresh {
        color: var(--color-green);
    }
    .freshness-stale {
        color: var(--color-amber);
    }
    .freshness-unavailable,
    .error-meta {
        color: var(--color-red);
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
    .dot.warning,
    .dot.grace {
        background: var(--color-amber);
    }
    .dot.alert {
        background: var(--color-red);
    }
    .dot.off {
        background: var(--color-subtle);
    }
    .pill {
        font:
            700 0.6rem 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        padding: 2px 8px;
        border-radius: 4px;
        color: #fff;
    }
    .pill.ok {
        background: var(--color-green);
    }
    .pill.warning,
    .pill.grace {
        background: var(--color-amber);
    }
    .pill.alert {
        background: var(--color-red);
    }
    .pill.off {
        background: var(--color-subtle);
    }
    .spark-cell {
        position: relative;
        overflow: hidden;
        border-left: 1px solid color-mix(in srgb, var(--color-border) 40%, transparent);
    }
    .muted {
        color: var(--color-muted) !important;
    }
    .detail-row td {
        padding: 0 !important;
        white-space: normal;
        border-bottom: var(--spacing-bw) solid var(--color-surface) !important;
    }
    .pager {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 14px 0 4px;
        flex-wrap: wrap;
    }
    .pager-btn {
        font-size: 0.7rem;
        padding: 5px 14px;
        background: var(--color-card);
    }
    .pager-btn:disabled {
        opacity: 0.35;
        pointer-events: none;
    }
    .pager-info,
    .page-size-label {
        font:
            700 0.7rem 'JetBrains Mono',
            monospace;
        color: var(--color-muted);
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
    @media (max-width: 700px) {
        .session-search {
            width: 100%;
            max-width: none;
        }
        .session-result {
            margin-left: 0;
        }
        .session-table-wrap {
            width: 100%;
        }
    }
</style>
