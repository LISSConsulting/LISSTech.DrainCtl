/**
 * api.js — Typed fetch wrappers for all DrainCtl API routes.
 *
 * The Go backend uses SSPI/Negotiate authentication; cookies are handled
 * automatically by the browser via credentials: 'include'.
 *
 * All routes are at /api/v1/.
 */

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

/**
 * Core fetch helper. Always sends credentials for SSPI/Negotiate.
 * Throws an ApiError on non-2xx responses.
 *
 * @param {string} path - Path relative to BASE, e.g. '/health'
 * @param {RequestInit} [options]
 * @returns {Promise<Response>}
 */
async function apiFetch(path, options = {}) {
  const response = await fetch(`${BASE}${path}`, {
    credentials: 'include',
    ...options,
    headers: {
      'Accept': 'application/json',
      ...options.headers,
    },
  });

  if (!response.ok) {
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
  return /** @type {NotifyConfig} */ (await res.json());
}

/**
 * PUT /api/v1/notify-config
 * Returns {ok: true} on success — does NOT return the saved config.
 * Callers should treat the local config as authoritative after a successful save.
 * @param {NotifyConfig} config
 * @returns {Promise<void>}
 */
export async function saveNotifyConfig(config) {
  await apiFetch('/notify-config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(config),
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
