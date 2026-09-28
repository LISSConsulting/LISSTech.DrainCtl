/**
 * Build a timestamp-to-value map for one counter series.
 * @param {{t:number[],avg:number[],min:number[],max:number[],p50:number[]}|undefined} series
 * @param {'avg'|'min'|'max'|'p50'} field
 */
function tsMap(series, field = 'avg') {
    const values = new Map();
    if (!series) return values;
    for (let i = 0; i < (series.t ?? []).length; i++) values.set(series.t[i], (series[field] ?? [])[i]);
    return values;
}

/**
 * Adapt fleet series for LOAD and Health Indicators.
 * @param {Record<string,{t:number[],avg:number[],min:number[],max:number[],p50:number[]}>} series
 */
export function adaptFleetToMetricsSamples(series) {
    const cpu = series.cpu_pct;
    if (!cpu || cpu.t.length === 0) return [];
    const cpuAvg = tsMap(cpu);
    const cpuP95 = tsMap(series.cpu_p95_pct, 'max');
    const memory = tsMap(series.mem_used_pct);
    const sessions = tsMap(series.sessions_total);
    const inputP95 = tsMap(series.input_delay_p95_ms, 'max');
    const inputP50 = tsMap(series.input_delay_p50_ms, 'p50');
    const pages = tsMap(series.pages_sec);
    const pagesP50 = tsMap(series.pages_sec, 'p50');
    const retransmits = tsMap(series.tcp_retrans_sec);
    const retransmitsP50 = tsMap(series.tcp_retrans_sec, 'p50');
    const diskQueue = tsMap(series.disk_queue);
    const diskQueueP50 = tsMap(series.disk_queue, 'p50');

    return cpu.t.map((ts) => {
        const cpuValue = cpuAvg.get(ts) ?? 0;
        const p95Value = cpuP95.get(ts);
        return {
            time: ts,
            cpu: cpuValue,
            cpuP95: p95Value != null && p95Value > 0 ? p95Value : cpuValue,
            mem: memory.get(ts) ?? 0,
            sessions: Math.round(sessions.get(ts) ?? 0),
            inputDelay: inputP95.get(ts) ?? 0,
            pagesPerSec: pages.get(ts) ?? 0,
            tcpRetrans: retransmits.get(ts) ?? 0,
            diskQueue: diskQueue.get(ts) ?? 0,
            p50InputDelay: inputP50.get(ts),
            p50PagesPerSec: pagesP50.get(ts),
            p50TcpRetrans: retransmitsP50.get(ts),
            p50DiskQueue: diskQueueP50.get(ts),
        };
    });
}

/**
 * Adapt fleet series for Session Metrics.
 * @param {Record<string,{t:number[],avg:number[],min:number[],max:number[],p50:number[]}>} series
 */
export function adaptFleetToSessionSamples(series) {
    const totalSeries = series.sessions_total;
    if (!totalSeries || totalSeries.t.length === 0) return [];
    const total = tsMap(totalSeries);
    const active = tsMap(series.sessions_active);
    const disconnected = tsMap(series.sessions_disconnected);
    const capacity = tsMap(series.sessions_max);
    const cpuP95 = tsMap(series.session_cpu_p95_pct, 'max');
    const cpuP50 = tsMap(series.session_cpu_p50_pct, 'p50');
    const memoryP95 = tsMap(series.session_mem_p95_bytes, 'max');
    const memoryP50 = tsMap(series.session_mem_p50_bytes, 'p50');

    return totalSeries.t.map((ts) => {
        const activeCount = Math.round(active.get(ts) ?? 0);
        const disconnectedCount = Math.round(disconnected.get(ts) ?? 0);
        const totalCount = Math.round(total.get(ts) ?? 0);
        const maxCount = Math.round(capacity.get(ts) ?? 0);
        return {
            ts,
            active: activeCount,
            disconnected: disconnectedCount,
            total: totalCount,
            utilization: maxCount > 0 ? Math.min((totalCount / maxCount) * 100, 100) : 0,
            sessionCpuP95: cpuP95.get(ts) ?? 0,
            sessionCpuP50: cpuP50.get(ts) ?? 0,
            sessionMemP95: memoryP95.get(ts) ?? 0,
            sessionMemP50: memoryP50.get(ts) ?? 0,
        };
    });
}
