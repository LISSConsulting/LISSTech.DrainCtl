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
 * @property {number} cpu_pct              - Host CPU % (0–100)
 * @property {number} mem_avail_mb         - Available memory in MB
 * @property {number} mem_total_mb         - Total physical memory in MB
 * @property {number} pages_sec            - Memory pages/sec
 * @property {number} disk_queue           - Average disk queue length
 * @property {number} tcp_retrans_sec      - TCP retransmits/sec
 * @property {number} input_delay_p50_ms   - Input delay P50 (ms)
 * @property {number} input_delay_p95_ms   - Input delay P95 (ms)
 * @property {number} input_delay_max_ms   - Input delay max (ms)
 */

/**
 * Server is the flattened view returned by GET /api/v1/servers.
 * The Go backend wraps ServerInfo + CheckResult into this shape so the
 * frontend never has to navigate nested last_result fields.
 *
 * @typedef {Object} Server
 * @property {string} host
 * @property {'ok'|'grace'|'alert'|'off'} status
 * @property {string} drain_mode
 * @property {number} sessions             - TotalSessions (integer)
 * @property {number} max_sessions         - Server session capacity (0 when unknown)
 * @property {string} version
 * @property {string} registered_at
 * @property {string} last_seen
 * @property {string|null} grace_deadline
 * @property {string} changed_by
 * @property {PerfMetrics|null} perf       - null when performance monitoring is disabled
 */

/**
 * HistoryEntry is one record from GET /api/v1/history/{host}.
 * Status is normalised to lowercase tokens by the Go backend.
 *
 * @typedef {Object} HistoryEntry
 * @property {string} timestamp
 * @property {string} host
 * @property {'ok'|'grace'|'alert'|'off'} status  - lowercase token from the backend
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
 * @typedef {Object} NotifyConfig
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
 * GET /api/v1/servers/{host}
 * @param {string} host
 * @returns {Promise<Server>}
 */
export async function fetchServer(host) {
    const res = await apiFetch(`/servers/${encodeURIComponent(host)}`);
    return /** @type {Server} */ (await res.json());
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
// Notify config
// ---------------------------------------------------------------------------

/**
 * GET /api/v1/notify-config
 * @returns {Promise<NotifyConfig>}
 */
export async function fetchNotifyConfig() {
    const res = await apiFetch('/notify-config');
    const cfg = /** @type {NotifyConfig} */ (await res.json());
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
 * PUT /api/v1/notify-config
 * Returns {ok: true} on success — does NOT return the saved config.
 * Callers should treat the local config as authoritative after a successful save.
 * @param {NotifyConfig} config
 * @returns {Promise<void>}
 */
export async function saveNotifyConfig(config) {
    // Deep-clone to avoid mutating the UI state, then invert mem % used → % free for Go.
    const payload = JSON.parse(JSON.stringify(config));
    if (payload.performance) {
        payload.performance.mem_warn_pct = 100 - (payload.performance.mem_warn_pct ?? 0);
        payload.performance.mem_crit_pct = 100 - (payload.performance.mem_crit_pct ?? 0);
    }
    await apiFetch('/notify-config', {
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
 * @param {NotifyTarget|null} [target=null]
 * @returns {Promise<{ok: boolean, message?: string}>}
 */
export async function sendNotifyTest(target = null) {
    const res = await apiFetch('/notify-test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: target != null ? JSON.stringify(target) : '{}',
    });
    return /** @type {{ok: boolean, message?: string}} */ (await res.json());
}
