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
 * @property {number}  [logical_processors]     - Logical processor count available to the service
 * @property {number}  mem_avail_mb             - Available memory in MB
 * @property {number}  mem_total_mb             - Total physical memory in MB
 * @property {number}  pages_sec                - Memory pages/sec
 * @property {number}  disk_queue               - Average disk queue length
 * @property {number}  tcp_retrans_sec          - TCP retransmits/sec
 * @property {number}  input_delay_p50_ms       - User input delay P50 (ms)
 * @property {number}  input_delay_p95_ms       - User input delay P95 (ms)
 * @property {number}  input_delay_max_ms       - User input delay max (ms)
 * @property {number}  [session_cpu_p95_pct]    - Per-session CPU P95 % (omitted when zero)
 * @property {number}  [session_mem_p95_bytes]  - Per-session working-set P95 in bytes (omitted when zero)
 * @property {boolean} [session_cpu_activity_collected] - True when active-session CPU thresholds were measured
 * @property {number}  [session_cpu_observed_count] - Active sessions with a CPU sample
 * @property {number}  [session_cpu_ge_5_count] - Observed active sessions at or above 5% CPU
 * @property {number}  [session_cpu_ge_20_count] - Observed active sessions at or above 20% CPU
 * @property {boolean} rfx_available            - true when RemoteFX counters are collected
 * @property {number}  [rfx_fps_out]            - RemoteFX output FPS service P95 floor (numeric P5)
 * @property {number}  [rfx_fps_out_p50]        - RemoteFX output FPS median
 * @property {number}  [rfx_encode_ms]           - RemoteFX encode time P95 (ms)
 * @property {number}  [rfx_encode_ms_p50]       - RemoteFX encode time median (ms)
 * @property {number}  [rfx_quality_pct]         - RemoteFX frame quality service P95 floor (numeric P5)
 * @property {number}  [rfx_quality_pct_p50]     - RemoteFX frame quality median %
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
 * @property {string} [rd_session_collection]  - authoritative RD Session Collection membership; absent when unassigned
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
 * @property {string} [secret]         - HMAC secret for webhook signing (write-only; never returned)
 * @property {boolean} [has_secret]    - read-only marker that the backend has a saved secret
 * @property {boolean} [clear_secret]  - write-only flag: true wipes the saved secret on update
 * @property {string[]} triggers
 * @property {number} repeat_minutes   - 0 = once only
 * @property {boolean} [enabled]       - false = skip this target; absent/true = send (default)
 * @property {{server:string,triggers:string[]}[]} [server_exclusions] - Per-server trigger suppressions
 */

/**
 * EvtSpikeSettings is the dashboard's operator-safe view of the evtspike
 * config block. Every field here round-trips through GET/PUT
 * `/api/v1/settings`; baseline_path is intentionally omitted because it is
 * admin-only (lives in config.json).
 *
 * @typedef {Object} EvtSpikeSettings
 * @property {boolean} enabled                       Master detector toggle.
 * @property {number}  min_count                     Minimum event count in a 10-second bucket before scoring.
 * @property {number}  threshold                     Tail-probability threshold under which a bucket counts as anomalous.
 * @property {number}  cooldown_minutes              Default cooldown after a confirmed alert.
 * @property {Object.<string, number>} channel_cooldown_minutes Exact Windows Event Log channel cooldown overrides.
 * @property {number}  slot_maturity_observations    Observations required before a time-of-day slot is used directly.
 * @property {number}  persist_interval_seconds      Baseline persistence cadence (should be a multiple of 900 for slot rollover).
 * @property {number}  half_life_buckets             EWMA half-life in 10-second buckets.
 * @property {number}  prior_strength                Effective prior observation count for the Gamma prior.
 * @property {number}  mean_per_bucket_prior         Prior mean event count per 10-second bucket.
 * @property {string[]} disabled_channels           Case-insensitive channels from the curated default list to suppress.
 * @property {string[]} added_channels               Extra channels to subscribe in addition to the curated default.
 * @property {boolean} security_channel_enabled      Opt-in to the Windows Security log.
 */

/**
 * EvtSpikeSettingsPatch mirrors EvtSpikeSettings but every field is
 * optional. The backend uses pointer-style partial-update semantics
 * (absent = no change; explicit value = apply; out-of-range = 400).
 *
 * @typedef {Object} EvtSpikeSettingsPatch
 * @property {boolean} [enabled]
 * @property {number}  [min_count]
 * @property {number}  [threshold]
 * @property {number}  [cooldown_minutes]
 * @property {Object.<string, number>} [channel_cooldown_minutes]
 * @property {number}  [slot_maturity_observations]
 * @property {number}  [persist_interval_seconds]
 * @property {number}  [half_life_buckets]
 * @property {number}  [prior_strength]
 * @property {number}  [mean_per_bucket_prior]
 * @property {string[]} [disabled_channels]
 * @property {string[]} [added_channels]
 * @property {boolean} [security_channel_enabled]
 */

/**
 * @typedef {Object} UpdateSettings
 * @property {boolean} enabled
 * @property {'stable'|'prerelease'} channel
 * @property {string} poll_interval - Go duration string, minimum 1h
 */

/**
 * @typedef {Object} Settings
 * @property {number} grace_period              - grace period in minutes
 * @property {number} poll_interval             - safety-net poll interval in seconds (10–86400)
 * @property {number} session_warning_threshold
 * @property {string} rd_connection_broker      - blank uses the local DrainCtl service host only when it is the Connection Broker
 * @property {PerfMonitoringConfig} performance
 * @property {NotifyTarget[]} notifications
 * @property {string[]} notification_exclusions - canonical hosts suppressed across every target and trigger
 * @property {EvtSpikeSettings} [evtspike]
 * @property {UpdateSettings} [update]
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
 * Paths starting with `/api/` are used verbatim so callers can target
 * sibling surfaces like `/api/evtspike/...`; other paths are prepended
 * with `BASE` (`/api/v1`).
 *
 * @param {string} path - Path relative to BASE (e.g. `/health`) or an absolute `/api/...` path
 * @param {RequestInit} [options]
 * @returns {Promise<Response>}
 */
