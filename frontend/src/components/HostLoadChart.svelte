<script>
    /**
     * HostLoadChart — per-host equivalent of MetricsChart's LOAD panel.
     *
     * Renders a single dual-axis chart carrying CPU, CPU P95, Memory, and
     * Sessions for one host, with window presets (15M/1H/1D/3D/5D),
     * drag-pan, wheel-zoom, series toggles, and a current-value readout.
     * Mirrors the composition of Overview's LOAD chart so operators see
     * the same visual grammar on the Server Detail drawer.
     *
     * Fetches from /api/v1/metrics/{host} — each instance owns its fetch,
     * window, pan, and toggle state. The window preset persists per-instance
     * under a host-qualified localStorage key so an operator's preferred
     * zoom survives navigation between server cards.
     */
    import { LayerCake, Svg } from 'layercake';
    import { OVERVIEW_WINDOW_PRESETS } from '../lib/state.svelte.js';
    import { fetchMetrics } from '../lib/api.js';
    import { resolveThresholds } from '../lib/thresholds.js';
    import { appState } from '../lib/state.svelte.js';
    import DualAxisChart from './chart/DualAxisChart.svelte';
    import { Cpu, MemoryStick, Users, Gauge } from 'lucide-svelte';

    /** @type {{ host: string }} */
    let { host } = $props();

    // ── Window preset (persisted per-host) ────────────────────────────────
    const LS_KEY = 'drainctl.hostLoadChart.window';
    function readStoredWindow() {
        try {
            const raw = localStorage.getItem(LS_KEY);
            if (!raw) return null;
            return OVERVIEW_WINDOW_PRESETS.some((p) => p.key === raw) ? raw : null;
        } catch {
            return null;
        }
    }
    let windowKey = $state(readStoredWindow() ?? '1hour');
    let windowMs = $derived(OVERVIEW_WINDOW_PRESETS.find((p) => p.key === windowKey)?.ms ?? 60 * 60 * 1000);
    $effect(() => {
        try {
            localStorage.setItem(LS_KEY, windowKey);
        } catch {
            /* localStorage disabled — not fatal */
        }
    });

    // ── Series toggles ────────────────────────────────────────────────────
    let showCpu = $state(true);
    let showCpuP95 = $state(false);
    let showMem = $state(true);
    let showSessions = $state(true);

    const LOAD_SERIES = [
        { key: 'cpu', label: 'CPU %', color: 'var(--color-accent)', axis: 'left', lineOnly: false,
          show: () => showCpu, toggle: () => { showCpu = !showCpu; } },
        { key: 'cpuP95', label: 'CPU P95', color: 'var(--color-amber)', axis: 'left', lineOnly: true,
          show: () => showCpuP95, toggle: () => { showCpuP95 = !showCpuP95; } },
        { key: 'mem', label: 'Memory %', color: 'var(--color-green)', axis: 'left', lineOnly: false,
          show: () => showMem, toggle: () => { showMem = !showMem; } },
        { key: 'sessions', label: 'Sessions', color: 'var(--color-red)', axis: 'right', lineOnly: true,
          show: () => showSessions, toggle: () => { showSessions = !showSessions; } },
    ];

    // ── Config-derived thresholds ─────────────────────────────────────────
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));

    // ── Fetch + pan state ─────────────────────────────────────────────────
    /** @type {import('../lib/api.js').MetricsResponse|null} */
    let response = $state(null);
    let loading = $state(false);
    let error = $state(false);
    let panOffsetMs = $state(0);
    let isDragging = $state(false);
    let dragStartX = 0;
    let dragStartOffset = 0;
    let dragPendingOffsetMs = 0;
    let containerW = $state(0);

    // Counters needed: CPU (avg + P95), memory (derive from avail/total), sessions.
    const COUNTERS = ['cpu_pct', 'cpu_p95_pct', 'mem_avail_mb', 'mem_total_mb', 'sessions_total'];

    $effect(() => {
        // Reading these reactive values registers the effect's dependencies.
        const h = host;
        const ms = windowMs;
        const offset = panOffsetMs;
        if (!h) return;
        const ac = new AbortController();
        loading = true;
        error = false;
        const now = new Date();
        const to = new Date(now.getTime() - offset);
        const from = new Date(to.getTime() - ms);
        fetchMetrics(h, from, to, 'auto', COUNTERS, ac.signal)
            .then((data) => {
                if (!ac.signal.aborted) {
                    response = data;
                    loading = false;
                }
            })
            .catch(() => {
                if (!ac.signal.aborted) {
                    error = true;
                    loading = false;
                }
            });
        return () => {
            ac.abort();
        };
    });

    // ── Interactions ──────────────────────────────────────────────────────
    /** @param {WheelEvent} e */
    function onWheel(e) {
        e.preventDefault();
        const idx = OVERVIEW_WINDOW_PRESETS.findIndex((p) => p.key === windowKey);
        if (e.deltaY < 0 && idx > 0) {
            panOffsetMs = 0;
            windowKey = OVERVIEW_WINDOW_PRESETS[idx - 1].key;
        } else if (e.deltaY > 0 && idx < OVERVIEW_WINDOW_PRESETS.length - 1) {
            panOffsetMs = 0;
            windowKey = OVERVIEW_WINDOW_PRESETS[idx + 1].key;
        }
    }

    /** @param {MouseEvent} e */
    function onMouseDown(e) {
        if (e.button !== 0) return;
        isDragging = true;
        dragStartX = e.clientX;
        dragStartOffset = panOffsetMs;
        dragPendingOffsetMs = panOffsetMs;
        appState.hoveredChartIndex = null;
        appState.pinnedChartIndex = null;
    }

    /** @param {MouseEvent} e */
    function onMouseMove(e) {
        if (!isDragging) return;
        const dx = e.clientX - dragStartX;
        const dMs = (dx / Math.max(containerW, 1)) * windowMs;
        dragPendingOffsetMs = Math.max(0, Math.round(dragStartOffset - dMs));
    }

    function commitPan() {
        if (isDragging && dragPendingOffsetMs !== panOffsetMs) {
            panOffsetMs = dragPendingOffsetMs;
        }
        isDragging = false;
    }

    // ── Adapt response.series → MetricsSample[] ───────────────────────────
    /** @typedef {{ time: number, cpu: number, cpuP95: number, mem: number, sessions: number }} Sample */
    let history = $derived.by(() => {
        const series = response?.series ?? {};
        const cpu = series['cpu_pct'];
        if (!cpu || cpu.t.length === 0) return /** @type {Sample[]} */ ([]);
        const p95 = series['cpu_p95_pct']?.avg ?? [];
        const avail = series['mem_avail_mb']?.avg ?? [];
        const total = series['mem_total_mb']?.avg ?? [];
        const sess = series['sessions_total']?.avg ?? [];
        return cpu.t.map((ts, i) => {
            const a = avail[i] ?? 0;
            const m = total[i] ?? 0;
            return {
                time: ts,
                cpu: cpu.avg[i] ?? 0,
                cpuP95: p95[i] ?? cpu.avg[i] ?? 0,
                mem: m > 0 ? (1 - a / m) * 100 : 0,
                sessions: Math.round(sess[i] ?? 0),
            };
        });
    });

    let sessionMax = $derived(Math.max(...history.map((h) => h.sessions ?? 0), 1));
    let hasRight = $derived(showSessions);

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

    let rightTicks = $derived(
        showSessions
            ? [0, 0.25, 0.5, 0.75, 1].map((f) => ({
                  pct: f * 100,
                  label: Math.round(f * sessionMax).toString(),
              }))
            : [],
    );

    let loadVisible = $derived({ cpu: showCpu, cpuP95: showCpuP95, mem: showMem, sessions: showSessions });

    let activeIdx = $derived(appState.pinnedChartIndex ?? appState.hoveredChartIndex);
    let displayPoint = $derived(
        activeIdx !== null ? (history[activeIdx] ?? history[history.length - 1]) : history[history.length - 1],
    );
    let loadCurrents = $derived([
        { label: 'CPU', value: displayPoint ? `${(+displayPoint.cpu).toFixed(1)}%` : '—',
          color: 'var(--color-accent)', icon: Cpu, show: () => showCpu },
        { label: 'CPU P95', value: displayPoint ? `${(+(displayPoint.cpuP95 ?? displayPoint.cpu)).toFixed(1)}%` : '—',
          color: 'var(--color-amber)', icon: Cpu, show: () => showCpuP95 },
        { label: 'MEM', value: displayPoint ? `${(+displayPoint.mem).toFixed(1)}%` : '—',
          color: 'var(--color-green)', icon: MemoryStick, show: () => showMem },
        { label: 'SESS', value: displayPoint ? `${displayPoint.sessions ?? 0}` : '—',
          color: 'var(--color-red)', icon: Users, show: () => showSessions },
    ]);

    const Y_DOMAIN = [0, 100];
    let lcData = $derived(history.map((_, i) => ({ x: i, y: 50 })));
