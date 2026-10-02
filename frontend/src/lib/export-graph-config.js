/** Static frontend mirror of internal/dashboard/export_graphs.go. */
export const EXPORT_GRAPHS = {
    'overview.load': {
        label: 'Overview Load',
        defaultCounters: ['cpu_pct', 'mem_avail_mb', 'mem_total_mb', 'input_delay_p95_ms'],
        slug: 'overview-load',
    },
    'humanic.input_delay': {
        label: 'HIC · Input Delay',
        defaultCounters: ['input_delay_p95_ms'],
        slug: 'input-delay',
    },
    'humanic.pages_sec': { label: 'HIC · Pages / sec', defaultCounters: ['pages_sec'], slug: 'pages-sec' },
    'humanic.tcp_retrans_sec': {
        label: 'HIC · TCP Retransmits / sec',
        defaultCounters: ['tcp_retrans_sec'],
        slug: 'tcp-retrans-sec',
    },
    'humanic.disk_queue': { label: 'HIC · Disk Queue', defaultCounters: ['disk_queue'], slug: 'disk-queue' },
    'overview.sessions': {
        label: 'Sessions',
        defaultCounters: ['sessions_total', 'sessions_active', 'sessions_max'],
        slug: 'sessions',
    },
    'overview.remotefx': {
        label: 'RemoteFX',
        defaultCounters: ['rfx_encode_p95_ms', 'rfx_encode_avg_ms', 'rfx_frames_sec', 'rfx_tcp_rtt_ms'],
        slug: 'remotefx',
    },
    'host.load': {
        label: 'Per-host Load',
        defaultCounters: ['cpu_pct', 'mem_avail_mb', 'mem_total_mb', 'input_delay_p95_ms'],
        slug: 'load',
    },
};

/** @param {{loading:boolean, failed:boolean, empty:boolean, hasEnabledSeries:boolean, isStale:boolean}} state */
export function shouldDisableExport({ loading, failed, empty, hasEnabledSeries, isStale }) {
    return loading || failed || empty || !hasEnabledSeries || isStale;
}

/** @param {{label:string}} graph */
export function exportAriaLabel(graph) {
    return `Export ${graph.label}`;
}