async function apiFetch(path, options = {}) {
    const timeoutController = new AbortController();
    const timer = setTimeout(() => timeoutController.abort(), FETCH_TIMEOUT_MS);

    const { signal: callerSignal, ...rest } = options;
    const signal = callerSignal ? AbortSignal.any([timeoutController.signal, callerSignal]) : timeoutController.signal;

    const url = path.startsWith('/api/') ? path : `${BASE}${path}`;

    let response;
    try {
        response = await fetch(url, {
            credentials: 'include',
            ...rest,
            signal,
            headers: {
                Accept: 'application/json',
                ...options.headers,
            },
        });
    } catch (err) {
        if (err instanceof DOMException && err.name === 'AbortError') {
            // Distinguish caller-initiated abort from our own timeout abort
            // so callers can ignore their own cancellations without seeing them
            // reported as timeouts.
            if (callerSignal?.aborted) throw err;
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
            detail = body.error ?? body.message ?? '';
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
    /**
     * @param {number} status
     * @param {string} statusText
     * @param {string|{code?: string}} detail
     * @param {string} path
     */
    constructor(status, statusText, detail, path) {
        const safeDetail = typeof detail === 'object' ? detail.code ?? '' : detail;
        const message = `API ${status} ${statusText} — ${path}${safeDetail ? ': ' + safeDetail : ''}`;
        super(message);
        this.name = 'ApiError';
        this.status = status;
        this.statusText = statusText;
        this.detail = safeDetail;
        this.code = typeof detail === 'object' ? detail.code ?? '' : '';
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
 * POST /api/v1/servers/{host}/permanent-remove
 *
 * Removes a live server and writes its durable tombstone. The agent cannot
 * re-register until an operator restores the tombstone from Configuration.
 *
 * @param {string} host
 * @returns {Promise<{ok:boolean, host:string, permanent:true, removed_at:string, removed_by?:string}>}
 */
export async function permanentRemoveServer(host) {
    const res = await apiFetch(`/servers/${encodeURIComponent(host)}/permanent-remove`, {
        method: 'POST',
    });
    return await res.json();
}

// ---------------------------------------------------------------------------
// Batch operations (Servers-table multi-select toolbar actions)
//
// BatchOperations consumes the contracts owned by PermanentRemoval and
// ForceUpdate; the helpers below do not invent endpoints. Names and request
// shapes match the sibling sprint deliverables; if a sibling adjusts the
// wire shape we adjust these helpers in lockstep and leave the rest of the
// toolbar untouched.
// ---------------------------------------------------------------------------

/**
 * BatchPermanentRemoveResult mirrors the response body of
 * `POST /api/v1/servers/permanent-remove` (PermanentRemoval's batch endpoint).
 *
 * @typedef {Object} BatchPermanentRemoveError
 * @property {string} host
 * @property {number} status
 * @property {string} reason
 *
 * @typedef {Object} BatchPermanentRemoveResult
 * @property {string[]} removed
 * @property {string[]} skipped
 * @property {BatchPermanentRemoveError[]} errors
 */

/**
 * POST /api/v1/servers/permanent-remove
 *
 * Tombstone N hosts atomically per-host (no transactional rollback across
 * hosts; each host is its own atomic write into the SQLite tombstone table).
 * Hosts listed in `errors[]` failed and must NOT be considered removed; hosts
 * in `skipped[]` were never registered and may be ignored; hosts in
 * `removed[]` are durable gone.
 *
 * @param {string[]} hosts
 * @returns {Promise<BatchPermanentRemoveResult>}
 */
export async function permanentRemoveHosts(hosts) {
    const res = await apiFetch('/servers/permanent-remove', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ hosts }),
    });
    return /** @type {BatchPermanentRemoveResult} */ (await res.json());
}

/**
 * RemovedServer is a durable permanent-removal tombstone returned by
 * GET /api/v1/servers/removed.
 *
 * @typedef {Object} RemovedServer
 * @property {string} host
 * @property {string} removed_at
 * @property {string} [removed_by]
 * @property {string} [reason]
 * @property {boolean} permanent
 * @property {boolean} was_present
 */

/** @returns {Promise<RemovedServer[]>} */
export async function fetchRemovedServers() {
    const res = await apiFetch('/servers/removed');
    return /** @type {RemovedServer[]} */ (await res.json());
}

/**
 * Remove one durable tombstone. The agent must register again before it
 * returns to the live servers table.
 *
 * @param {string} host
 * @returns {Promise<{ok:boolean, host:string, restored_at:string, restored_by?:string, was_tombstoned:boolean}>}
 */
export async function restoreRemovedServer(host) {
    const res = await apiFetch(`/servers/${encodeURIComponent(host)}/restore`, {
        method: 'POST',
    });
    return await res.json();
}

/**
 * BatchForceUpdateResult mirrors the per-host response body of
 * `POST /api/v1/servers/{host}/force-update` (ForceUpdate's primary endpoint).
 * Per-host outcomes distinguish "accepted for delivery" from
 * "offline / unsupported / duplicate" so the dashboard can render partial
 * failure without re-querying.
 *
 * @typedef {'accepted'|'duplicate'|'offline'|'unsupported'|'invalid'} ForceUpdateOutcome
 *
 * @typedef {Object} ForceUpdateResult
 * @property {string} host
 * @property {string} command_id             - echoed from the request body
 * @property {ForceUpdateOutcome} outcome
 * @property {string} [accepted_at]          - ISO-8601 UTC; populated for accepted/duplicate
 * @property {string} [version]              - agent's last-reported version (for the offline/unsupported UI hint)
 * @property {string} [reason]               - human-readable detail when the outcome explains itself
 *
 * @typedef {Object} ForceUpdateHostError
 * @property {number} status                 - HTTP status code (404 / 422 / 403)
 * @property {string} reason                 - server-provided error text
 */

/**
 * POST /api/v1/servers/{host}/force-update
 *
 * Issues a force-update command to ONE host. ForceUpdate is the contract
 * owner for this endpoint; BatchOperations invokes it once per host from the
 * ServerTable's "Force update" toolbar action and renders the per-host
 * outcomes inline.
 *
 * `command_id` is REQUIRED for idempotency — the server dedupes on
 * (host, command_id) for 24 hours. The caller MUST mint a fresh UUID for
 * every distinct operation; replays with the same id are silently dropped
 * server-side and surfaced here as outcome: "duplicate".
 *
 * The HTTP request is synchronous: it returns once the backend has
 * accepted / classified the command. The actual updater run happens on
 * the agent and surfaces via SSE event type `force_update` (same shape,
 * keyed on `command_id`).
 *
 * Throws `ApiError` on transport failure or non-2xx that the helper cannot
 * classify (rare; the 200/outcome channel covers accepted/duplicate/
 * offline/unsupported).
 *
 * @param {string} host
 * @param {{ command_id: string, reason?: string }} opts
 * @returns {Promise<ForceUpdateResult>}
 */
export async function forceUpdateHost(host, { command_id, reason } = {}) {
    if (!command_id) {
        throw new Error('forceUpdateHost requires command_id for server-side idempotency');
    }
    const body = { command_id };
    if (reason) body.reason = reason;
    const res = await apiFetch(`/servers/${encodeURIComponent(host)}/force-update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    /** @type {ForceUpdateResult} */
    const result = await res.json();
    return { ...result, host };
}

/**
 * @typedef {Object} BatchForceUpdateErrorEntry
 * @property {string} host
 * @property {number} status
 * @property {string} reason
 *
 * @typedef {Object} BatchForceUpdateSummary
 * @property {number} accepted
 * @property {number} duplicate
 * @property {number} offline
 * @property {number} unsupported
 * @property {number} failed
 *
 * @typedef {Object} BatchForceUpdateResult
 * @property {string} command_id
 * @property {BatchForceUpdateSummary} summary
 * @property {ForceUpdateResult[]} results
 * @property {BatchForceUpdateErrorEntry[]} errors
 */

/**
 * Mint a fresh command_id for force-update. Uses crypto.randomUUID() where
 * available; falls back to a Math.random() UUIDv4-shaped string otherwise.
 * Kept inline (not a top-level helper) so the dependency on the runtime
 * API is visible at every call site.
 * @returns {string}
 */
function mintCommandId() {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
        return crypto.randomUUID();
    }
    // RFC 4122 v4 fallback for environments without crypto.randomUUID
    // (very old browsers, jsdom tests, etc.).
    const bytes = new Uint8Array(16);
    if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
        crypto.getRandomValues(bytes);
    } else {
        for (let i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256);
    }
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = [...bytes].map((b) => b.toString(16).padStart(2, '0'));
    return (
        hex.slice(0, 4).join('') +
        '-' +
        hex.slice(4, 6).join('') +
        '-' +
        hex.slice(6, 8).join('') +
        '-' +
        hex.slice(8, 10).join('') +
        '-' +
        hex.slice(10, 16).join('')
    );
}

/**
 * Fan-out helper for the Servers-table "Force update" toolbar action.
 * Invokes the per-host endpoint in parallel via Promise.allSettled and
 * returns an aggregated BatchForceUpdateResult shaped like the optional
 * batch route — even though we never call the batch route. The shape is
 * what the toolbar renders; using a single shape across single-host and
 * multi-host invocations keeps the rendering layer trivially consistent.
 *
 * Each call gets its OWN command_id so per-host dedupe remains effective
 * even when the operator clicks "Force update" twice on a 50-host fleet.
 *
 * @param {string[]} hosts
 * @param {{ reason?: string, signal?: AbortSignal }} [opts]
 * @returns {Promise<BatchForceUpdateResult>}
 */
export async function forceUpdateHosts(hosts, { reason, signal } = {}) {
    const results = await Promise.allSettled(
        hosts.map((host) => {
            const command_id = mintCommandId();
            return forceUpdateHost(host, { command_id, reason }).then((r) => {
                if (signal?.aborted) throw new DOMException('aborted', 'AbortError');
                return r;
            });
        }),
    );
    const accepted = [];
    const errors = [];
    const summary = { accepted: 0, duplicate: 0, offline: 0, unsupported: 0, failed: 0 };
    /** @type {ForceUpdateResult[]} */
    const out = [];
    for (let i = 0; i < hosts.length; i++) {
        const host = hosts[i];
        const r = results[i];
        if (r.status === 'fulfilled') {
            out.push(r.value);
            summary[r.value.outcome] = (summary[r.value.outcome] ?? 0) + 1;
            if (r.value.outcome === 'accepted') accepted.push(r.value);
        } else {
            const reason = r.reason?.detail ?? r.reason?.message ?? String(r.reason);
            const status = r.reason?.status ?? 0;
            errors.push({ host, status, reason });
            summary.failed++;
        }
    }
    return {
        command_id: '',
        summary,
        results: out,
        errors,
    };
}

// ---------------------------------------------------------------------------
// Per-server perf metrics history (sparkline seed)
// ---------------------------------------------------------------------------

/**
 * MetricsSeedSample is one record from GET /api/v1/metrics (the per-host seed route).
 * Field names match the JSON tags produced by the backend for sparkline cold-start seeding.
 *
 * @typedef {Object} MetricsSeedSample
 * @property {number} time         - Unix timestamp (ms)
 * @property {number} cpu          - Average CPU % for this host
 * @property {number} mem          - Memory used % for this host
 * @property {number} inputDelay   - Input delay (ms)
 * @property {number} sessions     - Total session count for this host
 * @property {number} diskQueue    - Disk queue length
 * @property {number} tcpRetrans   - TCP retransmits/sec
 */

/**
 * GET /api/v1/metrics
 *
 * Returns a bounded recent retained-history slice per host for dashboard consumers
 * that need per-host history on cold start (e.g. sparkline seeding).
 *
 * @param {object} [opts]
 * @param {Date|string} [opts.from]         - inclusive lower bound; omit for server default
 * @param {Date|string} [opts.to]           - exclusive upper bound; omit for server default
 * @param {'raw'} [opts.resolution]         - only `raw` is supported in the initial version
 * @param {string[]} [opts.counters]        - omit for all counters
 * @param {number} [opts.limit]             - max points per host; server-side bounded
 * @returns {Promise<Record<string, MetricsSeedSample[]>>}
 */
export async function fetchAllServerMetrics({ from, to, resolution, counters, limit } = {}) {
    const params = new URLSearchParams();
    if (from != null) params.set('from', from instanceof Date ? from.toISOString() : from);
    if (to != null) params.set('to', to instanceof Date ? to.toISOString() : to);
    if (resolution != null) params.set('resolution', resolution);
    if (counters && counters.length > 0) params.set('counters', counters.join(','));
    if (limit != null) params.set('limit', String(limit));
    const qs = params.toString();
    const res = await apiFetch(qs ? `/metrics?${qs}` : '/metrics');
    return /** @type {Record<string, MetricsSeedSample[]>} */ (await res.json());
}

/**
 * CounterSeries is one counter's parallel arrays inside a metrics response.
 * P50 is the exact median across participating fleet-host values per bucket;
 * for a single host it equals avg. For tier=raw, avg === min === max === p50
 * === the raw sample value.
 *
 * @typedef {Object} CounterSeries
 * @property {number[]} t   - Unix-ms timestamps (parallel to avg/min/max/p50).
 * @property {number[]} avg
 * @property {number[]} min
 * @property {number[]} max
 * @property {number[]} p50
 */

/**
 * MetricsResponse is the shape returned by GET /api/v1/metrics/{host}.
 * Per contracts/http-metrics.md: on empty windows the server returns 200
 * with series === {} and oldest_available/newest_available === null so the
 * UI can render the FR-019a "Collecting data…" state.
 *
 * @typedef {Object} MetricsResponse
 * @property {string} host
 * @property {'raw'|'1min'|'5min'|'hourly'} tier          - tier the server actually served (may differ from requested)
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
 * @param {'raw'|'1min'|'5min'|'hourly'|'auto'} [resolution='auto']
 * @param {string[]} [counters]                      - omitted → all known counters
 * @param {AbortSignal} [signal]                     - abort in-flight fetch when a newer zoom/pan supersedes it
 * @returns {Promise<MetricsResponse>}
 */
export async function fetchMetrics(host, from, to, resolution = 'auto', counters, signal) {
    const params = new URLSearchParams({
        from: from instanceof Date ? from.toISOString() : from,
        to: to instanceof Date ? to.toISOString() : to,
        resolution,
    });
    if (counters && counters.length > 0) {
        params.set('counters', counters.join(','));
    }
    const res = await apiFetch(`/metrics/${encodeURIComponent(host)}?${params}`, { signal });
    return /** @type {MetricsResponse} */ (await res.json());
}

/**
 * GET /api/v1/metrics/_fleet
 *
 * An omitted `hosts` list requests every registered host. When supplied, each
 * host is serialized as a separate `host=` parameter so the backend can
 * distinguish it from a comma-containing hostname and validate each member.
 *
 * @param {Date|string} from                         - inclusive lower bound
 * @param {Date|string} to                           - exclusive upper bound (must be > from)
 * @param {'raw'|'1min'|'5min'|'hourly'|'auto'} [resolution='auto']
 * @param {string[]} [counters]                      - omitted → all known counters
 * @param {AbortSignal} [signal]                     - abort in-flight fetch when a newer query supersedes it
 * @param {Iterable<string>} [hosts]                 - omitted or empty → all registered hosts
 * @returns {Promise<MetricsResponse>}
 */
export async function fetchFleetMetrics(from, to, resolution = 'auto', counters, signal, hosts) {
    const params = new URLSearchParams({
        from: from instanceof Date ? from.toISOString() : from,
        to: to instanceof Date ? to.toISOString() : to,
        resolution,
    });
    if (counters && counters.length > 0) params.set('counters', counters.join(','));
    if (hosts) {
        for (const host of hosts) params.append('host', host);
    }
    const res = await apiFetch(`/metrics/_fleet?${params}`, { signal });
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
const DEFAULT_MEMORY_WARN_USED_PCT = 80;
const DEFAULT_MEMORY_CRIT_USED_PCT = 90;

/**
 * Convert Go's memory threshold (% free) to the dashboard's % used without
 * transforming the -1 disabled sentinel into an impossible 101% threshold.
 * @param {number|null|undefined} value
 * @param {number} defaultUsed
 */
function memoryFreeToUsed(value, defaultUsed) {
    if (value === -1) return -1;
    if (value == null || value === 0) return defaultUsed;
    return 100 - value;
}

/**
 * Convert a dashboard memory threshold (% used) to Go's % free wire format.
 * @param {number|null|undefined} value
 */
function memoryUsedToFree(value) {
    if (value === -1) return -1;
    if (value == null || value === 0) return 0;
    return 100 - value;
}

/**
 * Normalize a freshly decoded REST or SSE settings payload for UI use.
 * @param {Settings} config
 * @returns {Settings}
 */
export function settingsFromWire(config) {
    if (config.performance) {
        config.performance.mem_warn_pct = memoryFreeToUsed(
            config.performance.mem_warn_pct,
            DEFAULT_MEMORY_WARN_USED_PCT,
        );
        config.performance.mem_crit_pct = memoryFreeToUsed(
            config.performance.mem_crit_pct,
            DEFAULT_MEMORY_CRIT_USED_PCT,
        );
    }
    if (!config.update) {
        config.update = { enabled: false, channel: 'stable', poll_interval: '24h0m0s' };
    }
    return config;
}

/**
 * GET /api/v1/settings
 * @returns {Promise<Settings>}
 */
export async function fetchSettings() {
    const res = await apiFetch('/settings');
    return settingsFromWire(/** @type {Settings} */ (await res.json()));
}

/**
 * PUT /api/v1/settings
 * Returns {ok: true} on success — does NOT return the saved config.
 * Callers should treat the local config as authoritative after a successful save.
 *
 * Notification targets are stripped from the payload — they are managed via
 * the per-target CRUD endpoints (addNotificationTarget, updateNotificationTarget,
 * deleteNotificationTarget) and saved atomically. Including them here would
 * either be a no-op or, in the worst case, race against an in-flight CRUD.
 *
 * @param {Settings} config
 * @returns {Promise<void>}
 */
export async function saveSettings(config) {
    // Deep-clone to avoid mutating the UI state, then invert mem % used → % free for Go.
    const payload = JSON.parse(JSON.stringify(config));
    if (payload.performance) {
        payload.performance.mem_warn_pct = memoryUsedToFree(payload.performance.mem_warn_pct);
        payload.performance.mem_crit_pct = memoryUsedToFree(payload.performance.mem_crit_pct);
    }
    // Backend treats absent `notifications` as "no change", so omitting it
    // keeps targets entirely in the per-target endpoints' lane.
    delete payload.notifications;
    // Connection Broker configuration is persisted only through the elevated
    // drainctl broker-setup service path, after it validates discovery under
    // the service identity.
    delete payload.rd_connection_broker;
    await apiFetch('/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
    });
}

/**
 * Toggle the global notification exclusion for one host.
 * @param {string} host
 * @returns {Promise<{host: string, excluded: boolean}>}
 */
export async function toggleNotificationExclusion(host) {
    const res = await apiFetch(`/settings/notification-exclusions/${encodeURIComponent(host)}`, {
        method: 'POST',
    });
    return /** @type {{host: string, excluded: boolean}} */ (await res.json());
}

/**
 * @typedef {Object} NotifyTargetsResponse
 * @property {NotifyTarget[]} notifications
 */

/**
 * Strip frontend-only fields from a target before sending to the backend.
 * `id` is a UUID generated for Svelte list keying; `has_secret` is a read-only
 * marker the GET response sets. Both would be silently ignored by Go's JSON
 * decoder, but dropping them keeps the wire shape honest.
 * @param {NotifyTarget} target
 * @returns {Object}
 */
function stripClientFields(target) {
    const { id, has_secret, ...wire } = target;
    return wire;
}

/**
 * POST /api/v1/settings/notifications
 * Append a new notification target. Returns the full updated targets list.
 * @param {NotifyTarget} target
 * @returns {Promise<NotifyTarget[]>}
 */
export async function addNotificationTarget(target) {
    const res = await apiFetch('/settings/notifications', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(stripClientFields(target)),
    });
    const body = /** @type {NotifyTargetsResponse} */ (await res.json());
    return body.notifications ?? [];
}

/**
 * PUT /api/v1/settings/notifications/{idx}
 * Replace the target at the given index. Set `target.clear_secret = true` to
 * wipe a saved secret; an empty `secret` field preserves the existing one.
 * Returns the full updated targets list.
 * @param {number} idx
 * @param {NotifyTarget} target
 * @returns {Promise<NotifyTarget[]>}
 */
export async function updateNotificationTarget(idx, target) {
    const res = await apiFetch(`/settings/notifications/${idx}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(stripClientFields(target)),
    });
    const body = /** @type {NotifyTargetsResponse} */ (await res.json());
    return body.notifications ?? [];
}

