/**
 * api.js — Typed fetch wrappers for all DrainCtl API routes.
 *
 * The Go backend uses session-cookie authentication for dashboard routes.
 * Cookies are handled automatically by the browser via credentials: 'include'.
 *
 * All routes are at /api/v1/.
 */

import { authState } from './auth.svelte.js';

const BASE = '/api/v1';

/**
 * PerfMetrics maps the Go PerfSnapshot JSON fields that the dashboard uses.
 * Field names match PerfSnapshot JSON tags exactly (Go struct → JSON snake_case).
 *
 * @typedef {Object} PerfMetrics
 * @property {number}  cpu_pct                  - Host CPU % (0–100), average across samples
 * @property {number}  cpu_p95_pct              - Host CPU P95 % across samples
 * @property {number}  mem_avail_mb             - Available memory in MB
 * @property {number}  mem_total_mb             - Total physical memory in MB
 * @property {number}  pages_sec                - Memory pages/sec
 * @property {number}  disk_queue               - Average disk queue length
 * @property {number}  tcp_retrans_sec          - TCP retransmits/sec
 * @property {number}  input_delay_p50_ms       - User input delay P50 (ms)
 * @property {number}  input_delay_p95_ms       - User input delay P95 (ms)
 * @property {number}  input_delay_max_ms       - User input delay max (ms)
 * @property {number}  [session_cpu_p95_pct]    - Per-session CPU P95 % (omitted when zero)
 * @property {number}  [session_cpu_p50_pct]    - Per-session CPU P50 % (omitted when zero)
 * @property {number}  [session_mem_p95_bytes]  - Per-session working set P95 in bytes (omitted when zero)
 * @property {number}  [session_mem_p50_bytes]  - Per-session working set P50 in bytes (omitted when zero)
 * @property {boolean} rfx_available            - true when RemoteFX counters are collected
 * @property {number}  [rfx_fps_out]            - RemoteFX output FPS P95
 * @property {number}  [rfx_fps_out_p50]        - RemoteFX output FPS P50
 * @property {number}  [rfx_encode_ms]          - RemoteFX encode time P95 (ms)
 * @property {number}  [rfx_encode_ms_p50]      - RemoteFX encode time P50 (ms)
 * @property {number}  [rfx_quality_pct]        - RemoteFX frame quality P95 %
 * @property {number}  [rfx_quality_pct_p50]    - RemoteFX frame quality P50 %
 * @property {number}  [rfx_rtt_ms]             - RemoteFX TCP round-trip time P95 (ms)
 * @property {number}  [rfx_rtt_ms_p50]         - RemoteFX TCP round-trip time P50 (ms)
 * @property {number}  [rfx_loss_pct]           - RemoteFX loss rate P95 %
 * @property {number}  [rfx_loss_pct_p50]       - RemoteFX loss rate P50 %
 * @property {number}  [rfx_skip_server_sec]    - RemoteFX frames skipped/sec (server) P95
 * @property {number}  [rfx_skip_server_sec_p50] - RemoteFX frames skipped/sec (server) P50
 * @property {number}  [rfx_skip_net_sec]       - RemoteFX frames skipped/sec (network) P95
 * @property {number}  [rfx_skip_net_sec_p50]   - RemoteFX frames skipped/sec (network) P50
 */

/**
 * Server is the flattened view returned by GET /api/v1/servers.
 * The Go backend wraps ServerInfo + CheckResult into this shape so the
 * frontend never has to navigate nested last_result fields.
 *
 * @typedef {Object} Server
 * @property {string} host
 * @property {'ok'|'warning'|'grace'|'alert'|'off'} status
 * @property {string} drain_mode
 * @property {number} sessions                 - TotalSessions (integer)
 * @property {number} sessions_active          - Active (connected) sessions
 * @property {number} sessions_disconnected    - Disconnected sessions
 * @property {number} max_sessions             - Server session capacity (0 when unknown)
 * @property {number|null} state_duration_seconds - Seconds in current state (null when unknown)
 * @property {string|null} state_changed_at        - ISO timestamp when current state began (null when unknown)
 * @property {string} version
 * @property {string} registered_at
 * @property {string} last_seen
 * @property {string|null} grace_deadline
 * @property {string} changed_by
 * @property {PerfMetrics|null} perf           - null when performance monitoring is disabled
 */

