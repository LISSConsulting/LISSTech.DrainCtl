<script>
    import { onMount, tick } from 'svelte';
    import { Copy, Ellipsis, LogOut, MessageSquareText, RefreshCw, ScreenShare, Unplug } from '@lucide/svelte';
    import SessionActionDialog from './SessionActionDialog.svelte';
    import RingGauge from './RingGauge.svelte';
    import Sparkline from './Sparkline.svelte';
    import { fetchSessionDetail, fetchSessionShadow } from '../lib/api.js';
    import { appState } from '../lib/state.svelte.js';
    import { toast } from '../lib/toast.svelte.js';
    import {
        copyText,
        deriveSessionFreshness,
        effectiveSessionHeartbeatMs,
        launchShadowProtocol,
    } from '../lib/sessions.js';
    import {
        formatDecimalBytes,
        isSafeShadowTarget,
        resumeSessionActionPolling,
    } from '../lib/session-actions.svelte.js';

    let { host, summary = {}, serverTimeOffsetMs = null, revision = 0 } = $props();
    let detail = $state(null);
    let loading = $state(true);
    let error = $state('');
    let search = $state('');
    let submittedSearch = $state('');
    let page = $state(1);
    let pageSize = $state(30);
    let dialog = $state(null);
    let requestController;
    let menu = $state(null);
    let menuElement = $state();
    let menuPosition = $state({ left: 0, top: 0 });
    let menuId = $derived(`session-actions-menu-${host}`);
    let currentTimeMs = $state(Date.now());
    let wasFresh = false;
    const stateRank = {
        active: 0,
        connected: 1,
        connect_query: 2,
        shadow: 3,
        disconnected: 4,
        idle: 5,
        listen: 6,
        reset: 7,
        down: 8,
        init: 9,
        unknown: 10,
    };
    let sessions = $derived(
        (detail?.sessions ?? [])
            .slice()
            .sort((a, b) => (stateRank[a.state] ?? 10) - (stateRank[b.state] ?? 10) || a.session_id - b.session_id),
    );
    let totalPages = $derived(Math.max(1, detail?.page?.pages ?? (Math.ceil((detail?.total ?? 0) / pageSize) || 1)));
    let heartbeatMs = $derived(effectiveSessionHeartbeatMs(appState.config, detail ?? summary));
    let snapshot = $derived(detail ?? summary);
    let freshness = $derived(
        deriveSessionFreshness(snapshot, { serverTimeOffsetMs, heartbeatMs, nowMs: currentTimeMs }),
    );
    let collectionError = $derived((detail?.collection_status ?? summary?.collection_status) === 'error');
    let capabilities = $derived(detail?.capabilities ?? summary?.capabilities ?? {});
    let configuredActions = $derived(
        appState.config?.sessions?.enabled === true && appState.config?.sessions?.allow_actions === true,
    );
    let actionsAvailable = $derived(
        detail?.actions_available === true &&
            configuredActions &&
            freshness === 'fresh' &&
            !collectionError &&
            capabilities.session_actions === true,
    );
    let server = $derived(appState.servers.find((candidate) => candidate.host.toLowerCase() === host.toLowerCase()));
    let serverHistory = $derived(server ? (appState.serverMetrics.get(server.host) ?? []) : []);
    let sessionHistory = $derived(serverHistory.map((sample) => sample.sessions).filter(Number.isFinite));
    let totalSessions = $derived(detail?.summary?.total ?? null);
    let maxSessions = $derived(server?.max_sessions ?? null);
    let sessionWarnThreshold = $derived(appState.config?.session_warning_threshold ?? 80);

    async function load() {
        requestController?.abort();
        const controller = new AbortController();
        requestController = controller;
        loading = true;
        error = '';
        try {
            const next = await fetchSessionDetail(host, {
                q: submittedSearch.trim(),
                page,
                pageSize,
                signal: controller.signal,
            });
            if (controller.signal.aborted || requestController !== controller) return;
            detail = next;
            page = next?.query?.page ?? page;
            pageSize = next?.query?.page_size ?? pageSize;
        } catch (cause) {
            if (cause?.name !== 'AbortError')
                error =
                    cause?.status === 404
                        ? 'Current session data is no longer available for this host.'
                        : cause?.status === 403
                          ? 'Session detail is available to administrators only.'
                          : 'Session detail is temporarily unavailable. Try again.';
        } finally {
            if (!controller.signal.aborted && requestController === controller) loading = false;
        }
    }
    onMount(() => {
        function closeWhenLeavingMenu(event) {
            if (menu && !menuElement?.contains(event.target) && !menu.trigger?.contains(event.target)) {
                menu = null;
            }
        }
        resumeSessionActionPolling();
        load();
        document.addEventListener('pointerdown', closeWhenLeavingMenu, true);
        document.addEventListener('focusin', closeWhenLeavingMenu);
        return () => {
            requestController?.abort();
            document.removeEventListener('pointerdown', closeWhenLeavingMenu, true);
            document.removeEventListener('focusin', closeWhenLeavingMenu);
        };
    });
    $effect(() => {
        const timer = setInterval(() => {
            currentTimeMs = Date.now();
        }, 1000);
        return () => clearInterval(timer);
    });
    $effect(() => {
        const isFresh = freshness === 'fresh';
        if (wasFresh && !isFresh) {
            dialog = null;
            load();
        }
        wasFresh = isFresh;
    });
    $effect(() => {
        revision;
        if (revision) load();
    });
    function submitSearch() {
        page = 1;
        submittedSearch = search;
        load();
    }
    function changePage(next) {
        page = Math.max(1, Math.min(totalPages, next));
        load();
    }
    function timestamp(value) {
        return value == null ? '—' : new Date(value).toLocaleString();
    }
    function idle(value) {
        if (value == null) return '—';
        const seconds = Math.max(0, Math.floor((currentTimeMs - value) / 1000));
        return seconds < 3600
            ? `${Math.floor(seconds / 60)}m`
            : `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
    }
    function cpu(value) {
        return value == null ? '—' : `${Number(value).toFixed(1)}%`;
    }
    function actionReason(session) {
        if (!actionsAvailable)
            return collectionError
                ? 'Latest collection failed'
                : freshness !== 'fresh'
                  ? `Host is ${freshness}`
                  : !configuredActions
                    ? 'Session actions disabled'
                    : !capabilities.session_actions
                      ? 'Session actions unsupported'
                      : 'Session actions disabled';
        if (session.logon_at_ms == null) return 'Logon time unavailable';
        return '';
    }
    function closeMenu(returnFocus = false) {
        const trigger = menu?.trigger;
        menu = null;
        if (returnFocus) tick().then(() => trigger?.focus());
    }
    async function toggleMenu(session, trigger) {
        if (menu?.session.session_id === session.session_id) {
            closeMenu(true);
            return;
        }
        const rect = trigger.getBoundingClientRect();
        menuPosition = {
            left: Math.max(8, Math.min(rect.right - 210, window.innerWidth - 218)),
            top: Math.min(rect.bottom + 6, window.innerHeight - 196),
        };
        menu = { session, trigger };
        await tick();
        menuElement?.querySelector('button:not(:disabled)')?.focus();
    }
    function moveMenuFocus(event) {
        const items = [...menuElement.querySelectorAll('button:not(:disabled)')];
        const index = items.indexOf(document.activeElement);
        let next = null;
        if (event.key === 'ArrowDown') next = items[(index + 1) % items.length];
        if (event.key === 'ArrowUp') next = items[(index - 1 + items.length) % items.length];
        if (event.key === 'Home') next = items[0];
        if (event.key === 'End') next = items.at(-1);
        if (event.key === 'Escape') {
            event.preventDefault();
            closeMenu(true);
            return;
        }
        if (next) {
            event.preventDefault();
            next.focus();
        }
    }
    function openSessionDialog(session, action) {
        closeMenu();
        dialog = { session, action };
    }
    function runCopyShadow(session) {
        closeMenu(true);
        copyShadow(session);
    }
    async function launchShadow(session) {
        if (!isSafeShadowTarget(host, session.session_id)) {
            toast.err('Shadow is unavailable for this host or session.');
            return;
        }
        try {
            const { protocol_uri: protocolURI } = await fetchSessionShadow(host, session.session_id);
            if (!launchShadowProtocol(protocolURI, host, session.session_id)) throw new Error('Shadow launch failed');
            toast.ok('Shadow launch requested. Complete the browser and Remote Desktop prompts.');
        } catch {
            toast.err('Could not launch Shadow. Copy the command instead.');
        }
    }
    async function copyShadow(session) {
        if (!isSafeShadowTarget(host, session.session_id)) {
            toast.err('Shadow command is unavailable for this host or session.');
            return;
        }
        try {
            const { command } = await fetchSessionShadow(host, session.session_id);
            if (typeof command !== 'string') throw new Error('Invalid shadow command');
            if (!(await copyText(command))) throw new Error('Copy failed');
            toast.ok('Shadow command copied.');
        } catch {
            toast.err('Could not copy the shadow command.');
        }
    }
    function ratio(value, total) {
        return Number.isFinite(value) && total > 0 ? (value / total) * 100 : null;
    }
    function processText(processes) {
        return processes?.length
            ? processes.map((process) => `${process.image_name ?? '—'} (${process.pid})`).join(', ')
            : '—';
    }
</script>

<section class="host-detail" aria-live="polite" aria-busy={loading}>
    {#if loading && !detail}<div class="detail-state">Loading current session snapshot…</div>
    {:else if error}<div class="detail-state error" role="alert">{error}</div>
    {:else if detail}
        <div class="detail-tiles">
            <article class="detail-tile">
                <div class="tile-label">Session utilization</div>
                <div class="ring-row">
                    {#if maxSessions > 0}<RingGauge
                            value={(totalSessions / maxSessions) * 100}
                            max={100}
                            label="Sessions"
                            unit=" "
                            warnThreshold={sessionWarnThreshold}
                            critThreshold={Math.min(sessionWarnThreshold + 15, 100)}
                            centerLabel={String(totalSessions ?? '—')}
                            size={72}
                        />{:else}<strong class="total-value">{totalSessions ?? '—'}</strong>{/if}
                </div>
                <p class="tile-note">
                    {maxSessions > 0 ? `${totalSessions ?? '—'} of ${maxSessions} capacity` : 'Capacity unavailable'}
                </p>
                {#if sessionHistory.length >= 2}<Sparkline
                        data={sessionHistory}
                        color="var(--color-accent)"
                        height={34}
                    />{/if}
            </article>
            <article class="detail-tile">
                <div class="tile-label">Session composition</div>
                <div class="ring-row composition-row">
                    {#each [['Active', detail.summary?.active], ['Idle', detail.summary?.idle], ['Disconnected', detail.summary?.disconnected]] as [label, value]}<div
                            class="composition"
                        >
                            <RingGauge
                                value={ratio(value, totalSessions)}
                                max={100}
                                {label}
                                unit=" "
                                centerLabel={String(value ?? '—')}
                                size={56}
                            /><span>{label}</span>
                        </div>{/each}
                </div>
            </article>
            <article class="detail-tile">
                <div class="tile-label">Snapshot & availability</div>
                <div class="kv-row"><span>Host</span><strong>{host}</strong></div>
                <div class="kv-row"><span>Freshness</span><strong>{freshness}</strong></div>
                <div class="kv-row"><span>Mode</span><strong>{detail.mode ?? summary.mode ?? '—'}</strong></div>
                <div class="kv-row">
                    <span>Collector</span><strong
                        >{collectionError ? (detail.collection_error_code ?? 'Error') : 'OK'}</strong
                    >
                </div>
                <div class="kv-row">
                    <span>Actions</span><strong>{actionsAvailable ? 'Available' : 'Unavailable'}</strong>
                </div>
                <button
                    class="refresh btn-brutal"
                    onclick={load}
                    aria-label={`Refresh sessions for ${host}`}
                    disabled={loading}><RefreshCw size={15} class={loading ? 'spin' : ''} /> Refresh</button
                >
            </article>
            <article class="detail-tile roster-tile">
                <div class="roster-heading">
                    <div>
                        <div class="tile-label">Current sessions</div>
                        <strong class="mono">{host}</strong>
                    </div>
                    <form
                        class="detail-controls"
                        onsubmit={(event) => {
                            event.preventDefault();
                            submitSearch();
                        }}
                    >
                        <label class="sr-only" for={`session-search-${host}`}>Find current session</label><input
                            id={`session-search-${host}`}
                            class="settings-input"
                            bind:value={search}
                            maxlength="128"
                            placeholder="ID or visible user"
                        /><button class="btn-brutal" type="submit">Search</button>
                    </form>
                </div>
                {#if freshness === 'offline'}<p class="status-banner offline">
                        Host is offline. Values are retained from its last successful snapshot; actions are unavailable.
                    </p>{:else if collectionError}<p class="status-banner error">
                        Latest collection failed{detail.collection_error_code
                            ? ` (${detail.collection_error_code})`
                            : ''}. Last successful rows are retained; actions are unavailable.
                    </p>{:else if freshness === 'stale'}<p class="status-banner stale">
                        Snapshot is stale. Values may be outdated; actions are unavailable.
                    </p>{:else if freshness === 'unknown'}<p class="status-banner stale">
                        No successful current snapshot is available.
                    </p>{/if}
                {#if !capabilities.processes}<p class="status-banner capability">
                        Per-session process collection is unavailable on this host.
                    </p>{/if}{#if !capabilities.input_delay}<p class="status-banner capability">
                        Input-delay metrics are unavailable on this host.
                    </p>{/if}{#if !capabilities.session_actions}<p class="status-banner capability">
                        This host does not support session actions.
                    </p>{/if}
                {#if detail.summary?.total === 0}<p class="detail-state">
                        No current sessions in this complete snapshot.
                    </p>{:else if submittedSearch.trim() && detail.total === 0}<p class="detail-state">
                        No sessions match this filter.
                    </p>{:else}<p id={`session-detail-scroll-${host}`} class="scroll-hint">
                        Scroll horizontally for all session columns.
                    </p>
                    <!-- svelte-ignore a11y_no_noninteractive_tabindex: this named overflow region must accept keyboard scrolling -->
                    <div
                        class="detail-table-wrap scrollbar-styled"
                        tabindex="0"
                        role="region"
                        aria-label={`Scrollable current sessions for ${host}`}
                        aria-describedby={`session-detail-scroll-${host}`}
                    >
                        <table>
                            <thead
                                ><tr
                                    ><th scope="col">ID</th><th scope="col">User</th><th scope="col">State</th><th
                                        scope="col">Client</th
                                    ><th scope="col">Logon time</th><th scope="col">Idle</th><th scope="col">CPU</th><th
                                        scope="col">Mem</th
                                    ><th scope="col">App</th><th scope="col">Process</th><th scope="col">Actions</th
                                    ></tr
                                ></thead
                            ><tbody
                                >{#each sessions as session (session.session_id)}{@const unavailable =
                                        actionReason(session)}<tr
                                        ><td class="mono">{session.session_id}</td><td
                                            >{session.user ?? '—'}{session.domain ? `\\${session.domain}` : ''}</td
                                        ><td
                                            ><span class={`state state-${session.state}`}
                                                >{session.state ?? 'unknown'}</span
                                            ></td
                                        ><td>{session.client_name ?? session.client_address ?? '—'}</td><td
                                            >{timestamp(session.logon_at_ms)}</td
                                        ><td>{idle(session.idle_since_ms)}</td><td class="mono"
                                            >{cpu(session.cpu_percent)}</td
                                        ><td class="mono">{formatDecimalBytes(session.working_set_bytes)}</td><td
                                            >{session.station ?? '—'}</td
                                        ><td class="process-cell" title={processText(session.processes)}
                                            >{processText(session.processes)}</td
                                        ><td
                                            ><div
                                                class="session-actions"
                                                role="group"
                                                aria-label={`Session ${session.session_id} actions`}
                                            >
                                                <button
                                                    class="session-action-pill shadow-primary"
                                                    onclick={() => launchShadow(session)}
                                                    disabled={!isSafeShadowTarget(host, session.session_id)}
                                                    aria-label={`Launch Shadow for session ${session.session_id}`}
                                                    title="Launch Shadow"
                                                    ><ScreenShare size={14} aria-hidden="true" />
                                                    <span>Shadow</span></button
                                                ><button
                                                    class="session-menu-trigger"
                                                    onclick={(event) => toggleMenu(session, event.currentTarget)}
                                                    aria-label={`More actions for session ${session.session_id}`}
                                                    aria-haspopup="menu"
                                                    aria-controls={menuId}
                                                    aria-expanded={menu?.session.session_id === session.session_id}
                                                    title="More session actions"
                                                    ><Ellipsis size={17} aria-hidden="true" /></button
                                                >
                                            </div></td
                                        ></tr
                                    >{/each}</tbody
                            >
                        </table>
                    </div>
                    <nav class="pager" aria-label="Session detail pages">
                        <button class="btn-brutal" onclick={() => changePage(page - 1)} disabled={page <= 1}
                            >Previous</button
                        ><span>Page {page} of {totalPages}</span><button
                            class="btn-brutal"
                            onclick={() => changePage(page + 1)}
                            disabled={page >= totalPages}>Next</button
                        >
                    </nav>{/if}
            </article>
        </div>
    {/if}
</section>
{#if menu}{@const unavailable = actionReason(menu.session)}{@const shadowUnavailable = !isSafeShadowTarget(
        host,
        menu.session.session_id,
    )}
    <div
        bind:this={menuElement}
        id={menuId}
        class="session-action-menu"
        role="menu"
        tabindex="-1"
        aria-label={`Actions for session ${menu.session.session_id}`}
        style={`left: ${menuPosition.left}px; top: ${menuPosition.top}px`}
        onkeydown={moveMenuFocus}
    >
        <button
            class="session-menu-item"
            role="menuitem"
            onclick={() => runCopyShadow(menu.session)}
            disabled={shadowUnavailable}
            aria-label={`Copy Shadow command for session ${menu.session.session_id}${shadowUnavailable ? '. Shadow is unavailable for this host or session.' : ''}`}
            title={shadowUnavailable ? 'Shadow is unavailable for this host or session.' : 'Copy Shadow command'}
            ><Copy size={15} aria-hidden="true" />Copy Shadow command</button
        ><button
            class="session-menu-item"
            role="menuitem"
            onclick={() => openSessionDialog(menu.session, 'message')}
            disabled={!!unavailable}
            aria-label={`Message session ${menu.session.session_id}${unavailable ? `. Unavailable: ${unavailable}` : ''}`}
            title={unavailable || 'Message'}><MessageSquareText size={15} aria-hidden="true" />Message</button
        ><button
            class="session-menu-item warning"
            role="menuitem"
            onclick={() => openSessionDialog(menu.session, 'disconnect')}
            disabled={!!unavailable}
            aria-label={`Disconnect session ${menu.session.session_id}${unavailable ? `. Unavailable: ${unavailable}` : ''}`}
            title={unavailable || 'Disconnect'}><Unplug size={15} aria-hidden="true" />Disconnect</button
        ><button
            class="session-menu-item danger"
            role="menuitem"
            onclick={() => openSessionDialog(menu.session, 'logoff')}
            disabled={!!unavailable}
            aria-label={`Log off session ${menu.session.session_id}${unavailable ? `. Unavailable: ${unavailable}` : ''}`}
            title={unavailable || 'Log off'}><LogOut size={15} aria-hidden="true" />Log off</button
        >
    </div>
{/if}
{#if dialog}<SessionActionDialog
        {host}
        session={dialog.session}
        action={dialog.action}
        onclose={() => (dialog = null)}
        onqueued={() => load()}
    />{/if}

<style>
    .host-detail {
        background: var(--color-bg);
        padding: 12px 16px;
        font:
            12px 'JetBrains Mono',
            monospace;
    }
    .detail-tiles {
        display: flex;
        gap: 12px;
        flex-wrap: wrap;
    }
    .detail-tile {
        display: flex;
        flex-direction: column;
        min-width: 220px;
        flex: 1;
        padding: 10px 12px;
        border: 2.5px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 4px 4px 0 var(--color-shadow);
        background: var(--color-card);
    }
    .roster-tile {
        flex: 1 0 100%;
        min-width: 0;
    }
    .tile-label {
        margin-bottom: 8px;
        color: var(--color-accent);
        font-size: 11px;
        font-weight: 600;
        letter-spacing: 0.6px;
        text-transform: uppercase;
    }
    .ring-row {
        display: flex;
        justify-content: center;
        align-items: center;
        min-height: 80px;
    }
    .composition-row {
        gap: 8px;
        justify-content: space-around;
    }
    .composition {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 2px;
        color: var(--color-muted);
        font-size: 10px;
    }
    .total-value {
        font-size: 1.75rem;
    }
    .tile-note {
        margin: 3px 0 8px;
        color: var(--color-muted);
        text-align: center;
    }
    .kv-row {
        display: flex;
        justify-content: space-between;
        gap: 8px;
        padding: 3px 4px;
    }
    .kv-row:nth-of-type(even) {
        background: var(--color-surface);
    }
    .kv-row span {
        color: var(--color-muted);
    }
    .kv-row strong {
        text-align: right;
        overflow-wrap: anywhere;
    }
    .refresh {
        margin-top: auto;
        align-self: flex-start;
        padding: 4px 8px;
        font-size: 0.68rem;
        background: var(--color-card);
    }
    .roster-heading {
        display: flex;
        align-items: end;
        justify-content: space-between;
        gap: 12px;
        flex-wrap: wrap;
    }
    .detail-controls {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
    }
    .detail-controls input {
        min-width: 13rem;
    }
    .detail-controls .btn-brutal,
    .pager .btn-brutal {
        padding: 5px 9px;
        background: var(--color-card);
        font-size: 0.68rem;
    }
    .status-banner,
    .detail-state {
        margin: 8px 0;
        padding: 8px 10px;
        border-left: 4px solid var(--color-muted);
        background: var(--color-surface);
    }
    .status-banner.error,
    .detail-state.error {
        border-left-color: var(--color-red);
    }
    .status-banner.stale {
        border-left-color: var(--color-amber);
    }
    .status-banner.capability {
        border-left-color: var(--color-blue);
    }
    .scroll-hint {
        margin: 8px 0 4px;
        color: var(--color-muted);
        font-size: 0.72rem;
    }
    .detail-table-wrap {
        min-width: 0;
        max-inline-size: 100%;
        overflow-x: auto;
    }
    .detail-table-wrap:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
    table {
        width: 100%;
        min-width: 1050px;
        border-collapse: collapse;
        font-size: 0.78rem;
    }
    th {
        background: var(--color-surface);
        color: var(--color-muted);
        font:
            700 0.64rem/1.2 'JetBrains Mono',
            monospace;
        letter-spacing: 0.04em;
        text-align: left;
        text-transform: uppercase;
    }
    th,
    td {
        padding: 0.42rem 0.5rem;
        border-bottom: 1px solid var(--color-border);
        vertical-align: top;
    }
    .state {
        font:
            700 0.66rem 'JetBrains Mono',
            monospace;
        text-transform: uppercase;
    }
    .state-active,
    .state-connected {
        color: var(--color-green);
    }
    .state-disconnected,
    .state-idle {
        color: var(--color-amber);
    }
    .process-cell {
        max-width: 15rem;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .session-actions {
        display: flex;
        gap: 0.35rem;
        align-items: center;
        white-space: nowrap;
    }
    .session-action-pill,
    .session-menu-trigger,
    .session-menu-item {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 0.4rem;
        border: 1px solid var(--color-border);
        cursor: pointer;
        font:
            600 0.65rem/1 'JetBrains Mono',
            monospace;
    }
    .session-action-pill {
        min-height: 1.85rem;
        padding: 0.3rem 0.54rem;
        border-radius: 999px;
    }
    .shadow-primary {
        background: var(--color-accent);
        border-color: var(--color-accent);
        color: #fff;
    }
    .session-menu-trigger {
        width: 1.85rem;
        height: 1.85rem;
        padding: 0;
        border-radius: 0.3rem;
        background: var(--color-card);
        color: var(--color-fg);
    }
    .session-action-menu {
        position: fixed;
        z-index: 20;
        display: grid;
        min-width: 13.1rem;
        padding: 0.28rem;
        border: 2px solid var(--color-border);
        border-radius: 0.35rem;
        background: var(--color-card);
        box-shadow: 4px 4px 0 var(--color-shadow);
    }
    .session-menu-item {
        justify-content: flex-start;
        min-height: 2rem;
        padding: 0.4rem 0.5rem;
        border-color: transparent;
        border-radius: 0.18rem;
        background: transparent;
        color: var(--color-fg);
        text-align: left;
    }
    .session-menu-item:hover:not(:disabled),
    .session-menu-item:focus-visible {
        background: var(--color-surface);
    }
    .session-menu-item.warning {
        color: #713f12;
        background: #fef3c7;
    }
    .session-menu-item.warning:hover:not(:disabled),
    .session-menu-item.warning:focus-visible {
        background: #fde68a;
        color: #713f12;
    }
    .session-menu-item.danger {
        background: #b91c1c;
        color: #fff;
    }
    .session-menu-item.danger:hover:not(:disabled),
    .session-menu-item.danger:focus-visible {
        background: #991b1b;
        color: #fff;
    }
    .session-menu-item.danger:disabled {
        background: #7f1d1d;
        color: #fff;
        opacity: 1;
    }
    .session-action-pill:focus-visible,
    .session-menu-trigger:focus-visible,
    .session-menu-item:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
    .session-action-pill:disabled,
    .session-menu-trigger:disabled,
    .session-menu-item:disabled:not(.danger),
    .refresh:disabled {
        cursor: not-allowed;
        opacity: 0.48;
        box-shadow: none;
    }
    .session-menu-item.warning:disabled {
        background: #fde68a;
        color: #713f12;
        opacity: 1;
    }
    .pager {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 0.6rem;
        margin-top: 0.7rem;
        font-size: 0.75rem;
        flex-wrap: wrap;
    }
    .spin {
        animation: spin 0.8s linear infinite;
    }
    @keyframes spin {
        to {
            transform: rotate(360deg);
        }
    }
    @media (max-width: 700px) {
        .host-detail {
            box-sizing: border-box;
            width: calc(100vw - 52px);
            padding: 12px;
        }
        .detail-tile {
            min-width: 100%;
        }
        .detail-controls input {
            min-width: 0;
            flex: 1 1 12rem;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .spin {
            animation: none;
        }
    }
</style>