/**
 * DELETE /api/v1/settings/notifications/{idx}
 * Remove the target at the given index. Returns the full updated targets list.
 * @param {number} idx
 * @returns {Promise<NotifyTarget[]>}
 */
export async function deleteNotificationTarget(idx) {
    const res = await apiFetch(`/settings/notifications/${idx}`, { method: 'DELETE' });
    const body = /** @type {NotifyTargetsResponse} */ (await res.json());
    return body.notifications ?? [];
}

// ---------------------------------------------------------------------------
// Maintenance status
// ---------------------------------------------------------------------------

/**
 * MaintenanceJob mirrors one entry in the GET /api/v1/maintenance/status payload.
 *
 * @typedef {Object} MaintenanceJob
 * @property {string} name                       - aggregator_5min | aggregator_hourly | retention | jsonl_migration | drift_reconciliation (new names may appear)
 * @property {string} started                    - ISO-8601 UTC
 * @property {string} finished                   - ISO-8601 UTC
 * @property {number} duration_ms
 * @property {'success'|'failure'|'skipped'} outcome
 * @property {string} reason                     - populated on failure
 * @property {number} rows_affected
 * @property {boolean} overdue
 * @property {number} expected_interval_seconds  - 0 sentinel for one-shot startup jobs (never overdue)
 */