/**
 * HistoryEntry is one record from GET /api/v1/history/{host}.
 * Status is normalised to lowercase tokens by the Go backend.
 *
 * @typedef {Object} HistoryEntry
 * @property {string} timestamp
 * @property {string} host
 * @property {'ok'|'warning'|'grace'|'alert'|'off'} status  - lowercase token from the backend
 * @property {string} drain_mode                   - drain mode label string from the server
 * @property {number|null} [state_duration_seconds]
 * @property {boolean} transition
 * @property {string} [transition_from]
 * @property {string} [changed_by]
 * @property {string} version
 * @property {string} message
 */

/**
 * @typedef {Object} PerfMonitoringConfig
 * @property {boolean} enabled
 * @property {boolean} force_disabled
 * @property {number} sample_interval_sec
 * @property {number} cpu_warn_pct
 * @property {number} cpu_crit_pct
 * @property {number} mem_warn_pct
 * @property {number} mem_crit_pct
 * @property {number} input_delay_warn_ms
 * @property {number} input_delay_crit_ms
 * @property {number} load_alert_delay_sec
 * @property {number} input_delay_alert_delay_sec
 * @property {boolean} collect_per_session
 * @property {boolean} collect_remotefx
 */

/**
 * @typedef {Object} NotifyTarget
 * @property {string} [id]             - frontend-only UUID for keying list items; not persisted
 * @property {'webhook'|'ntfy'|'email'} type
 * @property {string} url              - webhook or ntfy URL (empty for email)
 * @property {string} [from]           - email from address (email type only)
 * @property {string[]} [to]           - email to addresses (email type only)
 * @property {string} [secret]         - HMAC secret for webhook signing
 * @property {string[]} triggers
 * @property {number} repeat_minutes   - 0 = once only
 * @property {boolean} [enabled]       - false = skip this target; absent/true = send (default)
 */

/**
 * @typedef {Object} Settings
 * @property {number} grace_period              - grace period in minutes
 * @property {number} session_warning_threshold
 * @property {PerfMonitoringConfig} performance
 * @property {NotifyTarget[]} notifications
 */

/**
 * @typedef {Object} HealthResponse
 * @property {string} version
 * @property {{ total: number, ok: number, grace: number, alert: number, off: number }} servers
 */

/** Default request timeout — prevents network hangs from freezing the dashboard. */
const FETCH_TIMEOUT_MS = 20_000;

/**
 * Core fetch helper. Always sends credentials for SSPI/Negotiate.
 * Automatically aborts after FETCH_TIMEOUT_MS milliseconds.
 * Throws an ApiError on non-2xx responses or timeout.
 *
 * @param {string} path - Path relative to BASE, e.g. '/health'
 * @param {RequestInit} [options]
 * @returns {Promise<Response>}
 */
async function apiFetch(path, options = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);

    let response;
    try {
        response = await fetch(`${BASE}${path}`, {
            credentials: 'include',
            signal: controller.signal,
            ...options,
            headers: {
                Accept: 'application/json',
                ...options.headers,
            },
        });
    } catch (err) {
        if (err instanceof DOMException && err.name === 'AbortError') {
            throw new ApiError(0, 'Timeout', `Request timed out after ${FETCH_TIMEOUT_MS / 1000}s`, path);
        }
        throw err;
    } finally {
        clearTimeout(timer);
    }

    if (!response.ok) {
        if (response.status === 401) {
            authState.username = null;
            authState.error = 'session_expired';
        }
        let detail = '';
        try {
            const body = await response.json();
            detail = body.error ?? body.message ?? JSON.stringify(body);
        } catch {
            detail = await response.text().catch(() => '');
        }
        throw new ApiError(response.status, response.statusText, detail, path);
    }

    return response;
}

