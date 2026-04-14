<script>
    import { untrack } from 'svelte';
    import { fly } from 'svelte/transition';
    import { initTheme } from './lib/theme.svelte.js';
    import {
        appState,
        addEvent,
        appendMetricsSample,
        appendServerMetricsSample,
        seedServerMetrics,
        appendSessionSample,
        appendRfxSample,
        deriveP95,
        deriveP50,
    } from './lib/state.svelte.js';
    import { fetchServers, fetchHealth, fetchSettings, fetchAllServerMetrics } from './lib/api.js';
    import { authState, checkSession } from './lib/auth.svelte.js';
    import { resolveThresholds, getThresholdColor } from './lib/thresholds.js';

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

    let refreshing = false;

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
            // Also fetch metric history in parallel when serverMetrics is empty so
            // sparklines are populated immediately on a cold start.
            const calls = /** @type {Promise<any>[]} */ ([fetchServers(), fetchHealth()]);
            const needsConfig = appState.config === null;
            const needsMetricSeed = appState.serverMetrics.size === 0;
            if (needsConfig) calls.push(fetchSettings());
            // Fetch seed history in parallel; silently ignore failures (non-mock envs
            // won't have this endpoint and should fall back to natural poll accumulation).
            const metricSeedPromise = needsMetricSeed
                ? fetchAllServerMetrics().catch(() => null)
                : Promise.resolve(null);

            const [results, seedData] = await Promise.all([Promise.all(calls), metricSeedPromise]);
            const [servers, health] = results;

            appState.servers = servers || [];
            appState.health = health;
            if (needsConfig) appState.config = results[2] ?? null;
            appState.connected = true;
            appState.lastUpdated = new Date();

            // Seed per-server metric history from the mock endpoint (cold start only).
            // seedServerMetrics skips hosts that already have live data so this is safe
            // to call even when some samples have arrived via earlier poll cycles.
            // Also synthesize fleet-wide metricsHistory so LOAD and HIC charts are
            // populated immediately instead of waiting N×30 s to fill.
            if (seedData) {
                const seedMap = new Map(Object.entries(seedData));
                seedServerMetrics(seedMap);

                if (appState.metricsHistory.length < 2) {
                    const hosts = [...seedMap.values()];
                    const sampleCount = Math.max(...hosts.map((h) => h.length), 0);
                    /** @type {import('./lib/state.svelte.js').MetricsSample[]} */
                    const fleetHistory = [];
                    /** @type {import('./lib/state.svelte.js').SessionSample[]} */
                    const sessHistory = [];
                    /** @type {import('./lib/state.svelte.js').RfxSample[]} */
                    const rfxHistSeed = [];
                    for (let i = 0; i < sampleCount; i++) {
                        const slices = hosts.map((h) => h[i]).filter(Boolean);
                        if (slices.length === 0) continue;
                        const cpuVals = slices.map((s) => s.cpu ?? 0);
                        const memVals = slices.map((s) => s.mem ?? 0);
                        const avgCpu = cpuVals.reduce((a, b) => a + b, 0) / cpuVals.length;
                        const avgMem = memVals.reduce((a, b) => a + b, 0) / memVals.length;
                        const totalSess = slices.reduce((a, s) => a + (s.sessions ?? 0), 0);
                        const idV = slices.map((s) => s.inputDelay ?? 0);
                        const psV = slices.map((s) => s.pagesPerSec ?? 0);
                        const trV = slices.map((s) => s.tcpRetrans ?? 0);
                        const dqV = slices.map((s) => s.diskQueue ?? 0);
                        fleetHistory.push({
                            time: slices[0].time,
                            cpu: avgCpu,
                            mem: avgMem,
                            sessions: totalSess,
                            inputDelay: deriveP95(idV),
                            pagesPerSec: deriveP95(psV),
                            tcpRetrans: deriveP95(trV),
                            diskQueue: deriveP95(dqV),
                            p50InputDelay: deriveP50(idV),
                            p50PagesPerSec: deriveP50(psV),
                            p50TcpRetrans: deriveP50(trV),
                            p50DiskQueue: deriveP50(dqV),
                        });

                        // Session aggregate history
                        const active = slices.reduce((a, s) => a + (s.sessionsActive ?? s.sessions ?? 0), 0);
                        const disconnected = slices.reduce((a, s) => a + (s.sessionsDisconnected ?? 0), 0);
                        const total = active + disconnected;
                        const maxAll = slices.reduce((a, s) => a + (s.maxSessions ?? 0), 0);
                        sessHistory.push({
                            ts: slices[0].time,
                            active,
                            disconnected,
                            total,
                            utilization: maxAll > 0 ? Math.round((total / maxAll) * 100) : 0,
                            sessionCpuP95: deriveP95(slices.map((s) => s.sessionCpuP95 ?? 0)),
                            sessionMemP95: deriveP95(slices.map((s) => s.sessionMemP95 ?? 0)),
                            sessionCpuP50: deriveP50(slices.map((s) => s.sessionCpuP50 ?? 0)),
                            sessionMemP50: deriveP50(slices.map((s) => s.sessionMemP50 ?? 0)),
                        });

                        // RemoteFX aggregate history
                        rfxHistSeed.push({
                            ts: slices[0].time,
                            fpsOut: deriveP95(slices.map((s) => s.rfxFpsOut ?? 0)),
                            encodeMs: deriveP95(slices.map((s) => s.rfxEncodeMs ?? 0)),
                            quality: deriveP95(slices.map((s) => s.rfxQuality ?? 0)),
                            rtt: deriveP95(slices.map((s) => s.rfxRtt ?? 0)),
                            loss: deriveP95(slices.map((s) => s.rfxLoss ?? 0)),
                            skipServer: deriveP95(slices.map((s) => s.rfxSkipServer ?? 0)),
                            skipNet: deriveP95(slices.map((s) => s.rfxSkipNet ?? 0)),
                            fpsOutP50: deriveP50(slices.map((s) => s.rfxFpsOutP50 ?? 0)),
                            encodeMsP50: deriveP50(slices.map((s) => s.rfxEncodeMsP50 ?? 0)),
                            qualityP50: deriveP50(slices.map((s) => s.rfxQualityP50 ?? 0)),
                            rttP50: deriveP50(slices.map((s) => s.rfxRttP50 ?? 0)),
                            lossP50: deriveP50(slices.map((s) => s.rfxLossP50 ?? 0)),
                            skipServerP50: deriveP50(slices.map((s) => s.rfxSkipServerP50 ?? 0)),
                            skipNetP50: deriveP50(slices.map((s) => s.rfxSkipNetP50 ?? 0)),
                        });
                    }
                    if (fleetHistory.length > 0) {
                        appState.metricsHistory = fleetHistory;
                    }
                    if (sessHistory.length > 0) {
                        appState.sessionHistory = sessHistory;
                    }
                    if (rfxHistSeed.length > 0) {
                        appState.remoteFxHistory = rfxHistSeed;
                        appState.rfxAvailable = true;
                    }
                }
            }

            // Detect and log server state transitions.
            // On the first refresh prevStates is empty, so every server emits a
            // "registered" event — giving the user an initial status snapshot.
            // Subsequent stable cycles emit nothing; transitions are always logged.
            const evtTime = new Date().toLocaleTimeString('en-US', {
                hour: '2-digit',
                minute: '2-digit',
                second: '2-digit',
                hour12: false,
            });
            const seenHosts = new Set((servers || []).map((sv) => sv.host));
            for (const sv of servers || []) {
                const prev = prevStates.get(sv.host);
                if (prev === undefined) {
                    addEvent(serverEvent(evtTime, sv, `registered (${statusLabel(sv.status)})`, statusSev(sv.status)));
                } else if (prev !== sv.status) {
                    addEvent(
                        serverEvent(
                            evtTime,
                            sv,
                            `${statusLabel(prev)} → ${statusLabel(sv.status)}`,
                            statusSev(sv.status),
                        ),
                    );
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

            // Compute fleet-wide P95 for health metrics — reveals outlier servers that
            // averages would smooth out. CPU and memory stay as averages (capacity planning).
            const s = appState.servers;
            const perfSvs = s.filter((sv) => sv.perf);

            const cpu = perfSvs.length ? perfSvs.reduce((a, sv) => a + (sv.perf.cpu_pct || 0), 0) / perfSvs.length : 0;
            const cpuP95Vals = perfSvs.map((sv) => sv.perf.cpu_p95_pct || sv.perf.cpu_pct || 0);
            const cpuP95 = deriveP95(cpuP95Vals);
            const memSvs = s.filter((sv) => sv.perf?.mem_total_mb > 0);
            const memPct = memSvs.length
                ? memSvs.reduce((a, sv) => a + (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100, 0) /
                  memSvs.length
                : 0;
            const idVals = perfSvs.map((sv) => sv.perf.input_delay_p95_ms || 0);
            const psVals = perfSvs.map((sv) => sv.perf.pages_sec || 0);
            const trVals = perfSvs.map((sv) => sv.perf.tcp_retrans_sec || 0);
            const dqVals = perfSvs.map((sv) => sv.perf.disk_queue || 0);
            const inputDelay = deriveP95(idVals);
            const pagesPerSec = deriveP95(psVals);
            const tcpRetrans = deriveP95(trVals);
            const diskQueue = deriveP95(dqVals);
            const sessions = s.reduce((a, sv) => a + (sv.sessions || 0), 0);

            const ts = Date.now();
            appendMetricsSample({
                time: ts,
                cpu,
                cpuP95,
                mem: memPct,
                inputDelay,
                sessions,
                pagesPerSec,
                tcpRetrans,
                diskQueue,
                p50InputDelay: deriveP50(idVals),
                p50PagesPerSec: deriveP50(psVals),
                p50TcpRetrans: deriveP50(trVals),
                p50DiskQueue: deriveP50(dqVals),
            });

            // Fleet session aggregates
            const totalActive = s.reduce((a, sv) => a + (sv.sessions_active ?? sv.sessions ?? 0), 0);
            const totalDisconnected = s.reduce((a, sv) => a + (sv.sessions_disconnected ?? 0), 0);
            const totalAll = totalActive + totalDisconnected;
            const totalMax = s.reduce((a, sv) => a + (sv.max_sessions ?? 0), 0);
            const scpuVals = perfSvs.map((sv) => sv.perf.session_cpu_p95 ?? null).filter((v) => v != null);
            const smemVals = perfSvs.map((sv) => sv.perf.session_mem_p95 ?? null).filter((v) => v != null);
            const scpuP50Vals = perfSvs.map((sv) => sv.perf.session_cpu_p50 ?? null).filter((v) => v != null);
            const smemP50Vals = perfSvs.map((sv) => sv.perf.session_mem_p50 ?? null).filter((v) => v != null);
            appendSessionSample({
                ts,
                active: totalActive,
                disconnected: totalDisconnected,
                total: totalAll,
                utilization: totalMax > 0 ? Math.round((totalAll / totalMax) * 100) : 0,
                sessionCpuP95: deriveP95(/** @type {number[]} */ (scpuVals)),
                sessionMemP95: deriveP95(/** @type {number[]} */ (smemVals)),
                sessionCpuP50: deriveP50(/** @type {number[]} */ (scpuP50Vals)),
                sessionMemP50: deriveP50(/** @type {number[]} */ (smemP50Vals)),
            });

            // Fleet RemoteFX aggregates
            const rfxSvs = perfSvs.filter((sv) => sv.perf.rfx_available);
            appState.rfxAvailable = rfxSvs.length > 0;
            if (rfxSvs.length > 0) {
                appendRfxSample({
                    ts,
                    fpsOut: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_fps_out ?? 0)),
                    encodeMs: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_encode_ms ?? 0)),
                    quality: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_quality ?? 0)),
                    rtt: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_rtt ?? 0)),
                    loss: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_loss ?? 0)),
                    skipServer: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_skip_server ?? 0)),
                    skipNet: deriveP95(rfxSvs.map((sv) => sv.perf.rfx_skip_net ?? 0)),
                    fpsOutP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_fps_out_p50 ?? 0)),
                    encodeMsP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_encode_ms_p50 ?? 0)),
                    qualityP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_quality_p50 ?? 0)),
                    rttP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_rtt_p50 ?? 0)),
                    lossP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_loss_p50 ?? 0)),
                    skipServerP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_skip_server_p50 ?? 0)),
                    skipNetP50: deriveP50(rfxSvs.map((sv) => sv.perf.rfx_skip_net_p50 ?? 0)),
                });
            }

            // Per-server ring buffers for per-host sparklines in ServerDetail.
            for (const sv of s) {
                if (sv.perf) {
                    const svMemPct =
                        sv.perf.mem_total_mb > 0 ? (1 - sv.perf.mem_avail_mb / sv.perf.mem_total_mb) * 100 : 0;
                    appendServerMetricsSample(sv.host, {
                        time: ts,
                        cpu: sv.perf.cpu_pct,
                        mem: svMemPct,
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
                time: new Date().toLocaleTimeString('en-US', {
                    hour: '2-digit',
                    minute: '2-digit',
                    second: '2-digit',
                    hour12: false,
                }),
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

    // On mount, check for an existing valid session. If the cookie is still live
    // the user goes straight to the dashboard; otherwise the login page shows
    // immediately — no Negotiate handshake, no Windows popup.
    $effect(() => {
        untrack(() => checkSession());
    });

    // Poll for fresh data every 30 seconds — only while authenticated.
    // Cleanup cancels the interval when the user logs out or the session expires.
    $effect(() => {
        if (!authState.username) return;
        untrack(() => refresh());
        const interval = setInterval(refresh, 30_000);
        return () => clearInterval(interval);
    });

    // SSE: real-time event stream — supplements polling with instant updates.
    // EventSource auto-reconnects on network errors (~3s default retry).
    $effect(() => {
        if (!authState.username) return;

        const es = new EventSource('/api/v1/events');

        es.onmessage = (e) => {
            try {
                const event = JSON.parse(e.data);
                if (event.type === 'server_update' && event.host && event.data) {
                    appState.handleSSEServerUpdate(event.host, event.data);
                } else if (event.type === 'settings_update' && event.data) {
                    appState.config = event.data;
                }
            } catch { /* ignore malformed events */ }
        };

        es.onerror = () => {
            // EventSource auto-reconnects. If the session expired,
            // the next poll will catch the 401 and clear auth.
        };

        return () => es.close();
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

    <Footer onrefresh={refresh} />

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