/**
 * @typedef {Object} MaintenanceResponse
 * @property {MaintenanceJob[]} jobs
 * @property {string} server_time                - server's own clock; clients should use this instead of Date.now() to avoid skew
 */

/**
 * GET /api/v1/maintenance/status
 * @returns {Promise<MaintenanceResponse>}
 */
export async function fetchMaintenance() {
    const res = await apiFetch('/maintenance/status');
    return /** @type {MaintenanceResponse} */ (await res.json());
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

// ---------------------------------------------------------------------------
// Event Log Anomaly Detection (evtspike)
// ---------------------------------------------------------------------------

/**
 * @typedef {import('./types.js').DetectorStatus} DetectorStatus
 * @typedef {import('./types.js').RecentSpike} RecentSpike
 */

/**
 * GET /api/evtspike/status?host=<host>
 *
 * Returns the current evtspike detector status for one registered host.
 * 200 with `state: "disabled"` when the feature is off; 404 for unknown host.
 *
 * @param {string} host
 * @returns {Promise<DetectorStatus>}
 */
export async function fetchEvtSpikeStatus(host) {
    const res = await apiFetch(`/api/evtspike/status?host=${encodeURIComponent(host)}`);
    return /** @type {DetectorStatus} */ (await res.json());
}

/**
 * GET /api/evtspike/spikes?host=<host>&limit=<1..50>
 *
 * Returns the most-recent confirmed spikes for one host, newest first, from
 * the SQLite event_spikes table. Empty array for a registered host with no
 * spikes — not a 404. Default limit is 20; the server clamps to [1, 50].
 *
 * @param {string} host
 * @param {number} [limit=20]
 * @param {AbortSignal} [signal]
 * @returns {Promise<RecentSpike[]>}
 */
export async function fetchRecentSpikes(host, limit = 20, signal) {
    const params = new URLSearchParams({
        host,
        limit: String(limit),
    });
    const res = await apiFetch(`/api/evtspike/spikes?${params}`, { signal });
    return /** @type {RecentSpike[]} */ (await res.json());
}

/**
 * SpikeRangeResponse is the bounded range-query envelope. `total` is the exact
 * count before the 500-row plotting cap; `truncated` reports whether rows were
 * omitted from `spikes`; `as_of_id` is the maximum in-window ID included in
 * the read snapshot (or 0 when no rows matched).
 *
 * @typedef {Object} SpikeRangeResponse
 * @property {RecentSpike[]} spikes
 * @property {number} total
 * @property {boolean} truncated
 * @property {number} as_of_id
 */

/**
 * GET /api/evtspike/spikes?host=<host>&from=<iso>&to=<iso>
 *
 * Returns confirmed spikes whose window_start falls in [from, to), newest
 * first. `spikes` contains at most 500 rows while `total` remains exact.
 *
 * @param {string} host
 * @param {Date} from
 * @param {Date} to
 * @param {AbortSignal} [signal]
 * @returns {Promise<SpikeRangeResponse>}
 */
export async function fetchSpikeRange(host, from, to, signal) {

    const params = new URLSearchParams({
        host,
        from: from.toISOString(),
        to: to.toISOString(),
    });
    const res = await apiFetch(`/api/evtspike/spikes?${params}`, { signal });
    return /** @type {SpikeRangeResponse} */ (await res.json());
}
// ---------------------------------------------------------------------------
// Investigation and deterministic session-drop APIs
// ---------------------------------------------------------------------------

const ATTEMPT_STATES = new Set(['queued', 'running', 'completed', 'insufficient_evidence', 'failed']);
const TERMINAL_REASONS = new Set([
    '', 'authentication_failed', 'configuration_disabled', 'configuration_invalid',
    'evidence_unavailable', 'interrupted', 'network_error', 'provider_rate_limited',
    'provider_request_rejected', 'provider_refused', 'redirect_refused', 'request_limit',
    'response_incomplete', 'response_invalid', 'response_limit', 'storage_unavailable',
    'timeout', 'upstream_error',
]);
const DECIMAL_ID = /^[1-9][0-9]{0,18}$/;
const RFC3339_MILLISECONDS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const OMITTION_CODES = new Set(['pre_upgrade_context', 'retention_expired', 'freshness_unavailable', 'drain_unavailable', 'detector_unavailable', 'local_points', 'fleet_points', 'local_aggregate', 'fleet_aggregate']);
const REPORT_CATEGORIES = new Set(['additional_time_series', 'host_health_detail', 'service_state', 'authentication_detail', 'network_dependency_detail', 'change_or_maintenance_context', 'fleet_comparison', 'other']);
const CHECK_TYPES = new Set(['inspect_retained_metrics', 'verify_service_state', 'verify_authentication_state', 'verify_network_or_dependency', 'verify_change_or_maintenance_context', 'compare_fleet', 'collect_additional_observation']);

function dto(condition, message) {
    if (!condition) throw new Error(`Invalid investigation response: ${message}`);
}

function closed(value, keys, message) {
    dto(value && typeof value === 'object' && !Array.isArray(value), message);
    const actual = Object.keys(value).sort();
    const expected = [...keys].sort();
    dto(actual.length === expected.length && actual.every((key, index) => key === expected[index]), `${message} members`);
}

function rfc3339(value, nullable = false) {
    if (nullable && value === null) return value;
    dto(typeof value === 'string' && RFC3339_MILLISECONDS.test(value) && new Date(value).toISOString() === value, 'UTC millisecond timestamp');
    return value;
}

function sourceLink(value) {
    closed(value, ['source_kind', 'source_id'], 'source');
    dto(['event_spike', 'session_drop'].includes(value.source_kind) && DECIMAL_ID.test(value.source_id), 'source');
    return value;
}

export function validateInvestigationAttempt(value) {
    closed(value, ['attempt_id', 'attempt_number', 'initiation', 'retry_of_attempt_id', 'state', 'created_at', 'started_at', 'send_authorized_at', 'send_completed_at', 'completed_at', 'terminal_reason', 'evidence_version', 'omission_codes'], 'attempt');
    dto(DECIMAL_ID.test(value.attempt_id) && Number.isInteger(value.attempt_number) && value.attempt_number > 0, 'attempt identity');
    dto(['automatic', 'manual', 'retry'].includes(value.initiation) && (value.retry_of_attempt_id === null || DECIMAL_ID.test(value.retry_of_attempt_id)), 'attempt lineage');
    dto(ATTEMPT_STATES.has(value.state) && TERMINAL_REASONS.has(value.terminal_reason), 'attempt state');
    for (const key of ['created_at', 'started_at', 'send_authorized_at', 'send_completed_at', 'completed_at']) rfc3339(value[key], key !== 'created_at');
    dto(value.evidence_version === 1 && Array.isArray(value.omission_codes) && value.omission_codes.length <= 9 && value.omission_codes.every((code) => OMITTION_CODES.has(code)), 'attempt evidence');
    return value;
}

function validateProvider(value) {
    closed(value, ['profile', 'endpoint', 'model', 'access_enabled', 'acknowledged', 'privacy_acknowledgement_version', 'automatic_enabled', 'has_credential'], 'provider');
    dto(value.profile === 'openai_responses' && value.endpoint === 'https://api.openai.com/v1/responses' && value.model === 'gpt-6-astra', 'provider profile');
    dto(typeof value.access_enabled === 'boolean' && typeof value.acknowledged === 'boolean' && typeof value.automatic_enabled === 'boolean' && typeof value.has_credential === 'boolean' && typeof value.privacy_acknowledgement_version === 'string', 'provider flags');
}

function validateSessionDropSettings(value, includeDetector) {
    const fields = ['lower_tail_threshold', 'minimum_drop_sessions', 'minimum_drop_percent', 'baseline_half_life_hours', 'cooldown_minutes'];
    closed(value, includeDetector ? [...fields, 'detector'] : fields, 'session-drop settings');
    dto(Number.isFinite(value.lower_tail_threshold) && value.lower_tail_threshold >= 1e-9 && value.lower_tail_threshold <= .1 && Number.isInteger(value.minimum_drop_sessions) && value.minimum_drop_sessions >= 1 && value.minimum_drop_sessions <= 1_000_000 && Number.isFinite(value.minimum_drop_percent) && value.minimum_drop_percent >= 1 && value.minimum_drop_percent <= 99 && Number.isInteger(value.baseline_half_life_hours) && value.baseline_half_life_hours >= 24 && value.baseline_half_life_hours <= 8760 && Number.isInteger(value.cooldown_minutes) && value.cooldown_minutes >= 1 && value.cooldown_minutes <= 1440, 'session-drop setting ranges');
    if (includeDetector) {
        closed(value.detector, ['slots_per_day', 'confirmation_required', 'confirmation_window', 'slot_maturity_eligible_days', 'fallback_minimum_observations', 'fallback_minimum_span_hours'], 'detector constants');
        dto(value.detector.slots_per_day === 96 && value.detector.confirmation_required === 2 && value.detector.confirmation_window === 3 && value.detector.slot_maturity_eligible_days === 7 && value.detector.fallback_minimum_observations === 20 && value.detector.fallback_minimum_span_hours === 24, 'detector constants');
    }
}

export function validateInvestigationSettings(value) {
    closed(value, ['provider', 'session_drop'], 'settings');
    validateProvider(value.provider);
    validateSessionDropSettings(value.session_drop, true);
    return value;
}

export function validateInvestigationStatus(value) {
    closed(value, ['operational_state', 'provider', 'attempt_counts', 'worker', 'latest_failure'], 'status');
    dto(['disabled', 'configured', 'ready', 'automatic_enabled', 'degraded', 'failing'].includes(value.operational_state), 'status');
    validateProvider(value.provider);
    closed(value.attempt_counts, ['queued', 'running', 'completed', 'insufficient_evidence', 'failed'], 'attempt counts');
    dto(Object.values(value.attempt_counts).every((count) => Number.isInteger(count) && count >= 0) && value.attempt_counts.queued + value.attempt_counts.running <= 100, 'attempt counts');
    closed(value.worker, ['workers', 'max_nonterminal_attempts', 'requests_per_minute', 'burst', 'request_timeout_seconds'], 'worker');
    dto(value.worker.workers === 1 && value.worker.max_nonterminal_attempts === 100 && value.worker.requests_per_minute === 10 && value.worker.burst === 2 && value.worker.request_timeout_seconds === 30, 'worker');
    if (value.latest_failure !== null) {
        closed(value.latest_failure, ['reason', 'at'], 'latest failure');
        dto(TERMINAL_REASONS.has(value.latest_failure.reason) && value.latest_failure.reason !== '', 'latest failure');
        rfc3339(value.latest_failure.at);
    }
    return value;
}

function validateReport(report) {
    closed(report, ['result_version', 'summary', 'overall_assessment', 'evidence_sufficiency', 'human_review_required', 'hypotheses', 'missing_evidence', 'recommended_diagnostic_checks'], 'report');
    dto(report.result_version === 1 && ['insufficient_evidence', 'indeterminate', 'likely_localized_operational_issue', 'likely_fleet_wide_operational_issue', 'likely_expected_or_maintenance_related'].includes(report.overall_assessment) && ['insufficient', 'partial', 'sufficient'].includes(report.evidence_sufficiency) && typeof report.human_review_required === 'boolean', 'report fields');
    closed(report.summary, ['text_kind', 'text', 'fact_ids'], 'summary');
    dto(report.summary.text_kind === 'untrusted_summary' && typeof report.summary.text === 'string' && Array.isArray(report.summary.fact_ids), 'summary');
    dto(Array.isArray(report.hypotheses) && Array.isArray(report.missing_evidence) && Array.isArray(report.recommended_diagnostic_checks), 'report arrays');
    report.hypotheses.forEach((item, index) => { closed(item, ['rank', 'confidence', 'text_kind', 'text', 'supporting_fact_ids', 'contradicting_fact_ids'], 'hypothesis'); dto(item.rank === index + 1 && ['low', 'medium', 'high'].includes(item.confidence) && item.text_kind === 'untrusted_hypothesis' && Array.isArray(item.supporting_fact_ids) && Array.isArray(item.contradicting_fact_ids), 'hypothesis'); });
    report.missing_evidence.forEach((item) => { closed(item, ['category', 'text_kind', 'text', 'related_fact_ids'], 'missing evidence'); dto(REPORT_CATEGORIES.has(item.category) && item.text_kind === 'untrusted_missing_evidence' && Array.isArray(item.related_fact_ids), 'missing evidence'); });
    report.recommended_diagnostic_checks.forEach((item, index) => { closed(item, ['rank', 'check_type', 'text_kind', 'text', 'fact_ids', 'hypothesis_ranks'], 'diagnostic check'); dto(item.rank === index + 1 && CHECK_TYPES.has(item.check_type) && item.text_kind === 'untrusted_diagnostic_check' && Array.isArray(item.fact_ids) && Array.isArray(item.hypothesis_ranks), 'diagnostic check'); });
}

export async function fetchInvestigationSettings() {
    return validateInvestigationSettings(await (await apiFetch('/investigation/settings')).json());
}

function validateInvestigationSettingsRequest(value) {
    closed(value, ['provider', 'session_drop'], 'settings request');
    closed(value.provider, ['access_enabled', 'privacy_acknowledgement', 'automatic_enabled', 'credential'].filter((key) => key !== 'privacy_acknowledgement' || Object.hasOwn(value.provider, key)), 'settings provider request');
    dto(typeof value.provider.access_enabled === 'boolean' && typeof value.provider.automatic_enabled === 'boolean', 'settings provider flags');
    if (Object.hasOwn(value.provider, 'privacy_acknowledgement')) {
        closed(value.provider.privacy_acknowledgement, Object.keys(mockPrivacyAcknowledgement()), 'privacy acknowledgement');
        dto(value.provider.privacy_acknowledgement.version === 'openai_responses_privacy_v1' && Object.entries(value.provider.privacy_acknowledgement).every(([key, entry]) => key === 'version' || entry === true), 'privacy acknowledgement');
    }
    closed(value.provider.credential, value.provider.credential.operation === 'replace' ? ['operation', 'value'] : ['operation'], 'credential command');
    dto(['preserve', 'replace', 'clear'].includes(value.provider.credential.operation) && (value.provider.credential.operation !== 'replace' || typeof value.provider.credential.value === 'string' && value.provider.credential.value.length > 0), 'credential command');
    validateSessionDropSettings(value.session_drop, false);
}

function mockPrivacyAcknowledgement() {
    return { version: true, third_party_subprocessors: true, no_training_without_opt_in: true, default_abuse_monitoring_up_to_30_days: true, store_false_application_state_only: true, temporary_prompt_cache_possible: true, zdr_mam_separate_approval: true, audit_days_local_only: true, global_endpoint_no_regional_guarantee: true };
}

export async function saveInvestigationSettings(settings) {
    validateInvestigationSettingsRequest(settings);
    const res = await apiFetch('/investigation/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(settings) });
    return validateInvestigationSettings(await res.json());
}

export async function fetchInvestigationStatus() {
    return validateInvestigationStatus(await (await apiFetch('/investigation/status')).json());
}

export async function fetchInvestigationHistory(sourceKind, sourceId, { limit = 100, afterAttemptNumber } = {}) {
    dto(['event_spike', 'session_drop'].includes(sourceKind) && DECIMAL_ID.test(String(sourceId)), 'source request');
    const params = new URLSearchParams({ limit: String(limit) });
    if (afterAttemptNumber != null) params.set('after_attempt_number', String(afterAttemptNumber));
    const value = await (await apiFetch(`/investigation/sources/${sourceKind}/${sourceId}/attempts?${params}`)).json();
    closed(value, ['source', 'attempts', 'next_after_attempt_number'], 'attempt history');
    sourceLink(value.source);
    dto(Array.isArray(value.attempts) && (value.next_after_attempt_number === null || DECIMAL_ID.test(value.next_after_attempt_number)), 'attempt history');
    value.attempts.forEach(validateInvestigationAttempt);
    return { ...value, attempts: value.attempts.map((attempt) => ({ ...attempt, source: value.source })) };
}

export async function createInvestigation(sourceKind, sourceId) {
    const value = await (await apiFetch(`/investigation/sources/${sourceKind}/${sourceId}/attempts`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })).json();
    validateInvestigationAttempt(value);
    return { ...value, source: { source_kind: sourceKind, source_id: String(sourceId) } };
}

export async function retryInvestigation(attemptId, source) {
    dto(DECIMAL_ID.test(String(attemptId)), 'retry request');
    sourceLink(source);
    const value = await (await apiFetch(`/investigation/attempts/${attemptId}/retry`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })).json();
    validateInvestigationAttempt(value);
    return { ...value, source };
}

