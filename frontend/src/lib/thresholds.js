/**
 * thresholds.js — Threshold logic for ring gauges and metric colorisation.
 *
 * No Svelte imports — this module is plain JS and can be used anywhere,
 * including server-side rendering contexts and unit tests.
 */

// ---------------------------------------------------------------------------
// Default thresholds
// ---------------------------------------------------------------------------

/**
 * Default warn/crit thresholds keyed by metric name.
 *
 * Units match the Server.perf field units:
 *   cpu           → percentage (0–100)
 *   mem           → percentage used (0–100, derived from 1 - mem_avail_mb/mem_total_mb)
 *                   Defaults mirror Go's PerformanceConfig defaults inverted from % free:
 *                   Go MemWarnPct=20 (% free) → 80 (% used); MemCritPct=10 → 90.
 *   sessions      → percentage of session_warning_threshold (0–100)
 *   diskQueue     → average disk queue depth (unitless)
 *   inputDelay    → milliseconds
 *   tcpRetransmits → count/sec
 *
 * @type {Record<string, {warn: number, crit: number}>}
 */
export const DEFAULTS = {
    cpu: { warn: 70, crit: 85 },
    mem: { warn: 80, crit: 90 },
    sessions: { warn: 80, crit: 95 },
    diskQueue: { warn: 2, crit: 5 },
    inputDelay: { warn: 50, crit: 100 },
    tcpRetransmits: { warn: 5, crit: 10 },
};

// ---------------------------------------------------------------------------
// Color resolution
// ---------------------------------------------------------------------------

/**
 * Return a semantic colour token for a metric value given warn/crit thresholds.
 *
 * For 'higher-worse' metrics (default): value below warn → green, at or above
 * warn → amber, at or above crit → red.
 *
 * For 'lower-worse' metrics (e.g. a score where low is bad): value above warn
 * → green, at or below warn → amber, at or below crit → red.
 *
 * Returns 'neutral' when value is null, undefined, or NaN (no data).
 *
 * @param {number|null|undefined} value
 * @param {number} warn
 * @param {number} crit
 * @param {'higher-worse'|'lower-worse'} [direction='higher-worse']
 * @returns {'green'|'amber'|'red'|'neutral'}
 */
export function getThresholdColor(value, warn, crit, direction = 'higher-worse') {
    if (value == null || !Number.isFinite(value)) return 'neutral';

    if (direction === 'lower-worse') {
        if (crit >= 0 && value <= crit) return 'red';
        if (warn >= 0 && value <= warn) return 'amber';
        return 'green';
    }

    if (crit >= 0 && value >= crit) return 'red';
    if (warn >= 0 && value >= warn) return 'amber';
    return 'green';
}

// ---------------------------------------------------------------------------
// Threshold resolution
// ---------------------------------------------------------------------------

/**
 * @typedef {Object} Thresholds
 * @property {number} warn
 * @property {number} crit
 */

/** @param {number|null|undefined} value @param {number} fallback */
function resolveConfiguredThreshold(value, fallback) {
    if (value === -1) return -1;
    return value > 0 ? value : fallback;
}

/**
 * Resolve thresholds for a metric, preferring values from the server's
 * perf_monitoring config over the built-in DEFAULTS.
 *
 * Recognised config overrides:
 *   cpu         → cpu_warn_pct / cpu_crit_pct           (% used, 0 = use default)
 *   mem         → mem_warn_pct / mem_crit_pct           (% used, normalized by the API layer)
 *   inputDelay  → input_delay_warn_ms / input_delay_crit_ms (ms, 0 = use default)
 *
 * A value of 0 in perfConfig means "use the default" — this matches Go's
 * resolveThreshold(0, defVal) = defVal semantics. Use -1 to disable a threshold.
 *
 * All other metric keys fall back to DEFAULTS only.
 *
 * @param {keyof typeof DEFAULTS} metricKey
 * @param {import('./api.js').PerfMonitoringConfig|null|undefined} perfConfig
 * @returns {Thresholds}
 */
export function resolveThresholds(metricKey, perfConfig) {
    const defaults = DEFAULTS[metricKey] ?? { warn: Infinity, crit: Infinity };

    if (perfConfig == null) return { ...defaults };

    switch (metricKey) {
        case 'cpu':
            return {
                warn: resolveConfiguredThreshold(perfConfig.cpu_warn_pct, defaults.warn),
                crit: resolveConfiguredThreshold(perfConfig.cpu_crit_pct, defaults.crit),
            };
        case 'mem':
            // API layer already converts Go's % free to % used.
            return {
                warn: resolveConfiguredThreshold(perfConfig.mem_warn_pct, defaults.warn),
                crit: resolveConfiguredThreshold(perfConfig.mem_crit_pct, defaults.crit),
            };
        case 'inputDelay':
            return {
                warn: resolveConfiguredThreshold(perfConfig.input_delay_warn_ms, defaults.warn),
                crit: resolveConfiguredThreshold(perfConfig.input_delay_crit_ms, defaults.crit),
            };
        default:
            return { ...defaults };
    }
}
