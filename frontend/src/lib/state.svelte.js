/**
 * state.svelte.js — Global reactive state using Svelte 5 runes.
 *
 * Import `appState` anywhere in the component tree without prop drilling.
 * Mutation helpers `addEvent`, `appendMetricsSample`, and
 * `appendServerMetricsSample` keep array caps enforced.
 *
 * servers, health, metricsHistory, serverMetrics, events, and lastUpdated are
 * persisted to localStorage so they survive page reloads. Writes are debounced
 * at 300 ms to avoid thrashing. connected and config are intentionally transient.
 */

const MAX_EVENTS = 200;
const MAX_METRICS = 60;
// Matches the server-side per-host ring buffer in internal/dashboard/spikestore.go.
const MAX_RECENT_SPIKES = 20;

/**
 * Must match MOCK_VERSION in frontend/dev/mock-api.js.
 * Bump both when the mock fleet definition changes; mismatched localStorage
 * data is wiped automatically on the next page load.
 */
const MOCK_VERSION = '3.6';

// ---------------------------------------------------------------------------
// localStorage persistence helpers
// ---------------------------------------------------------------------------

const LS_SERVERS = 'drainctl:servers';
const LS_HEALTH = 'drainctl:health';
const LS_METRICS = 'drainctl:metrics';
const LS_SERVER_METRICS = 'drainctl:server-metrics';
const LS_EVENTS = 'drainctl:events';
const LS_LAST_UPDATED = 'drainctl:last-updated';
const LS_MOCK_VERSION = 'drainctl:mock-version';
const LS_SESSION_HISTORY = 'drainctl:session-history';
const LS_RFX_HISTORY = 'drainctl:rfx-history';
const LS_RFX_AVAILABLE = 'drainctl:rfx-available';

// Non-authoritative UI-preference keys: persisted as operator convenience only.
// These do NOT represent retained history; history authority belongs to the backend.
const LS_OVERVIEW_WINDOW = 'drainctl:overview-window';

/**
 * If the stored mock-data version doesn't match the current MOCK_VERSION,
 * wipe all drainctl:* keys so the dashboard starts fresh with new mock data.
 * Runs once at module init, before any $state declarations read localStorage.
 */
function clearStaleState() {
    try {
        if (localStorage.getItem(LS_MOCK_VERSION) === MOCK_VERSION) return;
        const keysToRemove = [];
        for (let i = 0; i < localStorage.length; i++) {
            const key = localStorage.key(i);
            if (key?.startsWith('drainctl:')) keysToRemove.push(key);
        }
        for (const key of keysToRemove) localStorage.removeItem(key);
        localStorage.setItem(LS_MOCK_VERSION, MOCK_VERSION);
    } catch {
        // localStorage unavailable — no-op
    }
}

clearStaleState();

/** Read and JSON-parse a localStorage key; return `fallback` on any error. */
function lsGet(key, fallback) {
    try {
        const raw = localStorage.getItem(key);
        return raw ? JSON.parse(raw) : fallback;
    } catch {
        return fallback;
    }
}

/** Load serverMetrics from localStorage as a Map (stored as array-of-entries). */
function lsGetServerMetrics() {
    try {
        const raw = localStorage.getItem(LS_SERVER_METRICS);
        if (!raw) return new Map();
        return new Map(JSON.parse(raw));
    } catch {
        return new Map();
    }
}

/** Load a Date stored as a Unix ms timestamp; return null on missing/error. */
function lsGetDate(key) {
    try {
        const raw = localStorage.getItem(key);
        if (!raw) return null;
        const ts = JSON.parse(raw);
        return ts != null ? new Date(ts) : null;
    } catch {
        return null;
    }
}

/**
 * Shared Overview time-window preset definitions (FR-008a).
 * Exported so pill controls and data-fetch callers share one source of truth.
 * @type {Array<{key: OverviewWindowPreset, label: string, ms: number}>}
 */
export const OVERVIEW_WINDOW_PRESETS = [
    { key: '5min', label: '5M', ms: 5 * 60 * 1000 },
    { key: '1hour', label: '1H', ms: 60 * 60 * 1000 },
    { key: '1day', label: '1D', ms: 24 * 60 * 60 * 1000 },
    { key: '3day', label: '3D', ms: 3 * 24 * 60 * 60 * 1000 },
    { key: '5day', label: '5D', ms: 5 * 24 * 60 * 60 * 1000 },
];