export async function fetchInvestigationDetail(attemptId) {
    dto(DECIMAL_ID.test(String(attemptId)), 'attempt request');
    const value = await (await apiFetch(`/investigation/attempts/${attemptId}`)).json();
    closed(value, ['attempt'], 'attempt detail');
    const attempt = value.attempt;
    closed(attempt, ['attempt_id', 'attempt_number', 'source', 'initiation', 'retry_of_attempt_id', 'state', 'created_at', 'started_at', 'send_authorized_at', 'send_completed_at', 'completed_at', 'terminal_reason', 'evidence_version', 'omission_codes', 'evidence', 'result', 'provenance'], 'attempt detail');
    sourceLink(attempt.source);
    const { source, evidence, result, provenance, ...summary } = attempt;
    validateInvestigationAttempt(summary);
    closed(attempt.evidence, ['version', 'snapshot_kind', 'snapshot_at', 'window_start', 'window_end', 'fact_ids', 'omission_codes'], 'evidence');
    dto(attempt.evidence.version === 1 && ['available', 'unavailable'].includes(attempt.evidence.snapshot_kind) && Array.isArray(attempt.evidence.fact_ids) && Array.isArray(attempt.evidence.omission_codes), 'evidence');
    rfc3339(attempt.evidence.snapshot_at); rfc3339(attempt.evidence.window_start); rfc3339(attempt.evidence.window_end);
    if (attempt.result !== null) validateReport(attempt.result);
    if (attempt.provenance !== null) {
        const p = attempt.provenance;
        closed(p, ['provider_profile', 'provider_endpoint', 'requested_model', 'response_format', 'store', 'send_authorized_at', 'send_completed_at', 'request_header_bytes', 'request_body_bytes', 'response_header_bytes', 'response_body_bytes', 'validation_outcome'], 'provenance');
        dto(p.provider_profile === 'openai_responses' && p.provider_endpoint === 'https://api.openai.com/v1/responses' && p.requested_model === 'gpt-6-astra' && p.response_format === 'anomaly_investigation_v1' && p.store === false && ['accepted', 'insufficient_evidence'].includes(p.validation_outcome), 'provenance');
        rfc3339(p.send_authorized_at); rfc3339(p.send_completed_at);
    }
    return value;
}

