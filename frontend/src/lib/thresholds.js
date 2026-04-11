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
  cpu:            { warn: 70,  crit: 85  },
  mem:            { warn: 80,  crit: 90  },
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
 *   cpu         → cpu_warn_pct / cpu_crit_pct           (% used, 0 = use default)
 *   mem         → mem_warn_pct / mem_crit_pct           (% FREE in Go config, converted
 *                                                         to % used here; 0 = use default)
 *   inputDelay  → input_delay_warn_ms / input_delay_crit_ms (ms, 0 = use default)
 *
 * Go stores memory thresholds as "% free" (e.g. mem_warn_pct=20 means warn when
 * <20% free). The ring gauge and color helpers use "% used" (0–100), so this
 * function inverts them: threshold_pct_used = 100 - threshold_pct_free.
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
        warn: perfConfig.cpu_warn_pct > 0 ? perfConfig.cpu_warn_pct : defaults.warn,
        crit: perfConfig.cpu_crit_pct > 0 ? perfConfig.cpu_crit_pct : defaults.crit,
      };
    case 'mem': {
      // Go config stores % free; ring gauge uses % used — invert.
      const warnUsed = perfConfig.mem_warn_pct > 0 ? 100 - perfConfig.mem_warn_pct : defaults.warn;
      const critUsed = perfConfig.mem_crit_pct > 0 ? 100 - perfConfig.mem_crit_pct : defaults.crit;
      return { warn: warnUsed, crit: critUsed };
    }
    case 'inputDelay':
      return {
        warn: perfConfig.input_delay_warn_ms > 0 ? perfConfig.input_delay_warn_ms : defaults.warn,
        crit: perfConfig.input_delay_crit_ms > 0 ? perfConfig.input_delay_crit_ms : defaults.crit,
      };
    default:
      return { ...defaults };
  }
}
