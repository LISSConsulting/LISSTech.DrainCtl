<script>
    import { untrack } from 'svelte';
    import { fly } from 'svelte/transition';
    import { initTheme } from './lib/theme.svelte.js';
    import {
        appState,
        addEvent,
        appendPerfToRingBuffer,
        logStatusTransition,
        removeServerMetrics,
        seedServerMetrics,
        setDetectorStatus,
        appendRecentSpike,
        removeEvtSpikeState,
    } from './lib/state.svelte.js';
    import { fetchServers, fetchHealth, fetchSettings, fetchAllServerMetrics } from './lib/api.js';
    import { authState, checkSession } from './lib/auth.svelte.js';
    import { resolveThresholds, getThresholdColor } from './lib/thresholds.js';
    import { formatTime12 } from './lib/utils.js';

    import Nav from './components/Nav.svelte';
    import Login from './components/Login.svelte';
    import Footer from './components/Footer.svelte';
    import CounterGrid from './components/CounterGrid.svelte';
    import StateBar from './components/StateBar.svelte';
    import MetricsChart from './components/MetricsChart.svelte';
    import EventLog from './components/EventLog.svelte';
    import ServerTable from './components/ServerTable.svelte';
    import ConfigModal from './components/ConfigModal.svelte';
    import HistoryModal from './components/HistoryModal.svelte';
    import Toast from './components/Toast.svelte';

    // ---------------------------------------------------------------------------
    // Initialise theme once on load
    // ---------------------------------------------------------------------------
    initTheme();

    // ---------------------------------------------------------------------------
    // Modal visibility state
    // ---------------------------------------------------------------------------
    let configOpen = $state(false);
    /** @type {string|null} */
    let historyHost = $state(null);

    // ---------------------------------------------------------------------------
    // State transition tracking — detect status changes between refresh cycles
    // ---------------------------------------------------------------------------

    /** Previous server statuses keyed by hostname. Empty on first refresh → all servers emit "registered". */
    const prevStates = /** @type {Map<string, string>} */ (new Map());

    /**
     * Previous perf alert levels per host. Tracks threshold crossings so an
     * event is emitted only when a metric enters or leaves warn/crit territory
     * — not on every 30-second refresh while it stays elevated.
     * @type {Map<string, {cpu: string, mem: string, delay: string}>}
     */
    const prevAlerts = new Map();

    /** Map a getThresholdColor result to an alert level token. */
    function colorToLevel(color) {
        if (color === 'red') return 'crit';
        if (color === 'amber') return 'warn';
        return 'ok';
    }
    /** @param {'ok'|'warning'|'grace'|'alert'|'off'} s @returns {string} */
    function statusLabel(s) {
        return { ok: 'Healthy', warning: 'Warning', grace: 'Grace', alert: 'Alert', off: 'Offline' }[s] ?? s;
    }

    /** Map a server status to the event severity used by EventLog for colouring. */
    function statusSev(s) {
        if (s === 'alert' || s === 'off') return 'alert';
        if (s === 'grace' || s === 'warning') return 'grace';
        return 'ok';
    }

    // ---------------------------------------------------------------------------
    // Event helpers
    // ---------------------------------------------------------------------------

    /**
     * Build a rich structured event object for a per-server state transition.
     * Embeds the server's current drain state, perf metrics, and session counts
     * so EventLog can render a detailed snapshot inline.
     *
     * Event severity is promoted beyond the drain-state severity when perf
     * metrics exceed configured thresholds — a "Healthy" drain state with
     * critical CPU should not render as a green "everything is fine" event.
     * @param {string} time
     * @param {import('./lib/api.js').Server} sv
     * @param {string} text
     * @param {'ok'|'warning'|'grace'|'alert'|'off'} sev
     */
    function serverEvent(time, sv, text, sev) {
        const memUsedPct =
            sv.perf && sv.perf.mem_total_mb > 0
                ? Math.round(((sv.perf.mem_total_mb - sv.perf.mem_avail_mb) / sv.perf.mem_total_mb) * 100)
                : null;

        // Promote event severity when perf metrics exceed thresholds.
        if (sv.perf) {
            const perfCfg = appState.config?.performance ?? null;
            const checks = /** @type {[number|null, string][]} */ ([
                [sv.perf.cpu_pct, 'cpu'],
                [memUsedPct, 'mem'],
                [sv.perf.input_delay_p95_ms, 'inputDelay'],
            ]);
            for (const [val, key] of checks) {
                const t = resolveThresholds(/** @type {any} */ (key), perfCfg);
                const color = getThresholdColor(val, t.warn, t.crit);
                if (color === 'red') sev = 'alert';
                else if (color === 'amber' && sev !== 'alert') sev = 'grace';
            }
        }

        return {
            time,
            host: sv.host.split('.')[0],
            text,
            sev,
            transition: true,
            drain_state: sv.status,
            drain_mode: sv.drain_mode ?? null,
            state_duration_seconds: sv.state_duration_seconds ?? null,
            changed_by: sv.changed_by ?? '',
            sessions_active: sv.sessions_active ?? sv.sessions,
            sessions_disconnected: sv.sessions_disconnected ?? 0,
            sessions_max: sv.max_sessions,
            cpu_pct: sv.perf?.cpu_pct ?? null,
            mem_used_pct: memUsedPct,
            input_delay_p95_ms: sv.perf?.input_delay_p95_ms ?? null,
            pages_sec: sv.perf?.pages_sec ?? null,
            tcp_retrans_sec: sv.perf?.tcp_retrans_sec ?? null,
            disk_queue: sv.perf?.disk_queue ?? null,
        };
    }

    // ---------------------------------------------------------------------------
    // Refresh logic
    // ---------------------------------------------------------------------------

    let refreshing = $state(false);
    let metricSeedDone = false;

    /**
     * Pull fresh data from the API and update global state.
     * Guard prevents concurrent calls — if the previous fetch hasn't resolved
     * before the 30-second tick fires, the tick is skipped.
     */
    async function refresh() {
        if (refreshing) return;
        refreshing = true;
        try {
            // Fetch config once on the first successful refresh (lazy load).
            // Seed retained per-host history on the first call so sparklines
            // show historical data immediately, before any live polls accumulate.
            const calls = /** @type {Promise<any>[]} */ ([fetchServers(), fetchHealth()]);
            const needsConfig = appState.config === null;
            if (needsConfig) calls.push(fetchSettings());
            const metricSeedPromise = !metricSeedDone
                ? fetchAllServerMetrics().catch(() => null)
                : Promise.resolve(null);

            const [results, seedData] = await Promise.all([Promise.all(calls), metricSeedPromise]);
            const [servers, health] = results;

            // Merge poll results: only update servers where poll data is newer
            // than what SSE may have delivered while the poll was in flight.
            if (servers) {
                const existing = new Map(appState.servers.map((s) => [s.host, s]));
                appState.servers = servers.map((polled) => {
                    const cur = existing.get(polled.host);
                    if (cur?.last_seen && polled.last_seen && new Date(cur.last_seen) > new Date(polled.last_seen)) {
                        return cur;
                    }
                    return polled;
                });
            }
            appState.health = health;
            if (needsConfig) appState.config = results[2] ?? null;
            appState.connected = true;
            appState.lastUpdated = new Date();

            // Seed per-server ring buffers from retained history on the first load.
            // Overwrites any stale browser-local data so SQLite-retained history is
            // authoritative at cold start; subsequent refreshes skip this entirely.
            if (seedData) {
                const seedMap = new Map(Object.entries(seedData));
                seedServerMetrics(seedMap);
                metricSeedDone = true;
            }

            // Detect and log server state transitions.
            // On the first refresh prevStates is empty, so every server emits a
            // "registered" event — giving the user an initial status snapshot.
            // Subsequent stable cycles emit nothing; transitions are always logged.
            const evtTime = formatTime12(new Date(), { seconds: true });
            const seenHosts = new Set((servers || []).map((sv) => sv.host));
            for (const sv of servers || []) {
                const prev = prevStates.get(sv.host);
                if (prev === undefined) {
                    logStatusTransition(null, sv.status, sv.host, evtTime);
                } else if (prev !== sv.status) {
                    logStatusTransition(prev, sv.status, sv.host, evtTime);
                }
            }
            for (const host of prevStates.keys()) {
                if (!seenHosts.has(host)) {
                    addEvent({
                        time: evtTime,
                        host: host.split('.')[0],
                        text: 'removed from dashboard',
                        sev: 'alert',
                        transition: true,
                        drain_state: 'off',
                    });
                }
            }
            // Snapshot for next cycle.
            prevStates.clear();
            for (const sv of servers || []) prevStates.set(sv.host, sv.status);

            // Detect perf threshold crossings — emit events when a metric
            // enters or leaves warn/crit. Only fires on transitions, not
            // while a metric stays at the same level between refreshes.
            const perfCfg = appState.config?.performance ?? null;
            const cpuTh = resolveThresholds('cpu', perfCfg);
            const memTh = resolveThresholds('mem', perfCfg);
            const delayTh = resolveThresholds('inputDelay', perfCfg);

            /** @type {Map<string, {cpu: string, mem: string, delay: string}>} */
            const nextAlerts = new Map();
            for (const sv of servers || []) {
                if (!sv.perf) continue;
                const memUsedPct =
                    sv.perf.mem_total_mb > 0
                        ? Math.round(((sv.perf.mem_total_mb - sv.perf.mem_avail_mb) / sv.perf.mem_total_mb) * 100)
                        : 0;
                const cur = {
                    cpu: colorToLevel(getThresholdColor(sv.perf.cpu_pct, cpuTh.warn, cpuTh.crit)),
                    mem: colorToLevel(getThresholdColor(memUsedPct, memTh.warn, memTh.crit)),
                    delay: colorToLevel(
                        getThresholdColor(sv.perf.input_delay_p95_ms, delayTh.warn, delayTh.crit),
                    ),
                };
                const prev = prevAlerts.get(sv.host) ?? { cpu: 'ok', mem: 'ok', delay: 'ok' };

                /** @type {[string, string, number, string][]} */
                const checks = [
                    ['cpu', 'CPU', sv.perf.cpu_pct, '%'],
                    ['mem', 'Memory', memUsedPct, '%'],
                    ['delay', 'Input delay P95', sv.perf.input_delay_p95_ms, 'ms'],
                ];
                for (const [key, label, val, unit] of checks) {
                    if (cur[key] === prev[key]) continue;
                    if (cur[key] === 'crit') {
                        addEvent(
                            serverEvent(evtTime, sv, `${label} critical — ${val.toFixed(1)}${unit}`, 'alert'),
                        );
                    } else if (cur[key] === 'warn') {
                        addEvent(
                            serverEvent(evtTime, sv, `${label} warning — ${val.toFixed(1)}${unit}`, 'grace'),
                        );
                    } else if (prev[key] !== 'ok') {
                        addEvent(serverEvent(evtTime, sv, `${label} recovered`, 'ok'));
                    }
                }
                nextAlerts.set(sv.host, cur);
            }
            prevAlerts.clear();
            for (const [host, state] of nextAlerts) prevAlerts.set(host, state);

            // Per-server ring buffers for per-host sparklines in ServerDetail.
            const s = appState.servers;
            const ts = Date.now();
            for (const sv of s) {
                if (sv.perf) {
                    appendPerfToRingBuffer({
                        host: sv.host,
                        time: ts,
                        cpu: sv.perf.cpu_pct,
                        mem: sv.perf.mem_total_mb > 0 ? (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100 : 0,
                        inputDelay: sv.perf.input_delay_p95_ms,
                        sessions: sv.sessions ?? 0,
                        diskQueue: sv.perf.disk_queue ?? 0,
                        tcpRetrans: sv.perf.tcp_retrans_sec ?? 0,
                        pagesPerSec: sv.perf.pages_sec ?? 0,
                    });
                }
            }

        } catch (e) {
            appState.connected = false;
            addEvent({
                time: formatTime12(new Date(), { seconds: true }),
                host: '',
                text: `Refresh failed: ${e?.message ?? e}`,
                sev: 'alert',
                transition: false,
            });
        } finally {
            refreshing = false;
        }
    }

    // Scroll to top on view change.
    $effect(() => {
        appState.currentView; // track
        window.scrollTo({ top: 0, behavior: 'smooth' });
    });

    // Reduce-motion toggle: stamp the preference on <body> so the global CSS
    // in app.css can neutralise animations, transitions, and backdrop blurs
    // without every component having to opt in individually. This is the
    // single escape hatch for RDP sessions where compositor-heavy effects
    // cost real CPU on the thin-client endpoint.
    $effect(() => {
        const on = appState.reduceMotion;
        if (on) {
            document.body.setAttribute('data-reduce-motion', 'true');
        } else {
            document.body.removeAttribute('data-reduce-motion');
        }
    });

    // On mount, check for an existing valid session. If the cookie is still live
    // the user goes straight to the dashboard; otherwise the login page shows
    // immediately — no Negotiate handshake, no Windows popup.
    $effect(() => {
        untrack(() => checkSession());
    });

    // Poll for fresh data every 30 seconds — only while authenticated.
    // Also trigger a full sync when the tab regains focus (SSE events may
    // have been missed or throttled while the tab was backgrounded).
    $effect(() => {
        if (!authState.username) return;
        untrack(() => refresh());
        const interval = setInterval(refresh, 30_000);
        const onVisible = () => { if (!document.hidden) untrack(() => refresh()); };
        document.addEventListener('visibilitychange', onVisible);
        return () => {
            clearInterval(interval);
            document.removeEventListener('visibilitychange', onVisible);
            // Reset transition-tracking maps so the next login sees every server
            // as "new" and emits fresh "registered" events. Without this, re-login
            // without a page reload silently skips "registered" for servers whose
            // status hasn't changed since the last session.
            prevStates.clear();
            prevAlerts.clear();
        };
    });

    // SSE: real-time event stream — supplements polling with instant updates.
    // EventSource auto-reconnects on network errors (~3s default retry).
    $effect(() => {
        if (!authState.username) return;

        const es = new EventSource('/api/v1/events');

        es.onopen = () => {
            appState.sseConnected = true;
            appState.sseReconnecting = false;
        };

        es.onmessage = (e) => {
            try {
                const event = JSON.parse(e.data);
                if (event.type === 'server_update' && event.host && event.data) {
                    const sv = event.data;
                    // Detect status transitions immediately so the event log updates
                    // in real-time rather than waiting for the next 30-second poll.
                    // Also keep prevStates current so the poll doesn't re-log the same
                    // transition as a duplicate.
                    const prevStatus = prevStates.get(event.host);
                    const evtTime = formatTime12(new Date(), { seconds: true });
                    if (prevStatus === undefined) {
                        // Brand-new server appearing via SSE — log "registered" so the
                        // event log captures the first appearance instead of being silent.
                        // Without this, prevStates would be pre-set before the poll cycle
                        // runs, so the poll would also skip the "registered" log entry.
                        logStatusTransition(null, sv.status, sv.host, evtTime);
                    } else if (prevStatus !== sv.status) {
                        logStatusTransition(prevStatus, sv.status, sv.host, evtTime);
                    }
                    if (sv.status) prevStates.set(event.host, sv.status);
                    appState.handleSSEServerUpdate(event.host, sv);
                    // Feed per-server sparkline ring buffer so ServerDetail charts
                    // update in real-time instead of waiting for the next 30-second poll.
                    if (sv.perf) {
                        appendPerfToRingBuffer({
                            host: sv.host,
                            time: Date.now(),
                            cpu: sv.perf.cpu_pct,
                            mem:
                                sv.perf.mem_total_mb > 0
                                    ? (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100
                                    : 0,
                            inputDelay: sv.perf.input_delay_p95_ms,
                            sessions: sv.sessions ?? 0,
                            diskQueue: sv.perf.disk_queue ?? 0,
                            tcpRetrans: sv.perf.tcp_retrans_sec ?? 0,
                            pagesPerSec: sv.perf.pages_sec ?? 0,
                        });
                    }
                } else if (event.type === 'server_deleted' && event.host) {
                    // Server removed via REST API — update the list immediately so
                    // operators see it disappear without waiting for the next poll cycle.
                    appState.handleSSEServerDeleted(event.host);
                    removeServerMetrics(event.host);
                    removeEvtSpikeState(event.host);
                    prevStates.delete(event.host);
                    addEvent(
                        serverEvent(
                            formatTime12(new Date(), { seconds: true }),
                            { host: event.host, status: 'off', changed_by: event.data?.changed_by ?? '' },
                            'removed from dashboard',
                            'alert',
                        ),
                    );
                } else if (event.type === 'detector_status' && event.host && event.data) {
                    // Broker fires detector_status only on state transitions (see
                    // dashboard-sse-events.md), so no client-side dedup is needed.
                    setDetectorStatus(event.host, event.data);
                } else if (event.type === 'recent_spike' && event.host && event.data) {
                    appendRecentSpike(event.host, event.data);
                } else if (event.type === 'settings_update' && event.data) {
                    // Go stores memory thresholds as % free; UI works in % used —
                    // apply the same inversion that fetchSettings() does on REST load.
                    const cfg = event.data;
                    if (cfg.performance) {
                        cfg.performance.mem_warn_pct = 100 - (cfg.performance.mem_warn_pct ?? 0);
                        cfg.performance.mem_crit_pct = 100 - (cfg.performance.mem_crit_pct ?? 0);
                    }
                    appState.config = cfg;
                    addEvent({
                        time: formatTime12(new Date(), { seconds: true }),
                        host: '',
                        text: 'settings updated',
                        sev: 'ok',
                        transition: false,
                    });
                }
            } catch (e) { console.warn('[SSE] malformed event, ignored:', e); }
        };

        es.onerror = () => {
            appState.sseConnected = false;
            if (es.readyState === EventSource.CONNECTING) {
                // Transient error — EventSource will auto-retry. Show reconnecting
                // indicator in the footer so operators can distinguish "no SSE yet"
                // from "SSE dropped and is recovering".
                appState.sseReconnecting = true;
            } else {
                // Permanently closed (e.g. 401 session expiry) — stop reconnecting
                // and trigger an immediate session check.
                appState.sseReconnecting = false;
                es.close();
                untrack(() => refresh());
            }
        };

        return () => {
            es.close();
            appState.sseConnected = false;
            appState.sseReconnecting = false;
        };
    });
</script>

{#if authState.loading}
    <div class="auth-loading">
        <span class="auth-spinner"></span>
    </div>
{:else if !authState.username}
    <Login />
{:else}
    <Nav onconfigopen={() => (configOpen = true)} />

    <main class="main">
        {#key appState.currentView}
            <div in:fly={{ y: 12, duration: 120, delay: 60 }} out:fly={{ y: -6, duration: 80 }}>
                {#if appState.currentView === 'overview'}
                    <CounterGrid />
                    <StateBar />
                    <MetricsChart />
                {:else if appState.currentView === 'servers'}
                    <ServerTable onhistoryclick={(host) => (historyHost = host)} />
                {:else if appState.currentView === 'events'}
                    <EventLog />
                {/if}
            </div>
        {/key}
    </main>

    <Footer onrefresh={refresh} {refreshing} />

    {#if configOpen}
        <ConfigModal onclose={() => (configOpen = false)} />
    {/if}

    {#if historyHost}
        <HistoryModal host={historyHost} onclose={() => (historyHost = null)} />
    {/if}
{/if}

<Toast />

<style>
    .main {
        flex: 1;
        max-width: 1400px;
        margin: 0 auto;
        padding: 28px 24px 48px;
        width: 100%;
    }

    /* ── Auth loading screen ───────────────────────────────────── */
    .auth-loading {
        min-height: 100vh;
        display: flex;
        align-items: center;
        justify-content: center;
    }

    .auth-spinner {
        display: inline-block;
        width: 28px;
        height: 28px;
        border: 3px solid var(--color-border);
        border-top-color: var(--color-accent);
        border-radius: 50%;
        animation: spin 0.7s linear infinite;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }

</style>
