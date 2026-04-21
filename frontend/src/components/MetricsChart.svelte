<script>
    import { fly } from 'svelte/transition';
    import { LayerCake, Svg } from 'layercake';
    import { appState } from '../lib/state.svelte.js';
    import { resolveThresholds } from '../lib/thresholds.js';
    import { fetchFleetMetrics } from '../lib/api.js';
    import DualAxisChart from './chart/DualAxisChart.svelte';
    import HealthIndicatorChart from './chart/MiniHealthChart.svelte';
    import {
        Gauge,
        Timer,
        FileText,
        Network,
        HardDrive,
        Cpu,
        MemoryStick,
        Users,
        Monitor,
        Activity,
        Grid2x2,
        Rows3,
        HelpCircle,
    } from 'lucide-svelte';

    // ── Help text visibility toggles (collapsed by default) ──────────────────
    let showLoadHelp = $state(false);
    let showHicHelp = $state(false);
    let showSessionHelp = $state(false);
    let showRfxHelp = $state(false);

    // ── Upper chart (LOAD): CPU %, CPU P95, Memory %, Sessions ────────────────
    let showCpu = $state(true);
    let showCpuP95 = $state(false);
    let showMem = $state(true);
    let showSessions = $state(true);

    const LOAD_SERIES = [
        {
            key: 'cpu',
            label: 'CPU %',
            color: 'var(--color-accent)',
            axis: 'left',
            lineOnly: false,
            show: () => showCpu,
            toggle: () => {
                showCpu = !showCpu;
            },
        },
        {
            key: 'cpuP95',
            label: 'CPU P95',
            color: 'var(--color-amber)',
            axis: 'left',
            lineOnly: true,
            show: () => showCpuP95,
            toggle: () => {
                showCpuP95 = !showCpuP95;
            },
        },
        {
            key: 'mem',
            label: 'Memory %',
            color: 'var(--color-green)',
            axis: 'left',
            lineOnly: false,
            show: () => showMem,
            toggle: () => {
                showMem = !showMem;
            },
        },
        {
            key: 'sessions',
            label: 'Sessions',
            color: 'var(--color-red)',
            axis: 'right',
            lineOnly: true,
            show: () => showSessions,
            toggle: () => {
                showSessions = !showSessions;
            },
        },
    ];

    // ── Health Indicators: 4 full-size charts ────────────────────────────────
    // inputDelay thresholds come from the alert sensitivity config.
    // Pages/sec, TCP Retrans, Disk Queue use sensible hardcoded defaults.

    /** @type {Array<{key:string,p50Key:string,label:string,unit:string,thresholds:{warn:number,crit:number},color:string,fmt:(v:number)=>string,icon:import('svelte').Component}>} */
    const HIC_CHARTS = [
        {
            key: 'inputDelay',
            p50Key: 'p50InputDelay',
            label: 'Input Delay',
            unit: 'ms',
            thresholds: { warn: 50, crit: 100 },
            color: 'var(--color-amber)',
            fmt: (v) => `${Math.round(v)}ms`,
            icon: Timer,
            helpText:
                'Longest time between a keypress or click and the screen updating, sampled each second. Reports the worst delay per session, not the average — one slow input in the interval sets the value. The fleet P95 shows the 5% of servers where users feel it most.',
        },
        {
            key: 'pagesPerSec',
            p50Key: 'p50PagesPerSec',
            label: 'Pages/sec',
            unit: '/sec',
            thresholds: { warn: 80, crit: 150 },
            color: 'var(--color-accent)',
            fmt: (v) => `${Math.round(v)}/s`,
            icon: FileText,
            helpText:
                'Pages read from or written to disk each second. Includes pagefile activity, memory-mapped files, and prefetch. Not all page I/O is bad — correlate with available memory to tell pressure from routine file-backed reads.',
        },
        {
            key: 'tcpRetrans',
            p50Key: 'p50TcpRetrans',
            label: 'TCP Retrans',
            unit: '/sec',
            thresholds: { warn: 10, crit: 25 },
            color: 'var(--color-red)',
            fmt: (v) => `${v.toFixed(1)}/s`,
            icon: Network,
            helpText:
                'TCP segments the OS had to send again each second. A non-zero baseline is normal on busy networks; watch for sudden spikes or sustained climbs, which signal congestion or link-layer issues.',
        },
        {
            key: 'diskQueue',
            p50Key: 'p50DiskQueue',
            label: 'Avg Disk Queue',
            unit: '',
            thresholds: { warn: 2, crit: 5 },
            color: 'var(--color-green)',
            fmt: (v) => v.toFixed(2),
            icon: HardDrive,
            helpText:
                'How many I/O requests are waiting for disk at any moment. Reflects the interplay of storage speed, memory pressure, CPU scheduling, and workload mix — a spike here rarely has a single cause.',
        },
    ];

    // ── Config-derived thresholds ─────────────────────────────────────────────
    let perfCfg = $derived(appState.config?.performance ?? null);
    let inputDelayThresh = $derived(resolveThresholds('inputDelay', perfCfg));
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));

    // ── Fleet retained-history fetch ──────────────────────────────────────────
    /** @type {import('../lib/api.js').MetricsResponse|null} */
    let fleetResponse = $state(null);
    let fleetLoading = $state(false);
    let fleetError = $state(false);

    $effect(() => {
        const windowMs = appState.overviewWindowMs;
        let cancelled = false;
        fleetLoading = true;
        fleetError = false;
        const now = new Date();
        const from = new Date(now.getTime() - windowMs);
        fetchFleetMetrics(from, now)
            .then((data) => {
                if (!cancelled) {
                    fleetResponse = data;
                    fleetLoading = false;
                }
            })
            .catch(() => {
                if (!cancelled) {
                    fleetError = true;
                    fleetLoading = false;
                }
            });
        return () => {
            cancelled = true;
        };
    });

    /**
     * Adapt fleet series parallel arrays to MetricsSample[] for LOAD and HIC consumption.
     * Uses cpu_pct timestamps as the canonical index; other counters fill in by position.
     * @param {Record<string, {t: number[], avg: number[], min: number[], max: number[]}>} series
     * @returns {import('../lib/state.svelte.js').MetricsSample[]}
     */
    function adaptFleetToMetricsSamples(series) {
        const cpu = series['cpu_pct'];
        if (!cpu || cpu.t.length === 0) return [];
        const avail = series['mem_avail_mb']?.avg ?? [];
        const total = series['mem_total_mb']?.avg ?? [];
        const sess = series['sessions_total']?.avg ?? [];
        const id = series['input_delay_p95_ms']?.avg ?? [];
        const ps = series['pages_sec']?.avg ?? [];
        const tr = series['tcp_retrans_sec']?.avg ?? [];
        const dq = series['disk_queue']?.avg ?? [];
        return cpu.t.map((ts, i) => {
            const a = avail[i] ?? 0;
            const m = total[i] ?? 0;
            return {
                time: ts,
                cpu: cpu.avg[i] ?? 0,
                cpuP95: cpu.max[i] ?? cpu.avg[i] ?? 0,
                mem: m > 0 ? (1 - a / m) * 100 : 0,
                sessions: Math.round(sess[i] ?? 0),
                inputDelay: id[i] ?? 0,
                pagesPerSec: ps[i] ?? 0,
                tcpRetrans: tr[i] ?? 0,
                diskQueue: dq[i] ?? 0,
                p50InputDelay: 0,
                p50PagesPerSec: 0,
                p50TcpRetrans: 0,
                p50DiskQueue: 0,
            };
        });
    }

    let history = $derived(adaptFleetToMetricsSamples(fleetResponse?.series ?? {}));
    let sessionMax = $derived(Math.max(...history.map((h) => h.sessions ?? 0), 1));
    let hasRight = $derived(showSessions);

    // LOAD chart — normalise to 0–100; raw values carried for tooltip
    let loadNormData = $derived(
        history.map((h, i) => ({
            i,
            time: h.time,
            cpu: Math.min(h.cpu ?? 0, 100),
            cpuP95: Math.min(h.cpuP95 ?? h.cpu ?? 0, 100),
            mem: Math.min(h.mem ?? 0, 100),
            sessions: ((h.sessions ?? 0) / sessionMax) * 100,
            raw: {
                cpu: +(h.cpu ?? 0).toFixed(1),
                cpuP95: +(h.cpuP95 ?? h.cpu ?? 0).toFixed(1),
                mem: +(h.mem ?? 0).toFixed(1),
                sessions: h.sessions ?? 0,
            },
        })),
    );

    // Right-axis tick labels for sessions scale
    let rightTicks = $derived(
        showSessions
            ? [0, 0.25, 0.5, 0.75, 1].map((f) => ({
                  pct: f * 100,
                  label: Math.round(f * sessionMax).toString(),
              }))
            : [],
    );

    let loadVisible = $derived({
        cpu: showCpu,
        cpuP95: showCpuP95,
        mem: showMem,
        sessions: showSessions,
    });

    // ── LOAD current values — show pinned/hovered point, or latest
    let activeIdx = $derived(appState.pinnedChartIndex ?? appState.hoveredChartIndex);
    let displayPoint = $derived(
        activeIdx !== null ? (history[activeIdx] ?? history[history.length - 1]) : history[history.length - 1],
    );
    let loadCurrents = $derived([
        {
            label: 'CPU',
            value: displayPoint ? `${(+displayPoint.cpu).toFixed(1)}%` : '—',
            color: 'var(--color-accent)',
            icon: Cpu,
            show: () => showCpu,
        },
        {
            label: 'CPU P95',
            value: displayPoint ? `${(+(displayPoint.cpuP95 ?? displayPoint.cpu)).toFixed(1)}%` : '—',
            color: 'var(--color-amber)',
            icon: Cpu,
            show: () => showCpuP95,
        },
        {
            label: 'MEM',
            value: displayPoint ? `${(+displayPoint.mem).toFixed(1)}%` : '—',
            color: 'var(--color-green)',
            icon: MemoryStick,
            show: () => showMem,
        },
        {
            label: 'SESS',
            value: displayPoint ? `${displayPoint.sessions ?? 0}` : '—',
            color: 'var(--color-red)',
            icon: Users,
            show: () => showSessions,
        },
    ]);

    // ── Session Metrics charts ────────────────────────────────────────────────

    /**
     * Adapt fleet series to SessionSample[] for the SESSIONS sub-tab.
     * @param {Record<string, {t: number[], avg: number[], min: number[], max: number[]}>} series
     * @returns {import('../lib/state.svelte.js').SessionSample[]}
     */
    function adaptFleetToSessionSamples(series) {
        const tot = series['sessions_total'];
        if (!tot || tot.t.length === 0) return [];
        const active = series['sessions_active']?.avg ?? [];
        const disc = series['sessions_disconnected']?.avg ?? [];
        const max = series['sessions_max']?.avg ?? [];
        const scpu = series['session_cpu_p95_pct']?.max ?? [];
        const smem = series['session_mem_p95_bytes']?.max ?? [];
        return tot.t.map((ts, i) => {
            const a = Math.round(active[i] ?? 0);
            const d = Math.round(disc[i] ?? 0);
            const t = Math.round(tot.avg[i] ?? 0);
            const mx = Math.round(max[i] ?? 0);
            return {
                ts,
                active: a,
                disconnected: d,
                total: t,
                utilization: mx > 0 ? Math.round((t / mx) * 100) : 0,
                sessionCpuP95: scpu[i] ?? 0,
                sessionMemP95: smem[i] ?? 0,
                sessionCpuP50: 0,
                sessionMemP50: 0,
            };
        });
    }

    let sessionHistory = $derived(adaptFleetToSessionSamples(fleetResponse?.series ?? {}));

    /**
     * Adapt fleet series to RfxSample[] for the REMOTEFX sub-tab.
     * @param {Record<string, {t: number[], avg: number[], min: number[], max: number[]}>} series
     * @returns {import('../lib/state.svelte.js').RfxSample[]}
     */
    function adaptFleetToRfxSamples(series) {
        const fps = series['rfx_fps_out'];
        if (!fps || fps.t.length === 0) return [];
        const fpsP50 = series['rfx_fps_out_p50']?.avg ?? [];
        const enc = series['rfx_encode_ms']?.avg ?? [];
        const qual = series['rfx_quality_pct']?.avg ?? [];
        const skipSrv = series['rfx_skip_server_sec']?.avg ?? [];
        const skipNet = series['rfx_skip_net_sec']?.avg ?? [];
        const rtt = series['rfx_rtt_ms']?.avg ?? [];
        const loss = series['rfx_loss_pct']?.avg ?? [];
        return fps.t.map((ts, i) => ({
            ts,
            fpsOut: fps.avg[i] ?? 0,
            fpsOutP50: fpsP50[i] ?? 0,
            encodeMs: enc[i] ?? 0,
            encodeMsP50: 0,
            quality: qual[i] ?? 0,
            qualityP50: 0,
            skipServer: skipSrv[i] ?? 0,
            skipServerP50: 0,
            skipNet: skipNet[i] ?? 0,
            skipNetP50: 0,
            rtt: rtt[i] ?? 0,
            rttP50: 0,
            loss: loss[i] ?? 0,
            lossP50: 0,
        }));
    }

    // ── RemoteFX charts ───────────────────────────────────────────────────────
    let rfxHistory = $derived(adaptFleetToRfxSamples(fleetResponse?.series ?? {}));
    let rfxAvailable = $derived((fleetResponse?.series?.['rfx_fps_out']?.t?.length ?? 0) > 0);

    /** Stable identity transform — avoids allocating a new function on every render. */
    const IDENTITY = (/** @type {number} */ v) => v;

    // Order: Sessions Trend → Utilization → CPU → Memory
    const SESSION_CHARTS = [
        {
            key: 'total',
            p50Key: 'active',
            label: 'Sessions Trend',
            unit: '',
            thresholds: { warn: 0, crit: 0 },
            color: 'var(--color-green)',
            fmt: /** @param {number} v */ (v) => Math.round(v).toString(),
            icon: Users,
            timeKey: 'ts',
            valueLabel: 'Total',
            p50Label: 'Active',
            noThresholdZones: true,
            autoScale: true,
            helpText:
                'Logged-in users vs everyone still holding a session (including disconnected). The gap between the two lines is your ghost sessions — users who left but whose processes and memory are still allocated.',
        },
        {
            key: 'utilization',
            label: 'Utilization',
            unit: '%',
            thresholds: { warn: 80, crit: 90 },
            color: 'var(--color-red)',
            fmt: /** @param {number} v */ (v) => `${v.toFixed(1)}%`,
            icon: Gauge,
            timeKey: 'ts',
            valueLabel: 'Fleet',
            helpText:
                "How full is the farm? Total sessions divided by the sum of every server's MaxSessions limit. Tracks how close the fleet is to turning users away.",
        },
        {
            key: 'sessionCpuP95',
            p50Key: 'sessionCpuP50',
            label: 'Session CPU',
            unit: '%',
            thresholds: { warn: 15, crit: 30 },
            color: 'var(--color-accent)',
            fmt: /** @param {number} v */ (v) => `${v.toFixed(1)}%`,
            icon: Cpu,
            timeKey: 'ts',
            helpText:
                'CPU eaten by individual sessions, aggregated across the fleet. P95 catches the power users and runaway processes; P50 is what a normal session looks like. A wide gap between the two means a few sessions are doing most of the work.',
        },
        {
            key: 'sessionMemP95',
            p50Key: 'sessionMemP50',
            label: 'Session Memory',
            unit: '',
            thresholds: { warn: 500, crit: 800 },
            color: 'var(--color-amber)',
            fmt: /** @param {number} v */ (v) => (v >= 1024 ? `${(v / 1024).toFixed(1)} GB` : `${Math.round(v)} MB`),
            fmtYTick: /** @param {number} v */ (v) =>
                v === 0 ? '0' : v >= 1024 ? `${(v / 1024).toFixed(1)}G` : `${Math.round(v)}M`,
            icon: MemoryStick,
            timeKey: 'ts',
            transform: /** @param {number} v */ (v) => v / (1024 * 1024),
            helpText:
                'Working set memory claimed by each session. P95 spots the memory-hungry outliers (think Chrome with 40 tabs); P50 is the typical user. If P50 creeps up over hours, applications may be leaking.',
        },
    ];

    // Combine server + network frame skips into a single field (both P95 and P50).
    let rfxHistoryProcessed = $derived(
        rfxHistory.map((h) => ({
            ...h,
            skipTotal: (h.skipServer ?? 0) + (h.skipNet ?? 0),
            skipTotalP50: (h.skipServerP50 ?? 0) + (h.skipNetP50 ?? 0),
        })),
    );

    const RFX_CHARTS = [
        {
            key: 'fpsOut',
            p50Key: 'fpsOutP50',
            label: 'FPS Output',
            unit: 'fps',
            thresholds: { warn: 20, crit: 10 },
            color: 'var(--color-green)',
            fmt: /** @param {number} v */ (v) => `${Math.round(v)}fps`,
            icon: Monitor,
            timeKey: 'ts',
            invertThresholds: true,
            helpText:
                'Frames actually delivered to clients each second. When this drops below the source frame rate, the gap is frames being skipped. P95 shows the worst-performing sessions; P50 shows what a typical session receives. Inverted threshold — lower is worse.',
        },
        {
            key: 'encodeMs',
            p50Key: 'encodeMsP50',
            label: 'Encode Time',
            unit: 'ms',
            thresholds: { warn: 30, crit: 50 },
            color: 'var(--color-amber)',
            fmt: /** @param {number} v */ (v) => `${v.toFixed(1)}ms`,
            icon: Timer,
            timeKey: 'ts',
            helpText:
                'How long the GPU spends encoding each frame before sending it. Encoding is synchronous — anything above 33ms physically caps the session below 30fps. P95 catches sessions under the heaviest load; P50 shows the median encode cost across the fleet.',
        },
        {
            key: 'quality',
            p50Key: 'qualityP50',
            label: 'Frame Quality',
            unit: '%',
            thresholds: { warn: 70, crit: 50 },
            color: 'var(--color-accent)',
            fmt: /** @param {number} v */ (v) => `${Math.round(v)}%`,
            icon: Activity,
            timeKey: 'ts',
            invertThresholds: true,
            helpText:
                'How much fidelity survives compression — 100% means pixel-perfect. Quality drops during fast motion, low bandwidth, or heavy server load. P95 is the worst-affected sessions; P50 is what users typically see. Inverted threshold — lower is worse.',
        },
        {
            key: 'skipTotal',
            p50Key: 'skipTotalP50',
            label: 'Frames Skipped',
            unit: '/sec',
            thresholds: { warn: 5, crit: 15 },
            color: 'var(--color-amber)',
            fmt: /** @param {number} v */ (v) => `${v.toFixed(1)}/s`,
            icon: Activity,
            timeKey: 'ts',
            helpText:
                'Frames that never made it to the client, per second. Windows tracks three skip sources — server (GPU/CPU), network (bandwidth), and client (decoding). This chart sums server + network. P50 shows the typical skip rate; P95 shows the worst-affected sessions.',
        },
        {
            key: 'rtt',
            p50Key: 'rttP50',
            label: 'TCP RTT',
            unit: 'ms',
            thresholds: { warn: 50, crit: 100 },
            color: 'var(--color-red)',
            fmt: /** @param {number} v */ (v) => `${Math.round(v)}ms`,
            icon: Network,
            timeKey: 'ts',
            helpText:
                'Network round-trip time between server and client. Measured on the TCP channel — may not reflect actual latency if the session is using UDP transport. P95 catches the worst-connected users; P50 shows median network conditions across the fleet.',
        },
        {
            key: 'loss',
            p50Key: 'lossP50',
            label: 'Loss Rate',
            unit: '%',
            thresholds: { warn: 2, crit: 5 },
            color: 'var(--color-accent)',
            fmt: /** @param {number} v */ (v) => `${v.toFixed(2)}%`,
            icon: Network,
            timeKey: 'ts',
            helpText:
                'Percentage of packets lost in transit on the active RDP transport. RDP prefers UDP (where losses are recovered via forward error correction) and falls back to TCP (where losses trigger retransmission and congestion backoff). P50 is the typical loss rate; P95 shows the most affected sessions.',
        },
    ];

    let loadContainerW = $state(0);
    let gridLayout = $state(true); // true = 2-col grid, false = 1-col stack

    // ── Sub-tab state ─────────────────────────────────────────────────────
    let activeTab = $derived(appState.overviewSubTab);

    // If RemoteFX becomes unavailable while on that tab, reset to performance.
    $effect(() => {
        if (!rfxAvailable && appState.overviewSubTab === 'remotefx') {
            appState.overviewSubTab = 'performance';
        }
    });

    // ── Mobile detection for HIC axis placement ────────────────────────────
    let isMobile = $state(false);
    $effect(() => {
        const mq = window.matchMedia('(max-width: 760px)');
        isMobile = mq.matches;
        const handler = (/** @type {MediaQueryListEvent} */ e) => {
            isMobile = e.matches;
        };
        mq.addEventListener('change', handler);
        return () => mq.removeEventListener('change', handler);
    });

    const Y_DOMAIN = [0, 100];
    let lcData = $derived(history.map((_, i) => ({ x: i, y: 50 })));
