/**
 * types.js — Shared evtspike JSDoc typedefs.
 *
 * Mirrors Go's `internal/evtspike.DetectorStatus` and `internal/evtspike.RecentSpikeEntry`.
 * Authoritative wire shape: `specs/006-evtspike-detection/data-model.md` §6 and §7
 * and `specs/006-evtspike-detection/contracts/dashboard-sse-events.md`.
 *
 * This module has no runtime exports — it exists only so other modules can
 * reference these types via `@typedef {import('./types.js').DetectorStatus}`.
 */

/**
 * DetectorStatus is the per-host evtspike detector state published by
 * GET /api/evtspike/status and by `detector_status` SSE events.
 *
 * A response with `state: "disabled"` is 200 OK, not 503 — disabled is a
 * configuration choice, not a transient failure.
 *
 * @typedef {Object} DetectorStatus
 * @property {string} host
 * @property {'healthy'|'training'|'disabled'|'error'} state
 * @property {number} enabled_channels
 * @property {number} mature_channels
 * @property {string} [error_reason]     - populated only when state === 'error'
 * @property {string} [last_spike_at]    - ISO-8601 UTC; omitted if no spike ever observed
 */

/**
 * RecentSpike is one entry from GET /api/evtspike/spikes and from
 * `recent_spike` SSE events. Mirrors Go's `internal/evtspike.RecentSpikeEntry`
 * (SpikePayload + server-assigned monotonic ID).
 *
 * @typedef {Object} RecentSpike
 * @property {number} id                 - monotonic int64, stable within a service run
 * @property {string} host
 * @property {string} channel
 * @property {string} window_start       - ISO-8601 UTC
 * @property {string} window_end         - ISO-8601 UTC
 * @property {number} observed
 * @property {number} expected
 * @property {number} tail_probability
 * @property {number} confirmation_count - 2 or 3 (per data-model.md §5)
 * @property {string} first_seen_at      - ISO-8601 UTC
 */

export {};