export async function fetchSessionDrops({ limit = 50, before } = {}) {
    const params = new URLSearchParams({ limit: String(limit) });
    if (before) params.set('before', before);
    const value = await (await apiFetch(`/session-drops?${params}`)).json();
    closed(value, ['items', 'next_before'], 'session-drop list');
    dto(Array.isArray(value.items) && (value.next_before === null || DECIMAL_ID.test(value.next_before)), 'session-drop list');
    value.items.forEach((item) => {
        closed(item, ['id', 'source_kind', 'registered_host', 'confirmed_at', 'classification', 'investigation_eligible', 'observed_total_sessions', 'reference_total_sessions', 'expected_total_sessions', 'absolute_loss_sessions', 'relative_loss', 'tail_probability', 'baseline_model_version', 'baseline_scope', 'slot_index', 'slot_mature_days', 'confirmation_window_size', 'confirmation_count'], 'session-drop item');
        dto(DECIMAL_ID.test(item.id) && item.source_kind === 'session_drop' && typeof item.registered_host === 'string' && ['unexplained', 'drain_associated', 'unknown_context'].includes(item.classification) && typeof item.investigation_eligible === 'boolean' && item.baseline_model_version === 'gamma_poisson_lower_v1' && ['slot', 'all_hours'].includes(item.baseline_scope), 'session-drop item');
        rfc3339(item.confirmed_at);
    });
    return value;
}

