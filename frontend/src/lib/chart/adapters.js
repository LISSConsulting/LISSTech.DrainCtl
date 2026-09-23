/**
 * Chart data adapters — pure functions that translate the fleet API's
 * parallel-array series into the flat per-sample shapes the chart
 * primitives consume.
 *
 * Extracted from `components/MetricsChart.svelte` in commit 2 of the
 * chart-library-migration PR so they can be unit-tested without mounting
 * Svelte.
 *
 * IMPORTANT — KEEP PURE. These functions may not import from any Svelte
 * module, may not touch `$state` / `$derived`, and may not perform I/O.
 * Anything impure belongs in the chart parent component.
 */

/**
 * A single fleet counter series. Mirrors the shape returned by
 * `GET /api/v1/metrics/_fleet` for each counter in `series`.
 * `t` is parallel to whichever value array is selected (`avg`/`min`/`max`).
 *
 * @typedef {Object} FleetCounter
 * @property {number[]} t
 * @property {number[]} avg
 * @property {number[]} [min]
 * @property {number[]} [max]
 */

/**
 * @typedef {Object} FleetSeries
 * @property {Record<string, FleetCounter>} [series]
 */

/**
 * @typedef {Object} MetricsSample
 * @property {number} time
 * @property {number} cpu
 * @property {number} [cpuP95]
 * @property {number} mem
 * @property {number} inputDelay
 * @property {number} sessions
 * @property {number} pagesPerSec
 * @property {number} tcpRetrans
 * @property {number} diskQueue
 * @property {number} [p50InputDelay]
 * @property {number} [p50PagesPerSec]
 * @property {number} [p50TcpRetrans]
 * @property {number} [p50DiskQueue]
 */

/**
 * @typedef {Object} SessionSample
 * @property {number} ts
 * @property {number} active
 * @property {number} disconnected
 * @property {number} total
 * @property {number} utilization
 * @property {number} sessionCpuP95
 * @property {number} sessionMemP95
 * @property {number} [sessionCpuP50]
 * @property {number} [sessionMemP50]
 */

/**
 * @typedef {Object} RfxSample
 * @property {number} ts
 * @property {number} fpsOut
 * @property {number} encodeMs
 * @property {number} quality
 * @property {number} rtt
 * @property {number} loss
 * @property {number} skipServer
 * @property {number} skipNet
 * @property {number} [fpsOutP50]
 * @property {number} [encodeMsP50]
 * @property {number} [qualityP50]
 * @property {number} [rttP50]
 * @property {number} [lossP50]
 * @property {number} [skipServerP50]
 * @property {number} [skipNetP50]
 */

/**
 * Build a Map<ts, value> from a counter series so adapters can join
 * sibling counters by timestamp rather than array index. Different
 * counters can have different `t` arrays when individual samples are
 * missing, so positional indexing mispairs them.
 *
 * @param {FleetCounter|undefined} s
 * @param {'avg'|'min'|'max'} [field='avg']
 * @returns {Map<number, number>}
 */
export function tsMap(s, field = 'avg') {
    const m = new Map();
    if (!s) return m;
    const t = s.t || [];
    const v = s[field] || [];
    for (let i = 0; i < t.length; i++) m.set(t[i], v[i]);
    return m;
}

/**
 * Adapt fleet series to per-timestamp MetricsSample[] for the LOAD and
 * HIC charts. Time alignment is anchored on `cpu_pct.t`; missing values
 * from sibling counters default to 0 (or the avg for cpuP95 fallback).
 *
 * @param {Record<string, FleetCounter>} [series]
 * @returns {MetricsSample[]}
 */
export function adaptFleetToMetricsSamples(series = {}) {
    const cpu = series['cpu_pct'];
    if (!cpu || cpu.t.length === 0) return [];
    const cpuAvg = tsMap(cpu, 'avg');
    const cpuMax = tsMap(cpu, 'max');
    const memPctMap = tsMap(series['mem_used_pct']);
    const sessMap = tsMap(series['sessions_total']);
    const idMap = tsMap(series['input_delay_p95_ms']);
    const psMap = tsMap(series['pages_sec']);
    const trMap = tsMap(series['tcp_retrans_sec']);
    const dqMap = tsMap(series['disk_queue']);
    return cpu.t.map((ts) => {
        const cpuV = cpuAvg.get(ts) ?? 0;
        const cpuP95V = cpuMax.get(ts);
        const memV = memPctMap.get(ts);
        const sV = sessMap.get(ts);
        return {
            time: ts,
            cpu: cpuV,
            cpuP95: cpuP95V != null && cpuP95V > 0 ? cpuP95V : cpuV,
            mem: memV ?? 0,
            sessions: sV != null ? Math.round(sV) : 0,
            inputDelay: idMap.get(ts) ?? 0,
            pagesPerSec: psMap.get(ts) ?? 0,
            tcpRetrans: trMap.get(ts) ?? 0,
            diskQueue: dqMap.get(ts) ?? 0,
            p50InputDelay: 0,
            p50PagesPerSec: 0,
            p50TcpRetrans: 0,
            p50DiskQueue: 0,
        };
    });
}