function lsGetOverviewWindow() {
    const raw = lsGet(LS_OVERVIEW_WINDOW, null);
    return OVERVIEW_WINDOW_PRESETS.some((p) => p.key === raw)
        ? /** @type {OverviewWindowPreset} */ (raw)
        : /** @type {OverviewWindowPreset} */ ('1hour');
}

/** Return a debounced function that delays invoking `fn` until `ms` ms after the last call. */
function debounce(fn, ms) {
    let timer;
    return (value) => {
        clearTimeout(timer);
        timer = setTimeout(() => fn(value), ms);
    };
}

const persistServers = debounce((data) => {
    try {
        localStorage.setItem(LS_SERVERS, JSON.stringify(data));
    } catch {}
}, 300);
const persistHealth = debounce((data) => {
    try {
        localStorage.setItem(LS_HEALTH, JSON.stringify(data));
    } catch {}
}, 300);
const persistMetrics = debounce((data) => {
    try {
        localStorage.setItem(LS_METRICS, JSON.stringify(data));
    } catch {}
}, 300);
const persistServerMetrics = debounce((map) => {
    try {
        localStorage.setItem(LS_SERVER_METRICS, JSON.stringify([...map.entries()]));
    } catch {}
}, 300);
const persistEvents = debounce((data) => {
    try {
        localStorage.setItem(LS_EVENTS, JSON.stringify(data));
    } catch {}
}, 300);
const persistLastUpdated = debounce((date) => {
    try {
        localStorage.setItem(LS_LAST_UPDATED, JSON.stringify(date ? date.getTime() : null));
    } catch {}
}, 300);
const persistSessionHistory = debounce((data) => {
    try {
        localStorage.setItem(LS_SESSION_HISTORY, JSON.stringify(data));
    } catch {}
}, 300);
const persistRfxHistory = debounce((data) => {
    try {
        localStorage.setItem(LS_RFX_HISTORY, JSON.stringify(data));
    } catch {}
}, 300);
const persistRfxAvailable = debounce((v) => {
    try {
        localStorage.setItem(LS_RFX_AVAILABLE, JSON.stringify(v));
    } catch {}
}, 300);
const persistOverviewWindow = debounce((v) => {
    try {
        localStorage.setItem(LS_OVERVIEW_WINDOW, JSON.stringify(v));
    } catch {}
}, 300);

/**
 * @typedef {import('./api.js').Server} Server
 * @typedef {import('./api.js').HealthResponse} HealthResponse
 * @typedef {import('./api.js').Settings} Settings
 * @typedef {import('./types.js').DetectorStatus} DetectorStatus
 * @typedef {import('./types.js').RecentSpike} RecentSpike
 */

/**
 * @typedef {Object} MetricsSample
 * @property {number} time         - Unix timestamp (ms)
 * @property {number} cpu          - Average CPU % across all servers with perf data
 * @property {number} [cpuP95]     - P95 CPU % across all servers with perf data
 * @property {number} mem          - Average memory used % across all servers with perf data
 * @property {number} inputDelay   - P95 input delay across fleet (ms)
 * @property {number} sessions     - Total sessions across all servers
 * @property {number} pagesPerSec  - P95 pages/sec across fleet (memory pressure indicator)
 * @property {number} tcpRetrans   - P95 TCP retransmits/sec across fleet
 * @property {number} diskQueue    - P95 disk queue length across fleet
 * @property {number} [p50InputDelay]  - P50 (median) input delay across fleet (ms)
 * @property {number} [p50PagesPerSec] - P50 pages/sec across fleet
 * @property {number} [p50TcpRetrans]  - P50 TCP retransmits/sec across fleet
 * @property {number} [p50DiskQueue]   - P50 disk queue length across fleet
 */

/**
 * @typedef {Object} SessionSample
 * @property {number} ts             - Unix timestamp (ms)
 * @property {number} active         - Total active (connected) sessions across fleet
 * @property {number} disconnected   - Total disconnected sessions across fleet
 * @property {number} total          - active + disconnected
 * @property {number} utilization    - (total/maxTotal)*100 average utilization %
 * @property {number} sessionCpuP95  - P95 per-session CPU % across fleet
 * @property {number} sessionMemP95  - P95 per-session working set bytes across fleet
 * @property {number} [sessionCpuP50] - P50 per-session CPU % across fleet
 * @property {number} [sessionMemP50] - P50 per-session working set bytes across fleet
 */

