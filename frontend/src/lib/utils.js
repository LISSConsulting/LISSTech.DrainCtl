/**
 * utils.js — Shared display-formatting helpers.
 *
 * Centralised here so ServerTable, ServerDetail, HistoryModal, and EventLog
 * all produce identical output without duplicating the same functions.
 */

/**
 * Human-readable relative timestamp (e.g. "3m ago").
 *
 * Accepts an optional `_now` argument so that callers with a reactive clock
 * can pass it explicitly and have Svelte track the time dependency:
 *
 *   rel(server.last_seen, now)   // re-evaluates every time `now` changes
 *   rel(server.registered_at)    // snapshot at call time (fine for non-live uses)
 *
 * @param {string|null|undefined} iso
 * @param {number} [_now]
 * @returns {string}
 */
export function rel(iso, _now = Date.now()) {
    if (!iso) return 'never';
    const d = new Date(iso);
    if (isNaN(d)) return 'never';
    const s = Math.floor((_now - d.getTime()) / 1000);
    if (s < 0) return 'now';
    if (s < 60) return s + 's ago';
    const m = Math.floor(s / 60);
    if (m < 60) return m + 'm ago';
    const h = Math.floor(m / 60);
    if (h < 24) return h + 'h ago';
    return Math.floor(h / 24) + 'd ago';
}

/**
 * Format a Date / numeric / ISO timestamp as American 12-hour time with
 * lowercase meridiem and no space between minutes and am/pm — e.g. "1:03pm",
 * "1:03:01pm". Centralised so charts, footers, tooltips, and event-log
 * entries render identically; en-US's default "1:03 PM" with the space + caps
 * doesn't match the house style.
 *
 * @param {Date|number|string} t
 * @param {{ seconds?: boolean }} [opts]
 * @returns {string}
 */
export function formatTime12(t, { seconds = false } = {}) {
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '';
    const fmtOpts = { hour: 'numeric', minute: '2-digit', hour12: true };
    if (seconds) fmtOpts.second = '2-digit';
    return d.toLocaleTimeString('en-US', fmtOpts).replace(' ', '').toLowerCase();
}

/**
 * Format an ISO timestamp for compact display in history and event log panels.
 * Output: "Apr 11, 1:03:01pm" (en-US, 12-hour, lowercase meridiem).
 * @param {string} iso
 * @returns {string}
 */
export function formatTs(iso) {
    const d = new Date(iso);
    if (isNaN(d)) return iso;
    const date = d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
    return date + ', ' + formatTime12(d, { seconds: true });
}

/**
 * Convert a drain mode identifier to a human-readable label.
 *
 * Accepts both the Windows Registry API constant strings produced by
 * DrainMode.String() in the Go backend and legacy short-form labels.
 * Falls back to returning the raw value (or '—' when empty/null).
 *
 * @param {string|null|undefined} m
 * @returns {string}
 */
export function modeLabel(m) {
    const ML = {
        ALLOW_ALL_CONNECTIONS: 'Open',
        ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS: 'Drain',
        ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS_UNTIL_RESTART: 'Drain (Restart)',
        DENY_ALL_CONNECTIONS: 'Denied',
        none: 'Open',
        graceful: 'Graceful',
        immediate: 'Immediate',
    };
    return ML[m] || m || '—';
}

/**
 * Format a duration in seconds to a human-readable string.
 * Examples: "45s", "3m 12s", "2h 5m"
 * @param {number|null|undefined} sec
 * @returns {string}
 */
export function dur(sec) {
    if (sec == null) return '—';
    const s = Math.floor(sec);
    if (s < 60) return s + 's';
    const m = Math.floor(s / 60);
    if (m < 60) return m + 'm ' + (s % 60) + 's';
    const h = Math.floor(m / 60);
    return h + 'h ' + (m % 60) + 'm';
}

/**
 * Counters whose values are expressed as 0–100 percentages. Used to pin chart
 * Y-axis domains to [0,100] so ticks don't render outside the plot area when
 * the observed data stays well below 100%.
 */
export const PERCENT_COUNTERS = new Set([
    'cpu_pct',
    'cpu_p95_pct',
    'mem_pct',
    'mem_pct_used',
    'mem_used_pct',
    'session_cpu_p95_pct',
    'session_cpu_p50_pct',
    'rfx_quality_pct',
    'rfx_quality_pct_p50',
    'rfx_loss_pct',
    'rfx_loss_pct_p50',
]);

/** @param {string} counter */
export function isPercentCounter(counter) {
    return PERCENT_COUNTERS.has(counter);
}

/**
 * Human-readable labels for the perfmon/session counters emitted by
 * checkResultSamples() in internal/dashboard/server.go. Anything not mapped
 * falls back to the raw key so new counters are still visible while awaiting a
 * label.
 */
const COUNTER_LABELS = {
    cpu_pct: 'CPU',
    cpu_p95_pct: 'CPU p95',
    mem_pct: 'Memory',
    mem_pct_used: 'Memory',
    mem_used_pct: 'Memory',
    mem_avail_mb: 'Memory Available',
    mem_total_mb: 'Memory Total',
    pages_sec: 'Pages/sec',
    disk_queue: 'Disk Queue',
    tcp_retrans_sec: 'TCP Retrans/s',
    input_delay_p50_ms: 'Input Delay p50',
    input_delay_p95_ms: 'Input Delay p95',
    input_delay_max_ms: 'Input Delay max',
    session_cpu_p95_pct: 'Session CPU p95',
    session_cpu_p50_pct: 'Session CPU p50',
    session_mem_p95_bytes: 'Session Memory p95',
    session_mem_p50_bytes: 'Session Memory p50',
    sessions_total: 'Sessions',
    sessions_active: 'Active Sessions',
    sessions_disconnected: 'Disconnected Sessions',
    sessions_max: 'Max Sessions',
    rfx_fps_out: 'RFX FPS Out',
    rfx_fps_out_p50: 'RFX FPS Out p50',
    rfx_skip_server_sec: 'RFX Skip Server',
    rfx_skip_net_sec: 'RFX Skip Net',
    rfx_encode_ms: 'RFX Encode',
    rfx_encode_ms_p50: 'RFX Encode p50',
    rfx_quality_pct: 'RFX Quality',
    rfx_quality_pct_p50: 'RFX Quality p50',
    rfx_rtt_ms: 'RFX RTT',
    rfx_rtt_ms_p50: 'RFX RTT p50',
    rfx_loss_pct: 'RFX Loss',
    rfx_loss_pct_p50: 'RFX Loss p50',
    rfx_skip_server_sec_p50: 'RFX Skip Server p50',
    rfx_skip_net_sec_p50: 'RFX Skip Net p50',
};

/** @param {string} counter */
export function counterLabel(counter) {
    return COUNTER_LABELS[counter] || counter;
}