</script>

<div class="h-load">
    <div class="sub-label">
        <Gauge size={12} strokeWidth={2.4} /> HOST LOAD
    </div>

    <div class="window-pills">
        {#each OVERVIEW_WINDOW_PRESETS as preset}
            <button
                class="window-pill"
                class:active={windowKey === preset.key && panOffsetMs === 0}
                onclick={() => {
                    panOffsetMs = 0;
                    windowKey = preset.key;
                }}
                aria-pressed={windowKey === preset.key && panOffsetMs === 0}
            >
                {preset.label}
            </button>
        {/each}
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
            <div>
                {#if panOffsetMs > 0}
                    <button class="back-to-live" onclick={() => { panOffsetMs = 0; }} title="Return to live data">
                        ↺ LIVE
                    </button>
                {/if}
            </div>
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
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
            class="chart-body"
            class:dragging={isDragging}
            bind:clientWidth={containerW}
            onwheel={onWheel}
            onmousedown={onMouseDown}
            onmousemove={onMouseMove}
            onmouseup={commitPan}
            onmouseleave={commitPan}
        >
            {#if loading}
                <div class="chart-placeholder">Loading retained history…</div>
            {:else if error}
                <div class="chart-placeholder chart-error">Unable to reach the metrics endpoint</div>
            {:else if history.length < 2}
                <div class="chart-placeholder">No retained history for this window</div>
            {:else if containerW > 0}
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
                                { pct: cpuThresh.warn, opacity: 0.25, label: 'CPU WARN', show: () => showCpu },
                                { pct: cpuThresh.crit, opacity: 0.35, label: 'CPU CRIT', show: () => showCpu },
                                { pct: memThresh.warn, opacity: 0.25, label: 'MEM WARN', show: () => showMem },
                                { pct: memThresh.crit, opacity: 0.35, label: 'MEM CRIT', show: () => showMem },
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

<style>
    .h-load {
        display: flex;
        flex-direction: column;
    }

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

    .window-pills {
        display: flex;
        gap: 4px;
        margin-bottom: 10px;
    }

    .window-pill {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.58rem;
        font-weight: 700;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        padding: 4px 9px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 2px 2px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-muted);
        cursor: pointer;
        transition:
            transform 0.08s linear,
            box-shadow 0.08s linear,
            color 0.08s linear,
            background 0.08s linear;
    }

    .window-pill:hover {
        color: var(--color-fg);
        transform: translate(-1px, -1px);
        box-shadow: 3px 3px 0 var(--color-shadow);
    }

    .window-pill:active {
        transform: translate(2px, 2px);
        box-shadow: none;
    }

    .window-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }

    .chart-toggles-stacked {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
        margin-bottom: 10px;
    }

    .chart-toggle {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.1em;
        text-transform: uppercase;
        padding: 4px 9px;
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

    .t-dash {
        width: 18px;
        height: 3px;
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

    .chart-panel {
        display: flex;
        flex-direction: column;
    }

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
        font-size: 1.0rem;
        font-weight: 700;
        letter-spacing: -0.02em;
        line-height: 1;
    }

    .chart-body {
        position: relative;
        height: 220px;
        cursor: ew-resize;
    }

    .chart-body.dragging {
        cursor: grabbing;
        user-select: none;
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

    .back-to-live {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.56rem;
        font-weight: 700;
        letter-spacing: 0.12em;
        text-transform: uppercase;
        padding: 3px 8px;
        border: var(--spacing-bw) solid var(--color-amber);
        border-radius: var(--radius-default);
        box-shadow: 1px 1px 0 var(--color-shadow);
        background: var(--color-surface);
        color: var(--color-amber);
        cursor: pointer;
    }

    .back-to-live:hover {
        transform: translate(-1px, -1px);
        box-shadow: 2px 2px 0 var(--color-shadow);
    }

    .back-to-live:active {
        transform: translate(1px, 1px);
        box-shadow: none;
    }
</style>