/**
 * @typedef {Object} RfxSample
 * @property {number} ts              - Unix timestamp (ms)
 * @property {number} fpsOut          - P95 output frames/sec across fleet
 * @property {number} encodeMs        - P95 average encoding time ms across fleet
 * @property {number} quality         - P95 frame quality % across fleet
 * @property {number} rtt             - P95 TCP round-trip time ms across fleet
 * @property {number} loss            - P95 loss rate % across fleet
 * @property {number} skipServer      - P95 frames skipped/sec (server resources) across fleet
 * @property {number} skipNet         - P95 frames skipped/sec (network resources) across fleet
 * @property {number} [fpsOutP50]     - P50 output frames/sec across fleet
 * @property {number} [encodeMsP50]   - P50 average encoding time ms across fleet
 * @property {number} [qualityP50]    - P50 frame quality % across fleet
 * @property {number} [rttP50]        - P50 TCP round-trip time ms across fleet
 * @property {number} [lossP50]       - P50 loss rate % across fleet
 * @property {number} [skipServerP50] - P50 frames skipped/sec (server resources) across fleet
 * @property {number} [skipNetP50]    - P50 frames skipped/sec (network resources) across fleet
 */

/**
 * @typedef {Object} StateBarSegment
 * @property {'ok'|'warning'|'grace'|'alert'|'off'} state
 * @property {number} pct   - 0–100
 * @property {number} count
 */

/** @typedef {'5min'|'1hour'|'1day'|'3day'|'5day'} OverviewWindowPreset */

/**
 * @typedef {Object} Counters
 * @property {number} total
 * @property {number} ok
 * @property {number} warning
 * @property {number} grace
 * @property {number} alert
 * @property {number} off
 * @property {number} sessions
 */

// ---------------------------------------------------------------------------
// Raw reactive state
// ---------------------------------------------------------------------------

/** @type {Server[]} */
let servers = $state(/** @type {Server[]} */ (lsGet(LS_SERVERS, [])));

/** @type {HealthResponse|null} */
let health = $state(/** @type {HealthResponse|null} */ (lsGet(LS_HEALTH, null)));

/** @type {Settings|null} */
let config = $state(null);

/** @type {(string|Record<string,unknown>)[]} */
let events = $state(/** @type {(string|Record<string,unknown>)[]} */ (lsGet(LS_EVENTS, [])));

/** @type {MetricsSample[]} */
let metricsHistory = $state(/** @type {MetricsSample[]} */ (lsGet(LS_METRICS, [])));

/**
 * Per-server metric ring buffers (capped at MAX_METRICS each).
 * Key = hostname, value = MetricsSample[].
 * @type {Map<string, MetricsSample[]>}
 */
let serverMetrics = $state(lsGetServerMetrics());

/**
 * Per-host evtspike detector status. Populated by `fetchEvtSpikeStatus` and
 * kept live by `detector_status` SSE events. Transient — not persisted to
 * localStorage; the backend is authoritative on reconnect.
 * @type {Map<string, DetectorStatus>}
 */
let detectorStatuses = $state(new Map());

/**
 * Per-host recent-spike ring buffer. Seeded by `fetchRecentSpikes` and
 * prepended by `recent_spike` SSE events. Capped at MAX_RECENT_SPIKES per
 * host to mirror the server-side ring buffer. Newest first.
 * @type {Map<string, RecentSpike[]>}
 */
let recentSpikes = $state(new Map());

// UI state
let connected = $state(false);
let sseConnected = $state(false);
/** True while EventSource is in CONNECTING state after a transient error. */
let sseReconnecting = $state(false);

/** @type {'overview'|'servers'|'events'} */
let currentView = $state('overview');

/** @type {'all'|'ok'|'warning'|'grace'|'alert'|'off'} */
let serverFilter = $state('all');

/** Event log host filter — set by History button to pre-filter events by host. */
let eventHostFilter = $state('');

/** @type {Date|null} */
let lastUpdated = $state(lsGetDate(LS_LAST_UPDATED));

/** @type {SessionSample[]} */
let sessionHistory = $state(/** @type {SessionSample[]} */ (lsGet(LS_SESSION_HISTORY, [])));

/** @type {RfxSample[]} */
let remoteFxHistory = $state(/** @type {RfxSample[]} */ (lsGet(LS_RFX_HISTORY, [])));

/** True when at least one server reports RemoteFX data. */
let rfxAvailable = $state(/** @type {boolean} */ (lsGet(LS_RFX_AVAILABLE, false)));

