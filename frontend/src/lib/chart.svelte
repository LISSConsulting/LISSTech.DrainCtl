<script>
    /**
     * chart.svelte — durable per-host metrics chart backed by GET /api/v1/metrics/{host}.
     *
     * Replaces the legacy history-ring data source; the server now holds the
     * authoritative series in SQLite so the chart survives a service restart
     * without losing pre-restart samples.
     *
     * Props:
     *   host        — required hostname (must be registered)
     *   counter     — counter name from the PerfSnapshot/SessionSummary set;
     *                 defaults to 'cpu_pct'
     *   windowMs    — lookback window in milliseconds (default 5 days)
     *   resolution  — 'auto' | 'raw' | '5min' | 'hourly' (default 'auto')
     *   color       — line/fill color (CSS var or hex)
     *   height      — chart height in pixels
     *   refreshMs   — optional auto-refetch interval; 0 disables polling
     *
     * When the server returns an empty series map the chart renders the
     * "Collecting data…" status per FR-019a.
     */
    import { LayerCake, Svg } from 'layercake';
    import LinePath from '../components/chart/LinePath.svelte';
    import { fetchMetrics } from './api.js';

    /** @type {{
     *   host: string,
     *   counter?: string,
     *   windowMs?: number,
     *   resolution?: 'auto'|'raw'|'5min'|'hourly',
     *   color?: string,
     *   height?: number,
     *   refreshMs?: number,
     * }} */
    let {
        host,
        counter = 'cpu_pct',
        windowMs = 5 * 24 * 60 * 60 * 1000,
        resolution = 'auto',
        color = 'var(--color-accent)',
        height = 200,
        refreshMs = 0,
    } = $props();

    /** @type {import('./api.js').MetricsResponse | null} */
    let response = $state(null);
    let loading = $state(false);
    let error = $state('');

    // Monotonic sequence so a slow-returning request can't overwrite a newer one.
    let fetchSeq = 0;

    async function load() {
        if (!host) return;
        const mySeq = ++fetchSeq;
        loading = true;
        error = '';
        try {
            const to = new Date();
            const from = new Date(to.getTime() - windowMs);
            const r = await fetchMetrics(host, from, to, resolution, [counter]);
            if (mySeq !== fetchSeq) return;
            response = r;
        } catch (e) {
            if (mySeq !== fetchSeq) return;
            response = null;
            error = /** @type {Error} */ (e)?.message ?? String(e);
        } finally {
            if (mySeq === fetchSeq) loading = false;
        }
    }

    // Refetch whenever host / counter / window / resolution changes.
    $effect(() => {
        host;
        counter;
        windowMs;
        resolution;
        load();
    });

    // Optional polling so the chart keeps pace while the user stays on the page.
    $effect(() => {
        if (!refreshMs || refreshMs <= 0) return;
        const id = setInterval(load, refreshMs);
        return () => clearInterval(id);
    });

    let series = $derived(response?.series?.[counter] ?? null);
    let isEmpty = $derived.by(() => {
        if (!response) return true;
        const map = response.servers ?? response.series ?? {};
        if (Object.keys(map).length === 0) return true;
        return !series || !Array.isArray(series.t) || series.t.length === 0;
    });

    let points = $derived.by(() => {
        if (!series) return [];
        const ts = series.t ?? [];
        const avg = series.avg ?? [];
        /** @type {{t:number,v:number}[]} */
        const out = [];
        for (let i = 0; i < ts.length; i++) {
            const v = avg[i];
            if (v == null || !Number.isFinite(v)) continue;
            out.push({ t: ts[i], v });
        }
        return out;
    });

    let tier = $derived(response?.tier ?? resolution);
</script>

<div class="chart-wrap" style="height:{height}px">
    {#if error}
        <div class="chart-status err">Error: {error}</div>
    {:else if loading && !response}
        <div class="chart-status">Loading…</div>
    {:else if isEmpty}
        <div class="chart-status">Collecting data…</div>
    {:else}
        <LayerCake
            data={points}
            x="t"
            y="v"
            yDomain={[0, null]}
            padding={{ top: 8, right: 8, bottom: 8, left: 8 }}
        >
            <Svg>
                <LinePath {color} filled={true} />
            </Svg>
        </LayerCake>
        <div class="chart-meta">
            <span class="meta-counter">{counter}</span>
            <span class="meta-tier">tier={tier}</span>
        </div>
    {/if}
</div>

<style>
    .chart-wrap {
        position: relative;
        width: 100%;
        background: var(--color-surface);
        border-radius: var(--radius-default);
    }

    .chart-status {
        position: absolute;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        color: var(--color-muted);
        letter-spacing: 0.04em;
    }

    .chart-status.err {
        color: var(--color-red);
    }

    .chart-meta {
        position: absolute;
        top: 6px;
        right: 8px;
        display: flex;
        gap: 8px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.55rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--color-muted);
        opacity: 0.7;
        pointer-events: none;
    }

    .meta-tier {
        color: var(--color-subtle);
    }
</style>