</script>

<!-- ── Sub-tab bar ── -->
<div class="subtab-bar">
    <button
        class="subtab"
        class:active={activeTab === 'performance'}
        onclick={() => {
            appState.overviewSubTab = 'performance';
        }}
    >
        <Activity size={12} strokeWidth={2.4} />
        PERFORMANCE
    </button>
    <button
        class="subtab"
        class:active={activeTab === 'sessions'}
        onclick={() => {
            appState.overviewSubTab = 'sessions';
        }}
    >
        <Users size={12} strokeWidth={2.4} />
        SESSIONS
    </button>
    {#if rfxAvailable}
        <button
            class="subtab"
            class:active={activeTab === 'remotefx'}
            onclick={() => {
                appState.overviewSubTab = 'remotefx';
            }}
        >
            <Monitor size={12} strokeWidth={2.4} />
            REMOTEFX
        </button>
    {/if}
    <button
        class="layout-toggle btn-brutal"
        onclick={() => {
            gridLayout = !gridLayout;
        }}
        aria-label={gridLayout ? 'Switch to single column' : 'Switch to grid'}
    >
        {#if gridLayout}
            <Rows3 size={13} strokeWidth={2.2} />
        {:else}
            <Grid2x2 size={13} strokeWidth={2.2} />
        {/if}
    </button>
</div>

{#key activeTab}
    <div in:fly={{ y: 12, duration: 120, delay: 60 }} out:fly={{ y: -6, duration: 80 }}>
        <!-- ── LOAD chart card ── -->
        {#if activeTab === 'performance'}
            <div class="chart-wrap">
                <div class="chart-card">
                    <div class="load-top">
                        <div class="sub-label">
                            <Gauge size={12} strokeWidth={2.4} /> LOAD
                            <span class="sub-label-note">· Average across fleet</span>
                            <button
                                class="help-toggle"
                                class:active={showLoadHelp}
                                onclick={() => (showLoadHelp = !showLoadHelp)}
                                aria-label="Toggle help text"
                                aria-pressed={showLoadHelp}
                            >
                                <HelpCircle size={11} strokeWidth={2.2} />
                            </button>
                        </div>
                        {#if showLoadHelp}
                            <p class="chart-desc">
                                Fleet-average CPU and memory utilization with total connected sessions. CPU is averaged
                                across all cores on all hosts; memory is the percentage of physical RAM in use. The
                                Sessions line (right axis) tracks how many users are connected fleet-wide — rising
                                sessions with flat CPU/memory means headroom; rising CPU/memory with flat sessions means
                                per-user cost is climbing.
                            </p>
                        {/if}
                    </div>
                    <div class="chart-toggles-stacked">
                        {#each LOAD_SERIES as s}
                            <button
                                class="chart-toggle"
                                class:active={s.show()}
                                style="--sc: {s.color}"
                                aria-pressed={s.show()}
                                onclick={s.toggle}
                            >
                                {#if s.lineOnly}
                                    <span class="t-dash" aria-hidden="true"></span>
                                {:else}
                                    <span class="t-dot" aria-hidden="true"></span>
                                {/if}
                                {s.label}
                                {#if s.axis === 'right'}<span class="t-axis">R</span>{/if}
                            </button>
                        {/each}
                    </div>
                    <div class="chart-panel">
                        <div class="load-chart-header">
                            <div></div>
                            <div class="load-currents" style="padding-right: {hasRight ? 64 : 16}px">
                                {#each loadCurrents as lc}
                                    {#if lc.show()}
                                        {@const Icon = lc.icon}
                                        <span class="load-val">
                                            <Icon size={11} strokeWidth={2.2} />
                                            <span class="load-val-label">{lc.label}</span>
                                            <span class="load-val-num" style="color: {lc.color}">{lc.value}</span>
                                        </span>
                                    {/if}
                                {/each}
                            </div>
                        </div>
                        <div class="chart-body upper-chart" bind:clientWidth={loadContainerW}>
                            {#if fleetLoading}
                                <div class="chart-placeholder">Loading retained history…</div>
                            {:else if fleetError}
                                <div class="chart-placeholder chart-error">Unable to reach the metrics endpoint</div>
                            {:else if history.length < 2}
                                <div class="chart-placeholder">No retained history for this window</div>
                            {:else if loadContainerW > 0}
                                <LayerCake
                                    data={lcData}
                                    x="x"
                                    y="y"
                                    yDomain={Y_DOMAIN}
                                    padding={{ top: 16, right: hasRight ? 64 : 16, bottom: 24, left: 48 }}
                                >
                                    <Svg>
                                        <DualAxisChart
                                            normData={loadNormData}
                                            SERIES={LOAD_SERIES}
                                            {rightTicks}
                                            thresholds={[
                                                {
                                                    pct: cpuThresh.warn,
                                                    opacity: 0.25,
                                                    label: 'CPU WARN',
                                                    show: () => showCpu,
                                                },
                                                {
                                                    pct: cpuThresh.crit,
                                                    opacity: 0.35,
                                                    label: 'CPU CRIT',
                                                    show: () => showCpu,
                                                },
                                                {
                                                    pct: memThresh.warn,
                                                    opacity: 0.25,
                                                    label: 'MEM WARN',
                                                    show: () => showMem,
                                                },
                                                {
                                                    pct: memThresh.crit,
                                                    opacity: 0.35,
                                                    label: 'MEM CRIT',
                                                    show: () => showMem,
                                                },
                                            ]}
                                            {history}
                                            visible={loadVisible}
                                            showXAxis={true}
                                        />
                                    </Svg>
                                </LayerCake>
                            {/if}
                        </div>
                    </div>
                </div>
            </div>

            <!-- ── Health Indicators card ── -->
            <div class="chart-wrap">
                <div class="chart-card hic-section">
                    <div class="sub-label">
                        <Gauge size={12} strokeWidth={2.4} /> HEALTH INDICATORS
                        <span class="sub-label-note">· P95 across fleet</span>
                        <button
                            class="help-toggle"
                            class:active={showHicHelp}
                            onclick={() => (showHicHelp = !showHicHelp)}
                            aria-label="Toggle help text"
                            aria-pressed={showHicHelp}
                        >
                            <HelpCircle size={11} strokeWidth={2.2} />
                        </button>
                    </div>
                    {#if showHicHelp}
                        <p class="chart-desc">
                            P95 health indicators across the fleet — input responsiveness, memory pressure, network
                            reliability, and storage I/O. P95 highlights the worst-performing 5% of servers; P50 shows the
                            median.
                        </p>
                    {/if}

                    {#if fleetLoading}
                        <div class="chart-placeholder">Loading retained history…</div>
                    {:else if fleetError}
                        <div class="chart-placeholder chart-error">Unable to reach the metrics endpoint</div>
                    {:else if history.length === 0}
                        <div class="chart-placeholder">No retained history for this window</div>
                    {:else}
                        <div class="hic-grid {gridLayout ? '' : 'single-col'}">
                            {#each HIC_CHARTS as mc, i}
                                <HealthIndicatorChart
                                    {history}
                                    valueKey={mc.key}
                                    p50Key={mc.p50Key}
                                    label={mc.label}
                                    unit={mc.unit}
                                    thresholds={mc.key === 'inputDelay' ? inputDelayThresh : mc.thresholds}
                                    color={mc.color}
                                    fmt={mc.fmt}
                                    icon={mc.icon}
                                    axisRight={i % 2 === 1 && !isMobile && gridLayout}
                                    helpText={mc.helpText ?? ''}
                                    showHelp={showHicHelp}
                                />
                            {/each}
                        </div>
                    {/if}
                </div>
            </div>
        {/if}

        <!-- ── Session Metrics card ── -->
        {#if activeTab === 'sessions'}
            <div class="chart-wrap">
                <div class="chart-card hic-section">
                    <div class="sub-label">
                        <Users size={12} strokeWidth={2.4} /> SESSION METRICS
                        <span class="sub-label-note">· Fleet overview</span>
                        <button
                            class="help-toggle"
                            class:active={showSessionHelp}
                            onclick={() => (showSessionHelp = !showSessionHelp)}
                            aria-label="Toggle help text"
                            aria-pressed={showSessionHelp}
                        >
                            <HelpCircle size={11} strokeWidth={2.2} />
                        </button>
                    </div>
                    {#if showSessionHelp}
                        <p class="chart-desc">
                            How many sessions are running, how full the farm is, and what each session costs in CPU and
                            memory. The gap between P95 and P50 tells you how much spread there is between your heaviest
                            users and everyone else.
                        </p>
                    {/if}

                    {#if fleetLoading}
                        <div class="chart-placeholder">Loading retained history…</div>
                    {:else if fleetError}
                        <div class="chart-placeholder chart-error">Unable to reach the metrics endpoint</div>
                    {:else if sessionHistory.length === 0}
                        <div class="chart-placeholder">No retained history for this window</div>
                    {:else}
                        <div class="hic-grid {gridLayout ? '' : 'single-col'}">
                            {#each SESSION_CHARTS as mc, i}
                                <HealthIndicatorChart
                                    history={sessionHistory}
                                    valueKey={mc.key}
                                    p50Key={mc.p50Key}
                                    label={mc.label}
                                    unit={mc.unit}
                                    thresholds={mc.thresholds}
                                    color={mc.color}
                                    fmt={mc.fmt}
                                    fmtYTick={mc.fmtYTick ?? null}
                                    icon={mc.icon}
                                    axisRight={i % 2 === 1 && !isMobile && gridLayout}
                                    timeKey={mc.timeKey ?? 'time'}
                                    valueLabel={mc.valueLabel ?? 'P95'}
                                    p50Label={mc.p50Label ?? 'P50'}
                                    noThresholdZones={mc.noThresholdZones ?? false}
                                    autoScale={mc.autoScale ?? false}
                                    transform={mc.transform ?? IDENTITY}
                                    invertThresholds={mc.invertThresholds ?? false}
                                    helpText={mc.helpText ?? ''}
                                    showHelp={showSessionHelp}
                                />
                            {/each}
                        </div>
                    {/if}
                </div>
            </div>
        {/if}

        <!-- ── RemoteFX card ── -->
        {#if activeTab === 'remotefx' && rfxAvailable}
            <div class="chart-wrap">
                <div class="chart-card hic-section">
                    <div class="sub-label">
                        <Monitor size={12} strokeWidth={2.4} /> REMOTEFX
                        <span class="sub-label-note">· Graphics & Network P95 / P50</span>
                        <button
                            class="help-toggle"
                            class:active={showRfxHelp}
                            onclick={() => (showRfxHelp = !showRfxHelp)}
                            aria-label="Toggle help text"
                            aria-pressed={showRfxHelp}
                        >
                            <HelpCircle size={11} strokeWidth={2.2} />
                        </button>
                    </div>
                    {#if showRfxHelp}
                        <p class="chart-desc">
                            What the users actually see: frame rates, encoding speed, visual quality, and the network
                            between them. P95 shows the worst-affected sessions; P50 shows what a typical user experiences.
                            The gap between them reveals how much spread there is across your fleet.
                        </p>
                    {/if}

                    {#if fleetLoading}
                        <div class="chart-placeholder">Loading retained history…</div>
                    {:else if fleetError}
                        <div class="chart-placeholder chart-error">Unable to reach the metrics endpoint</div>
                    {:else if rfxHistoryProcessed.length === 0}
                        <div class="chart-placeholder">No retained history for this window</div>
                    {:else}
                        <div class="rfx-grid {gridLayout ? '' : 'single-col'}">
                            {#each RFX_CHARTS as mc, i}
                                <HealthIndicatorChart
                                    history={rfxHistoryProcessed}
                                    valueKey={mc.key}
                                    p50Key={mc.p50Key}
                                    label={mc.label}
                                    unit={mc.unit}
                                    thresholds={mc.thresholds}
                                    color={mc.color}
                                    fmt={mc.fmt}
                                    icon={mc.icon}
                                    axisRight={i % 2 === 1 && !isMobile && gridLayout}
                                    timeKey={mc.timeKey ?? 'time'}
                                    invertThresholds={mc.invertThresholds ?? false}
                                    helpText={mc.helpText ?? ''}
                                    showHelp={showRfxHelp}
                                />
                            {/each}
                        </div>
                    {/if}
                </div>
            </div>
        {/if}
    </div>
{/key}

<style>
    .chart-wrap {
        margin-bottom: 24px;
    }

    /* ── Sub-tab bar ── */
    .subtab-bar {
        display: flex;
        align-items: center;
        gap: 0;
        margin-bottom: 16px;
        /* no bottom border */
    }

    .layout-toggle {
        margin-left: auto;
        padding: 4px 8px;
        font-size: 0;
        line-height: 0;
        background: var(--color-surface);
        color: var(--color-muted);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        margin-bottom: 4px;
    }

    .layout-toggle:hover {
        color: var(--color-fg);
    }

    .subtab {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        color: var(--color-muted);
        background: none;
        border: none;
        border-bottom: 2px solid transparent;
        padding: 6px 14px 7px;
        margin-bottom: -1.5px;
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 5px;
        transition:
            color 0.12s linear,
            border-color 0.12s linear;
        white-space: nowrap;
    }

    .subtab:hover {
        color: var(--color-fg);
    }

    .subtab.active {
        color: var(--color-accent);
        border-bottom-color: var(--color-accent);
    }

    .chart-card {
        background: var(--color-card);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        padding: 16px 18px;
    }

    /* ── LOAD header + toggles stacked vertically ── */
    .load-top {
        margin-bottom: 4px;
    }

    .load-top .sub-label {
        margin-bottom: 6px;
    }

    .load-top .chart-desc {
        margin-bottom: 0;
    }

    .chart-toggles-stacked {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
        margin: 12px 0 0;
    }

    /* ── LOAD current values — row above chart, right-aligned ── */
    .load-chart-header {
        display: flex;
        align-items: flex-end;
        justify-content: space-between;
        margin-bottom: 4px;
    }

    .load-currents {
        display: flex;
        align-items: center;
        gap: 12px;
    }

    .load-val {
        display: flex;
        align-items: center;
        gap: 4px;
        font-family: 'JetBrains Mono', monospace;
        color: var(--color-fg);
    }

    .load-val-label {
        font-size: 0.55rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        color: var(--color-muted);
    }

    .load-val-num {
        font-size: 1.05rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        line-height: 1;
    }

    /* Small sub-heading above each chart section */
    .sub-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.14em;
        text-transform: uppercase;
        color: var(--color-muted);
        margin-bottom: 10px;
        opacity: 0.65;
        display: flex;
        align-items: center;
        gap: 5px;
    }

    .sub-label-note {
        font-weight: 400;
        letter-spacing: 0.08em;
        opacity: 0.7;
    }

    .help-toggle {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        padding: 2px 4px;
        margin-left: 4px;
        background: none;
        border: 1px solid transparent;
        border-radius: 3px;
        color: var(--color-muted);
        opacity: 0.45;
        cursor: pointer;
        line-height: 0;
        transition:
            opacity 0.1s linear,
            color 0.1s linear,
            border-color 0.1s linear;
    }

    .help-toggle:hover {
        opacity: 0.9;
        color: var(--color-fg);
        border-color: var(--color-border);
    }

    .help-toggle.active {
        opacity: 1;
        color: var(--color-accent);
        border-color: var(--color-accent);
    }

    .chart-desc {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.72rem;
        line-height: 1.55;
        color: var(--color-muted);
        margin: 0 0 14px 0;
        max-width: 700px;
    }

    .chart-panel {
        display: flex;
        flex-direction: column;
    }

    /* ── Toggle buttons — neobrutalist ── */
    .chart-toggle {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.62rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        padding: 5px 10px;
        border-radius: var(--radius-default);
        border: var(--spacing-bw) solid var(--color-border);
        box-shadow: 2px 2px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-fg);
        cursor: pointer;
        display: flex;
        align-items: center;
        gap: 6px;
        transition:
            transform 0.08s linear,
            box-shadow 0.08s linear;
        white-space: nowrap;
    }

    .chart-toggle:hover {
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }

    .chart-toggle:active {
        transform: translate(2px, 2px);
        box-shadow: none;
    }

    .chart-toggle.active {
        background: var(--sc);
        color: #fff;
    }

    /* Area series indicator: solid square */
    .t-dot {
        width: 8px;
        height: 8px;
        border-radius: 1px;
        background: var(--sc);
        border: 1.5px solid color-mix(in srgb, var(--color-border) 60%, transparent);
        flex-shrink: 0;
    }

    .chart-toggle.active .t-dot {
        background: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.4);
    }

    /* Dashed line indicator (Sessions — right-axis series) */
    .t-dash {
        width: 18px;
        height: 3px;
        border-radius: 0;
        background: repeating-linear-gradient(
            to right,
            var(--sc) 0px,
            var(--sc) 7px,
            transparent 7px,
            transparent 11px
        );
        flex-shrink: 0;
    }

    .chart-toggle.active .t-dash {
        background: repeating-linear-gradient(
            to right,
            rgba(255, 255, 255, 0.9) 0px,
            rgba(255, 255, 255, 0.9) 7px,
            transparent 7px,
            transparent 11px
        );
    }

    .t-axis {
        font-size: 0.52rem;
        opacity: 0.55;
        margin-left: -2px;
    }

    /* ── Load chart area ── */
    .chart-body {
        position: relative;
    }

    .upper-chart {
        height: 240px;
    }

    .chart-placeholder {
        display: flex;
        align-items: center;
        justify-content: center;
        height: 100%;
        min-height: 80px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.8rem;
        color: var(--color-muted);
    }

    .chart-placeholder.chart-error {
        color: var(--color-red);
    }

    /* ── Health Indicators section ── */
    .hic-section {
        padding: 16px 18px 18px;
    }

    /* 2 × 2 grid of full-size indicator cards */
    .hic-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 16px;
    }

    .hic-grid.single-col,
    .rfx-grid.single-col {
        grid-template-columns: 1fr;
    }

    @media (max-width: 760px) {
        .hic-grid {
            grid-template-columns: 1fr;
        }
    }

    /* ── RemoteFX 2×3 grid ── */
    .rfx-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 16px;
    }

    @media (max-width: 760px) {
        .rfx-grid {
            grid-template-columns: 1fr;
        }
    }
</style>