/**
 * Structured error thrown for non-2xx API responses.
 */
export class ApiError extends Error {
    /**
     * @param {number} status
     * @param {string} statusText
     * @param {string} detail
     * @param {string} path
     */
    constructor(status, statusText, detail, path) {
        const message = `API ${status} ${statusText} — ${path}${detail ? ': ' + detail : ''}`;
        super(message);
        this.name = 'ApiError';
        this.status = status;
        this.statusText = statusText;
        this.detail = detail;
        this.path = path;
    }
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/health
 * @returns {Promise<HealthResponse>}
 */
export async function fetchHealth() {
    const res = await apiFetch('/health');
    return /** @type {HealthResponse} */ (await res.json());
}

// ---------------------------------------------------------------------------
// Servers
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/servers
 * @returns {Promise<Server[]>}
 */
export async function fetchServers() {
    const res = await apiFetch('/servers');
    return /** @type {Server[]} */ (await res.json());
}

/**
 * DELETE /api/v1/servers/{host}
 * Returns undefined on 204 No Content.
 * @param {string} host
 * @returns {Promise<void>}
 */
export async function deleteServer(host) {
    await apiFetch(`/servers/${encodeURIComponent(host)}`, { method: 'DELETE' });
}

// ---------------------------------------------------------------------------
// Per-server perf metrics history (sparkline seed)
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/metrics
 *
 * Returns a map of hostname → MetricsSample[] containing the last
 * MAX_PERF_HISTORY samples per server. Used by App.svelte to seed
 * appState.serverMetrics on a cold start so sparklines are visible
 * immediately without waiting for polling cycles to accumulate data.
 *
 * This endpoint is only served by the dev mock; in production the client
 * accumulates samples from the regular /api/v1/servers poll.
 *
 * @returns {Promise<Record<string, import('./state.svelte.js').MetricsSample[]>>}
 */
export async function fetchAllServerMetrics() {
    const res = await apiFetch('/metrics');
    return /** @type {Record<string, any[]>} */ (await res.json());
}

/**
 * CounterSeries is one counter's parallel arrays inside a metrics response.
 * For tier=raw, avg === min === max === the raw sample value.
 *
 * @typedef {Object} CounterSeries
 * @property {number[]} t   - Unix-ms timestamps (parallel to avg/min/max).
 * @property {number[]} avg
 * @property {number[]} min
 * @property {number[]} max
 */

/**
 * MetricsResponse is the shape returned by GET /api/v1/metrics/{host}.
 * Per contracts/http-metrics.md: on empty windows the server returns 200
 * with series === {} and oldest_available/newest_available === null so the
 * UI can render the FR-019a "Collecting data…" state.
 *
 * @typedef {Object} MetricsResponse
 * @property {string} host
 * @property {'raw'|'5min'|'hourly'} tier          - tier the server actually served (may differ from requested)
 * @property {string} from                         - ISO-8601 UTC (echo of request)
 * @property {string} to                           - ISO-8601 UTC (echo of request)
 * @property {string|null} oldest_available        - ISO-8601 UTC; null when the tier holds no rows for this host
 * @property {string|null} newest_available        - ISO-8601 UTC; null when the tier holds no rows for this host
 * @property {Record<string, CounterSeries>} series
 */

/**
 * GET /api/v1/metrics/{host}
 *
 * @param {string} host
 * @param {Date|string} from                         - inclusive lower bound
 * @param {Date|string} to                           - exclusive upper bound (must be > from)
 * @param {'raw'|'5min'|'hourly'|'auto'} [resolution='auto']
 * @param {string[]} [counters]                      - omitted → all known counters
 * @returns {Promise<MetricsResponse>}
 */
export async function fetchMetrics(host, from, to, resolution = 'auto', counters) {
    const params = new URLSearchParams({
        from: from instanceof Date ? from.toISOString() : from,
        to: to instanceof Date ? to.toISOString() : to,
        resolution,
    });
    if (counters && counters.length > 0) {
        params.set('counters', counters.join(','));
    }
    const res = await apiFetch(`/metrics/${encodeURIComponent(host)}?${params}`);
    return /** @type {MetricsResponse} */ (await res.json());
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/history/{host}
 * @param {string} host
 * @param {number} [limit=50]
 * @param {boolean} [changesOnly=false]
 * @returns {Promise<HistoryEntry[]>}
 */
export async function fetchHistory(host, limit = 50, changesOnly = false) {
    const params = new URLSearchParams({
        limit: String(limit),
        changes_only: String(changesOnly),
    });
    const res = await apiFetch(`/history/${encodeURIComponent(host)}?${params}`);
    return /** @type {HistoryEntry[]} */ (await res.json());
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/settings
 * @returns {Promise<Settings>}
 */
export async function fetchSettings() {
    const res = await apiFetch('/settings');
    const cfg = /** @type {Settings} */ (await res.json());
    // Go stores memory thresholds as % free; UI works in % used — always invert on load.
    // 0 means "use default" in Go; inverting it to 100 is harmless (resolveThresholds
    // checks > 0 and falls back to the default, which matches Go's behavior).
    if (cfg.performance) {
        cfg.performance.mem_warn_pct = 100 - (cfg.performance.mem_warn_pct ?? 0);
        cfg.performance.mem_crit_pct = 100 - (cfg.performance.mem_crit_pct ?? 0);
    }
    return cfg;
}

/**
 * PUT /api/v1/settings
 * Returns {ok: true} on success — does NOT return the saved config.
 * Callers should treat the local config as authoritative after a successful save.
 * @param {Settings} config
 * @returns {Promise<void>}
 */
export async function saveSettings(config) {
    // Deep-clone to avoid mutating the UI state, then invert mem % used → % free for Go.
    const payload = JSON.parse(JSON.stringify(config));
    if (payload.performance) {
        payload.performance.mem_warn_pct = 100 - (payload.performance.mem_warn_pct ?? 0);
        payload.performance.mem_crit_pct = 100 - (payload.performance.mem_crit_pct ?? 0);
    }
    await apiFetch('/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
    });
}

// ---------------------------------------------------------------------------
// Notify test
// ---------------------------------------------------------------------------

/**
 * POST /api/v1/notify-test
 *
 * Pass a full NotifyTarget object to test a specific target (the backend
 * decodes it directly from the request body and sends to that target only).
 * Pass null to test all currently saved targets.
 *
 * The body is parsed regardless of HTTP status — the server returns 200 on
 * full success, 207 (Multi-Status) when some targets failed, and 400 when
 * everything failed or no targets are configured. Always shape:
 *   { ok: boolean, results: [...], error?: string }
 *
 * Bypasses apiFetch (which throws on non-2xx) so callers can render
 * per-target failures from the body itself.
 *
 * @param {NotifyTarget|null} [target=null]
 * @returns {Promise<{ok: boolean, results: Array<{type:string,url:string,type_index:number,ok:boolean,error?:string}>, error?: string}>}
 */
export async function sendNotifyTest(target = null) {
    const r = await fetch(`${BASE}/notify-test`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: target != null ? JSON.stringify(target) : '{}',
    });
    let body;
    try {
        body = await r.json();
    } catch {
        // Server returned non-JSON (e.g. proxy 502). Synthesise an error shape.
        const txt = await r.text().catch(() => '');
        body = { ok: false, results: [], error: txt || `${r.status} ${r.statusText}` };
    }
    if (!body.results) body.results = [];
    return body;
}