/** @type {'performance'|'sessions'|'remotefx'} */
let overviewSubTab = $state('performance');

/** @type {OverviewWindowPreset} */
let overviewWindow = $state(lsGetOverviewWindow());

/** Milliseconds for the active Overview window preset. Read-only derived; set `overviewWindow` to change. */
const overviewWindowMs = $derived(
    OVERVIEW_WINDOW_PRESETS.find((p) => p.key === overviewWindow)?.ms ?? 60 * 60 * 1000
);

/**
 * Shared hovered data index for synchronized crosshairs across all charts.
 * Set by whichever chart the user is currently hovering; cleared on mouseleave.
 * @type {number|null}
 */
let hoveredChartIndex = $state(/** @type {number|null} */ (null));

/**
 * Pinned data index — click a data point to freeze all charts at that index.
 * Click again (or click elsewhere) to unpin. When pinned, hoveredChartIndex
 * is ignored and this value drives crosshairs, tooltips, and current values.
 * @type {number|null}
 */
let pinnedChartIndex = $state(/** @type {number|null} */ (null));

// ---------------------------------------------------------------------------
// localStorage persistence effects (module-level, outside any component)
// ---------------------------------------------------------------------------

$effect.root(() => {
    $effect(() => {
        persistServers(servers);
    });
    $effect(() => {
        persistHealth(health);
    });
    $effect(() => {
        persistMetrics(metricsHistory);
    });
    $effect(() => {
        persistServerMetrics(serverMetrics);
    });
    $effect(() => {
        persistEvents(events);
    });
    $effect(() => {
        persistLastUpdated(lastUpdated);
    });
    $effect(() => {
        persistSessionHistory(sessionHistory);
    });
    $effect(() => {
        persistRfxHistory(remoteFxHistory);
    });
    $effect(() => {
        persistRfxAvailable(rfxAvailable);
    });
    $effect(() => {
        persistOverviewWindow(overviewWindow);
    });
});

// ---------------------------------------------------------------------------
// Derived state
// ---------------------------------------------------------------------------

/** @type {Counters} */
const counters = $derived.by(() => {
    const total = servers.length;
    let ok = 0,
        warning = 0,
        grace = 0,
        alert = 0,
        off = 0,
        sessions = 0;
    for (const s of servers) {
        sessions += s.sessions ?? 0;
        switch (s.status) {
            case 'ok':
                ok++;
                break;
            case 'warning':
                warning++;
                break;
            case 'grace':
                grace++;
                break;
            case 'alert':
                alert++;
                break;
            case 'off':
                off++;
                break;
        }
    }
    return { total, ok, warning, grace, alert, off, sessions };
});

/** @type {StateBarSegment[]} */
const stateBarSegments = $derived.by(() => {
    const total = counters.total;
    if (total === 0) {
        return [
            { state: 'ok', pct: 0, count: 0 },
            { state: 'warning', pct: 0, count: 0 },
            { state: 'grace', pct: 0, count: 0 },
            { state: 'alert', pct: 0, count: 0 },
            { state: 'off', pct: 0, count: 0 },
        ];
    }
    return /** @type {StateBarSegment[]} */ ([
        { state: 'ok', pct: (counters.ok / total) * 100, count: counters.ok },
        { state: 'warning', pct: (counters.warning / total) * 100, count: counters.warning },
        { state: 'grace', pct: (counters.grace / total) * 100, count: counters.grace },
        { state: 'alert', pct: (counters.alert / total) * 100, count: counters.alert },
        { state: 'off', pct: (counters.off / total) * 100, count: counters.off },
    ]);
});

/** Return the P95 value from a numeric array. @param {number[]} vals */
export function deriveP95(vals) {
    if (vals.length === 0) return 0;
    const sorted = [...vals].sort((a, b) => a - b);
    return sorted[Math.max(0, Math.ceil(vals.length * 0.95) - 1)];
}