/**
 * Adapt fleet series to SessionSample[] for the SESSIONS sub-tab.
 * Utilization = round(total/maxTotal*100) where maxTotal=0 → 0.
 *
 * @param {Record<string, FleetCounter>} [series]
 * @returns {SessionSample[]}
 */
export function adaptFleetToSessionSamples(series = {}) {
    const tot = series['sessions_total'];
    if (!tot || tot.t.length === 0) return [];
    const totAvg = tsMap(tot, 'avg');
    const activeMap = tsMap(series['sessions_active']);
    const discMap = tsMap(series['sessions_disconnected']);
    const maxMap = tsMap(series['sessions_max']);
    const scpuMap = tsMap(series['session_cpu_p95_pct'], 'max');
    const smemMap = tsMap(series['session_mem_p95_bytes'], 'max');
    return tot.t.map((ts) => {
        const a = Math.round(activeMap.get(ts) ?? 0);
        const d = Math.round(discMap.get(ts) ?? 0);
        const t = Math.round(totAvg.get(ts) ?? 0);
        const mx = Math.round(maxMap.get(ts) ?? 0);
        return {
            ts,
            active: a,
            disconnected: d,
            total: t,
            utilization: mx > 0 ? Math.round((t / mx) * 100) : 0,
            sessionCpuP95: scpuMap.get(ts) ?? 0,
            sessionMemP95: smemMap.get(ts) ?? 0,
            sessionCpuP50: 0,
            sessionMemP50: 0,
        };
    });
}

/**
 * Adapt fleet series to RfxSample[] for the REMOTEFX sub-tab.
 * All counters default to 0 when missing; P50 fields are seeded at 0
 * because the fleet aggregation hasn't been wired for P50 yet.
 *
 * @param {Record<string, FleetCounter>} [series]
 * @returns {RfxSample[]}
 */
export function adaptFleetToRfxSamples(series = {}) {
    const fps = series['rfx_fps_out'];
    if (!fps || fps.t.length === 0) return [];
    const fpsAvg = tsMap(fps, 'avg');
    const fpsP50Map = tsMap(series['rfx_fps_out_p50']);
    const encMap = tsMap(series['rfx_encode_ms']);
    const qualMap = tsMap(series['rfx_quality_pct']);
    const skipSrvMap = tsMap(series['rfx_skip_server_sec']);
    const skipNetMap = tsMap(series['rfx_skip_net_sec']);
    const rttMap = tsMap(series['rfx_rtt_ms']);
    const lossMap = tsMap(series['rfx_loss_pct']);
    return fps.t.map((ts) => ({
        ts,
        fpsOut: fpsAvg.get(ts) ?? 0,
        fpsOutP50: fpsP50Map.get(ts) ?? 0,
        encodeMs: encMap.get(ts) ?? 0,
        encodeMsP50: 0,
        quality: qualMap.get(ts) ?? 0,
        qualityP50: 0,
        skipServer: skipSrvMap.get(ts) ?? 0,
        skipServerP50: 0,
        skipNet: skipNetMap.get(ts) ?? 0,
        skipNetP50: 0,
        rtt: rttMap.get(ts) ?? 0,
        rttP50: 0,
        loss: lossMap.get(ts) ?? 0,
        lossP50: 0,
    }));
}

/**
 * Helper to build a FleetCounter from parallel arrays of timestamps and
 * values. Test-only — production code never builds series client-side.
 *
 * @param {number[]} t
 * @param {number[]} avg
 * @param {number[]} [min]
 * @param {number[]} [max]
 * @returns {FleetCounter}
 */
export function mkCounter(t, avg, min, max) {
    return {
        t: t.slice(),
        avg: avg.slice(),
        min: min ? min.slice() : undefined,
        max: max ? max.slice() : undefined,
    };
}
