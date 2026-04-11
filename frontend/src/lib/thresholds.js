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
 *   mem           → percentage (0–100, derived from mem_free_mb / mem_total_mb)
 *   sessions      → percentage of session_warning_threshold (0–100)
 *   diskQueue     → average disk queue depth (unitless)
 *   inputDelay    → milliseconds
 *   tcpRetransmits → percentage
 *
 * @type {Record<string, {warn: number, crit: number}>}
 */
export const DEFAULTS = {
  cpu:            { warn: 85,  crit: 95  },
  mem:            { warn: 85,  crit: 95  },
  sessions:       { warn: 80,  crit: 95  },
  diskQueue:      { warn: 2,   crit: 5   },
  inputDelay:     { warn: 50,  crit: 100 },
  tcpRetransmits: { warn: 5,   crit: 10  },
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
    // Low values are bad (e.g. available capacity score)
    if (value <= crit) return 'red';
    if (value <= warn) return 'amber';
    return 'green';
  }

  // Higher-worse (default): high values are bad
  if (value >= crit) return 'red';
  if (value >= warn) return 'amber';
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

/**
 * Resolve thresholds for a metric, preferring values from the server's
 * perf_monitoring config over the built-in DEFAULTS.
 *
 * Recognised config overrides:
 *   cpu         → cpu_warn_pct / cpu_crit_pct
 *   mem         → mem_warn_pct / mem_crit_pct
 *   inputDelay  → input_delay_warn_ms / input_delay_crit_ms
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
        warn: perfConfig.cpu_warn_pct ?? defaults.warn,
        crit: perfConfig.cpu_crit_pct ?? defaults.crit,
      };
    case 'mem':
      return {
        warn: perfConfig.mem_warn_pct ?? defaults.warn,
        crit: perfConfig.mem_crit_pct ?? defaults.crit,
      };
    case 'inputDelay':
      return {
        warn: perfConfig.input_delay_warn_ms ?? defaults.warn,
        crit: perfConfig.input_delay_crit_ms ?? defaults.crit,
      };
    default:
      return { ...defaults };
  }
}