/** Return the P50 (median) value from a numeric array. @param {number[]} vals */
export function deriveP50(vals) {
    if (vals.length === 0) return 0;
    const sorted = [...vals].sort((a, b) => a - b);
    const mid = Math.floor(sorted.length / 2);
    return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

// ---------------------------------------------------------------------------
// Exported state object
// ---------------------------------------------------------------------------

/**
 * Global application state.
 *
 * All properties are reactive via Svelte 5 runes. Derived fields are read-only;
 * write to raw fields (`servers`, `health`, `config`, `events`, `metricsHistory`,
 * `connected`, `lastUpdated`) directly, or use the mutation helpers.
 * `serverMetrics` is a Map and must be updated via `appendServerMetricsSample`.
 */
export const appState = {
    // Raw data — assign directly: appState.servers = newList
    get servers() {
        return servers;
    },
    set servers(v) {
        servers = v;
    },

    get health() {
        return health;
    },
    set health(v) {
        health = v;
    },

    get config() {
        return config;
    },
    set config(v) {
        config = v;
    },

    get events() {
        return events;
    },
    set events(v) {
        events = v;
    },

    get metricsHistory() {
        return metricsHistory;
    },
    set metricsHistory(v) {
        metricsHistory = v;
    },

    // Per-server ring buffers — read-only; mutate via appendServerMetricsSample
    get serverMetrics() {
        return serverMetrics;
    },

    // Evtspike per-host state — read-only; mutate via the helpers below
    get detectorStatuses() {
        return detectorStatuses;
    },
    get recentSpikes() {
        return recentSpikes;
    },

    // UI state
    get connected() {
        return connected;
    },
    set connected(v) {
        connected = v;
    },

    get sseConnected() {
        return sseConnected;
    },
    set sseConnected(v) {
        sseConnected = v;
    },

    get sseReconnecting() {
        return sseReconnecting;
    },
    set sseReconnecting(v) {
        sseReconnecting = v;
    },

    get currentView() {
        return currentView;
    },
    set currentView(v) {
        currentView = v;
    },

    get serverFilter() {
        return serverFilter;
    },
    set serverFilter(v) {
        serverFilter = v;
    },

    get eventHostFilter() {
        return eventHostFilter;
    },
    set eventHostFilter(v) {
        eventHostFilter = v;
    },

    get lastUpdated() {
        return lastUpdated;
    },
    set lastUpdated(v) {
        lastUpdated = v;
    },

    get hoveredChartIndex() {
        return hoveredChartIndex;
    },
    set hoveredChartIndex(v) {
        hoveredChartIndex = v;
    },

    get pinnedChartIndex() {
        return pinnedChartIndex;
    },
    set pinnedChartIndex(v) {
        pinnedChartIndex = v;
    },

    get sessionHistory() {
        return sessionHistory;
    },
    set sessionHistory(v) {
        sessionHistory = v;
    },

    get remoteFxHistory() {
        return remoteFxHistory;
    },
    set remoteFxHistory(v) {
        remoteFxHistory = v;
    },

    get rfxAvailable() {
        return rfxAvailable;
    },
    set rfxAvailable(v) {
        rfxAvailable = v;
    },

    get overviewSubTab() {
        return overviewSubTab;
    },
    set overviewSubTab(v) {
        overviewSubTab = v;
    },

    get overviewWindow() {
        return overviewWindow;
    },
    set overviewWindow(v) {
        overviewWindow = v;
    },

    get overviewWindowMs() {
        return overviewWindowMs;
    },

    // Derived — read-only
    get counters() {
        return counters;
    },
    get stateBarSegments() {
        return stateBarSegments;
    },

    /**
     * Handle an SSE server_update event by patching the matching server
     * in the servers array. If the host is new, append it.
     * @param {string} host
     * @param {Server} serverView
     */
    handleSSEServerUpdate(host, serverView) {
        const idx = servers.findIndex((s) => s.host === host);
        if (idx >= 0) {
            const updated = [...servers];
            updated[idx] = serverView;
            servers = updated;
        } else {
            servers = [...servers, serverView];
        }
        lastUpdated = new Date();
    },

    /**
     * Remove a server from the list when a server_deleted SSE event arrives.
     * @param {string} host
     */
    handleSSEServerDeleted(host) {
        servers = servers.filter((s) => s.host !== host);
        lastUpdated = new Date();
    },
};

// ---------------------------------------------------------------------------
// Mutation helpers
// ---------------------------------------------------------------------------

/**
 * Prepend a log event (string or structured object), capping the array at MAX_EVENTS.
 * Pass a string for simple text events; pass an object with { time, host, text, sev,
 * transition } fields to produce a colour-coded, host-tagged entry in EventLog.
 * @param {string|Record<string,unknown>} msg
 */
export function addEvent(msg) {
    events = [msg, ...events].slice(0, MAX_EVENTS);
}

/**
 * Append a metrics sample, capping the array at MAX_METRICS.
 * @param {MetricsSample} sample
 */
export function appendMetricsSample(sample) {
    metricsHistory = [...metricsHistory, sample].slice(-MAX_METRICS);
}

/**
 * Append a per-server metrics sample, capping each host's ring buffer at MAX_METRICS.
 * Creates a new Map to preserve Svelte 5 deep reactivity.
 * @param {string} host
 * @param {MetricsSample} sample
 */
export function appendServerMetricsSample(host, sample) {
    const next = new Map(serverMetrics);
    const prev = next.get(host) ?? [];
    next.set(host, [...prev, sample].slice(-MAX_METRICS));
    serverMetrics = next;
}

/**
 * Remove a server's metrics ring buffer (call after the server is deleted from the
 * dashboard so the stale entry does not leak memory indefinitely).
 * @param {string} host
 */
export function removeServerMetrics(host) {
    if (!serverMetrics.has(host)) return;
    const next = new Map(serverMetrics);
    next.delete(host);
    serverMetrics = next;
}

/**
 * Append a session aggregate sample, capping the array at MAX_METRICS.
 * @param {SessionSample} sample
 */
export function appendSessionSample(sample) {
    sessionHistory = [...sessionHistory, sample].slice(-MAX_METRICS);
}

/**
 * Append a RemoteFX aggregate sample, capping the array at MAX_METRICS.
 * @param {RfxSample} sample
 */
export function appendRfxSample(sample) {
    remoteFxHistory = [...remoteFxHistory, sample].slice(-MAX_METRICS);
}

/**
 * Set the evtspike detector status for a host. Creates a new Map to preserve
 * Svelte 5 deep reactivity. Called by SSE `detector_status` events and by the
 * REST fallback via `fetchEvtSpikeStatus`.
 * @param {string} host
 * @param {DetectorStatus} status
 */
export function setDetectorStatus(host, status) {
    const next = new Map(detectorStatuses);
    next.set(host, status);
    detectorStatuses = next;
}

/**
 * Replace the recent-spikes ring for a host with a seeded list (newest first).
 * Called after an explicit `fetchRecentSpikes` refresh. Trims to MAX_RECENT_SPIKES.
 * @param {string} host
 * @param {RecentSpike[]} entries
 */
export function setRecentSpikes(host, entries) {
    const next = new Map(recentSpikes);
    next.set(host, entries.slice(0, MAX_RECENT_SPIKES));
    recentSpikes = next;
}

/**
 * Prepend a single spike to a host's ring buffer, capped at MAX_RECENT_SPIKES.
 * De-dupes on `id` so a REST refresh immediately followed by an SSE arrival
 * of the same spike (common on reconnect) does not double-list.
 * @param {string} host
 * @param {RecentSpike} spike
 */
export function appendRecentSpike(host, spike) {
    const next = new Map(recentSpikes);
    const prev = next.get(host) ?? [];
    const deduped = prev.filter((s) => s.id !== spike.id);
    next.set(host, [spike, ...deduped].slice(0, MAX_RECENT_SPIKES));
    recentSpikes = next;
}

/**
 * Drop per-host evtspike state when a server is removed from the dashboard.
 * Mirrors `removeServerMetrics` so ghost entries don't leak indefinitely.
 * @param {string} host
 */
export function removeEvtSpikeState(host) {
    if (detectorStatuses.has(host)) {
        const nextStatuses = new Map(detectorStatuses);
        nextStatuses.delete(host);
        detectorStatuses = nextStatuses;
    }
    if (recentSpikes.has(host)) {
        const nextSpikes = new Map(recentSpikes);
        nextSpikes.delete(host);
        recentSpikes = nextSpikes;
    }
}

/**
 * Bulk-seed per-server metric ring buffers from a pre-fetched history map.
 * Only writes entries for hosts that have no existing data, so calling this
 * after the regular poll cycle has already begun is safe — it will not
 * overwrite live-accumulated samples.
 *
 * A single Map replacement is used instead of one appendServerMetricsSample
 * call per sample to avoid creating thousands of intermediate Maps (which
 * would thrash GC for a 50-server fleet × 60 samples).
 *
 * @param {Map<string, MetricsSample[]>} seedData
 */
export function seedServerMetrics(seedData) {
    const next = new Map(serverMetrics);
    let changed = false;
    for (const [host, samples] of seedData) {
        if (!next.has(host) || (next.get(host)?.length ?? 0) === 0) {
            next.set(host, samples.slice(-MAX_METRICS));
            changed = true;
        }
    }
    if (changed) serverMetrics = next;
}
