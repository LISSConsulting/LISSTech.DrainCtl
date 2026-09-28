export const NEUTRAL_METRIC_COLOR = 'var(--color-blue)';
export const SECONDARY_METRIC_COLOR = 'var(--color-series-secondary)';
export const PRIMARY_METRIC_FILL_OPACITY = 0.46;
export const SECONDARY_METRIC_FILL_OPACITY = 0.34;
export const SINGLE_METRIC_FILL_OPACITY = 0.58;

export const LOAD_SERIES_META = Object.freeze([
    Object.freeze({
        key: 'cpu',
        label: 'CPU AVG',
        shortLabel: 'CPU AVG',
        color: 'var(--color-accent)',
        axis: 'left',
        lineOnly: false,
        fillOpacity: 0.68,
        foregroundRank: 4,
    }),
    Object.freeze({
        key: 'mem',
        label: 'Memory',
        shortLabel: 'Memory',
        color: 'var(--color-green)',
        axis: 'left',
        lineOnly: false,
        fillOpacity: 0.58,
        foregroundRank: 3,
    }),
    Object.freeze({
        key: 'sessions',
        label: 'Sessions',
        shortLabel: 'Sessions',
        color: 'var(--color-blue)',
        axis: 'right',
        lineOnly: false,
        fillOpacity: 0.5,
        foregroundRank: 2,
        dash: '7,4',
    }),
    Object.freeze({
        key: 'cpuP95',
        label: 'CPU P95',
        shortLabel: 'CPU P95',
        color: 'var(--color-amber)',
        axis: 'left',
        lineOnly: false,
        fillOpacity: 0.42,
        foregroundRank: 1,
    }),
]);

export const DEFAULT_LOAD_VISIBILITY = Object.freeze({ cpu: true, cpuP95: true, mem: true, sessions: true });

export function readLoadVisibility(storage, key) {
    try {
        const parsed = JSON.parse(storage.getItem(key) ?? 'null');
        const visibility = { ...DEFAULT_LOAD_VISIBILITY };
        if (!parsed || typeof parsed !== 'object') return visibility;
        for (const series of LOAD_SERIES_META) {
            if (typeof parsed[series.key] === 'boolean') visibility[series.key] = parsed[series.key];
        }
        return visibility;
    } catch {
        return { ...DEFAULT_LOAD_VISIBILITY };
    }
}

export function writeLoadVisibility(storage, key, visibility) {
    try {
        const persisted = {};
        for (const series of LOAD_SERIES_META) persisted[series.key] = visibility[series.key] !== false;
        storage.setItem(key, JSON.stringify(persisted));
    } catch {
        // Browser storage is an optional UI convenience.
    }
}

export function hostLoadVisibilityKey(host) {
    return `drainctl:chart-visibility:host-load:${String(host).trim().toLowerCase()}`;
}

export const OVERVIEW_LOAD_VISIBILITY_KEY = 'drainctl:chart-visibility:overview-load';