export async function fetchSessionDropDetail(id) {
    dto(DECIMAL_ID.test(String(id)), 'session-drop request');
    const value = await (await apiFetch(`/session-drops/${id}`)).json();
    closed(value, ['source', 'attempts'], 'session-drop detail');
    dto(Array.isArray(value.attempts), 'session-drop attempts');
    const source = value.source;
    closed(source, ['id', 'source_kind', 'registered_host', 'confirmed_at', 'classification', 'investigation_eligible', 'observed_total_sessions', 'reference_total_sessions', 'expected_total_sessions', 'absolute_loss_sessions', 'relative_loss', 'tail_probability', 'baseline_model_version', 'baseline_scope', 'slot_index', 'slot_mature_days', 'confirmation_window_size', 'confirmation_count', 'confirmation_started_at', 'confirmation_ended_at', 'confirmation_flags', 'freshness_context', 'drain_context', 'confirmation_ended_report_epoch_ms'], 'session-drop source detail');
    dto(DECIMAL_ID.test(source.id) && source.source_kind === 'session_drop' && ['unexplained', 'drain_associated', 'unknown_context'].includes(source.classification) && typeof source.investigation_eligible === 'boolean' && source.baseline_model_version === 'gamma_poisson_lower_v1' && ['slot', 'all_hours'].includes(source.baseline_scope), 'session-drop source detail');
    dto(source.confirmation_window_size >= 2 && source.confirmation_window_size <= 3 && Array.isArray(source.confirmation_flags) && source.confirmation_flags.length === source.confirmation_window_size && source.confirmation_flags.every((flag) => typeof flag === 'boolean') && source.confirmation_flags.filter(Boolean).length === source.confirmation_count && source.freshness_context === 'fresh' && ['none', 'overlap', 'post_horizon', 'unknown'].includes(source.drain_context) && /^[0-9]+$/.test(source.confirmation_ended_report_epoch_ms), 'confirmation flags');
    for (const key of ['confirmed_at', 'confirmation_started_at', 'confirmation_ended_at']) rfc3339(source[key]);
    value.attempts.forEach(validateInvestigationAttempt);
    return value;
}
