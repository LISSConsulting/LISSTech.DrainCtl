/**
 * Chart time-window presets — pure data, no Svelte imports.
 *
 * The per-host chart in lib/chart.svelte exposes 5 pill presets (15M, 1H, 1D,
 * 3D, 5D). The Overview fleet chart reuses `OVERVIEW_WINDOW_PRESETS` from
 * `state.svelte.js`, which has 4 presets (1H, 1D, 3D, 5D) — no 15-minute
 * window on Overview because per-host granular zoom isn't useful at the
 * fleet level and the SQL tier resolution switches to `raw` for spans
 * under ~30 min, multiplying fetch size.
 *
 * Both arrays live in their respective consumers; this module just exports
 * the per-host preset list and matching helpers so they can be unit-tested
 * without mounting the chart.
 */

/**
 * @typedef {{ label: string, ms: number }} Preset
 */

/**
 * Per-host chart presets (lib/chart.svelte). Operators rarely know to
 * scroll-wheel a chart — surface the common windows as clickable pills.
 * Sorted by ms ascending.
 *
 * @type {Preset[]}
 */
export const PER_HOST_PRESETS = [
    { label: '15M', ms: 15 * 60 * 1000 },
    { label: '1H', ms: 60 * 60 * 1000 },
    { label: '1D', ms: 24 * 60 * 60 * 1000 },
    { label: '3D', ms: 3 * 24 * 60 * 60 * 1000 },
    { label: '5D', ms: 5 * 24 * 60 * 60 * 1000 },
];

/**
 * Find the preset whose ms matches `spanMs` exactly. Returns null if none.
 * Tolerance (1% drift from prior wheel zoom) is applied by callers —
 * keep this strict so the storage round-trip is lossless.
 *
 * @param {Preset[]} presets
 * @param {number} spanMs
 * @returns {Preset|null}
 */
export function findPreset(presets, spanMs) {
    return presets.find((p) => p.ms === spanMs) ?? null;
}

/**
 * The localStorage key under which the per-host chart persists the
 * operator's last-clicked zoom preset. Extracted so test code can pin
 * the key without depending on the chart component's internal const.
 */
export const PER_HOST_ZOOM_PRESET_STORAGE_KEY = 'drainctl.chart.zoomPillMs';

/**
 * Read the persisted per-host zoom preset in milliseconds, with defensive
 * guards against malformed localStorage values (NaN, negative, zero).
 *
 * @returns {number|null}
 */
export function readStoredZoomPresetMs() {
    if (typeof localStorage === 'undefined') return null;
    try {
        const raw = localStorage.getItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY);
        if (!raw) return null;
        const n = Number(raw);
        return Number.isFinite(n) && n > 0 ? n : null;
    } catch {
        return null;
    }
}

/**
 * Persist the per-host zoom preset in milliseconds. Failures (disabled
 * localStorage, quota exceeded, private mode) are swallowed — losing the
 * preference is non-fatal.
 *
 * @param {number} ms
 */
export function writeStoredZoomPresetMs(ms) {
    if (typeof localStorage === 'undefined') return;
    try {
        localStorage.setItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY, String(ms));
    } catch {
        /* localStorage disabled / full — preference just won't persist */
    }
}
