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

/**
 * Must match MOCK_VERSION in frontend/dev/mock-api.js.
 * Bump both when the mock fleet definition changes; mismatched localStorage
 * data is wiped automatically on the next page load.
 */
const MOCK_VERSION = '2.0';

// ---------------------------------------------------------------------------
// localStorage persistence helpers
// ---------------------------------------------------------------------------

const LS_SERVERS        = 'drainctl:servers';
const LS_HEALTH         = 'drainctl:health';
const LS_METRICS        = 'drainctl:metrics';
const LS_SERVER_METRICS = 'drainctl:server-metrics';
const LS_EVENTS         = 'drainctl:events';
const LS_LAST_UPDATED   = 'drainctl:last-updated';
const LS_MOCK_VERSION   = 'drainctl:mock-version';

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

/** Return a debounced function that delays invoking `fn` until `ms` ms after the last call. */
function debounce(fn, ms) {
  let timer;
  return (value) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(value), ms);
  };
}

const persistServers = debounce(
  (data) => { try { localStorage.setItem(LS_SERVERS, JSON.stringify(data)); } catch {} },
  300,
);
const persistHealth = debounce(
  (data) => { try { localStorage.setItem(LS_HEALTH, JSON.stringify(data)); } catch {} },
  300,
);
const persistMetrics = debounce(
  (data) => { try { localStorage.setItem(LS_METRICS, JSON.stringify(data)); } catch {} },
  300,
);
const persistServerMetrics = debounce(
  (map)  => { try { localStorage.setItem(LS_SERVER_METRICS, JSON.stringify([...map.entries()])); } catch {} },
  300,
);
const persistEvents = debounce(
  (data) => { try { localStorage.setItem(LS_EVENTS, JSON.stringify(data)); } catch {} },
  300,
);
const persistLastUpdated = debounce(
  (date) => { try { localStorage.setItem(LS_LAST_UPDATED, JSON.stringify(date ? date.getTime() : null)); } catch {} },
  300,
);

/**
 * @typedef {import('./api.js').Server} Server
 * @typedef {import('./api.js').HealthResponse} HealthResponse
 * @typedef {import('./api.js').NotifyConfig} NotifyConfig
 */

/**
 * @typedef {Object} MetricsSample
 * @property {number} time        - Unix timestamp (ms)
 * @property {number} cpu         - Average CPU % across all servers with perf data
 * @property {number} mem         - Average memory used % across all servers with perf data
 * @property {number} inputDelay  - Average input delay (ms)
 * @property {number} sessions    - Total sessions across all servers
 */

/**
 * @typedef {Object} StateBarSegment
 * @property {'ok'|'grace'|'alert'|'off'} state
 * @property {number} pct   - 0–100
 * @property {number} count
 */

/**
 * @typedef {Object} Counters
 * @property {number} total
 * @property {number} ok
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

/** @type {NotifyConfig|null} */
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

// UI state
let connected = $state(false);

/** @type {Date|null} */
let lastUpdated = $state(lsGetDate(LS_LAST_UPDATED));

// ---------------------------------------------------------------------------
// localStorage persistence effects (module-level, outside any component)
// ---------------------------------------------------------------------------

$effect.root(() => {
  $effect(() => { persistServers(servers); });
  $effect(() => { persistHealth(health); });
  $effect(() => { persistMetrics(metricsHistory); });
  $effect(() => { persistServerMetrics(serverMetrics); });
  $effect(() => { persistEvents(events); });
  $effect(() => { persistLastUpdated(lastUpdated); });
});

// ---------------------------------------------------------------------------
// Derived state
// ---------------------------------------------------------------------------

/** @type {Counters} */
const counters = $derived.by(() => {
  const total = servers.length;
  let ok = 0, grace = 0, alert = 0, off = 0, sessions = 0;
  for (const s of servers) {
    sessions += s.sessions ?? 0;
    switch (s.status) {
      case 'ok':    ok++;    break;
      case 'grace': grace++; break;
      case 'alert': alert++; break;
      case 'off':   off++;   break;
    }
  }
  return { total, ok, grace, alert, off, sessions };
});

/** @type {StateBarSegment[]} */
const stateBarSegments = $derived.by(() => {
  const total = counters.total;
  if (total === 0) {
    return [
      { state: 'ok',    pct: 0, count: 0 },
      { state: 'grace', pct: 0, count: 0 },
      { state: 'alert', pct: 0, count: 0 },
      { state: 'off',   pct: 0, count: 0 },
    ];
  }
  return /** @type {StateBarSegment[]} */ ([
    { state: 'ok',    pct: (counters.ok    / total) * 100, count: counters.ok    },
    { state: 'grace', pct: (counters.grace / total) * 100, count: counters.grace },
    { state: 'alert', pct: (counters.alert / total) * 100, count: counters.alert },
    { state: 'off',   pct: (counters.off   / total) * 100, count: counters.off   },
  ]);
});

const avgCpu = $derived.by(() => {
  const perf = servers.map(s => s.perf).filter(p => p != null);
  if (perf.length === 0) return 0;
  return perf.reduce((sum, p) => sum + (p.cpu_pct || 0), 0) / perf.length;
});

const avgMem = $derived.by(() => {
  const perf = servers.map(s => s.perf).filter(p => p != null && p.mem_total_mb > 0);
  if (perf.length === 0) return 0;
  const usedPcts = perf.map(p => ((p.mem_total_mb - p.mem_avail_mb) / p.mem_total_mb) * 100);
  return usedPcts.reduce((sum, v) => sum + v, 0) / usedPcts.length;
});

const avgInputDelay = $derived.by(() => {
  const perf = servers.map(s => s.perf).filter(p => p != null);
  if (perf.length === 0) return 0;
  return perf.reduce((sum, p) => sum + (p.input_delay_p95_ms || 0), 0) / perf.length;
});

const totalSessions = $derived.by(() => counters.sessions);

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
  get servers()        { return servers; },
  set servers(v)       { servers = v; },

  get health()         { return health; },
  set health(v)        { health = v; },

  get config()         { return config; },
  set config(v)        { config = v; },

  get events()         { return events; },
  set events(v)        { events = v; },

  get metricsHistory() { return metricsHistory; },
  set metricsHistory(v){ metricsHistory = v; },

  // Per-server ring buffers — read-only; mutate via appendServerMetricsSample
  get serverMetrics()  { return serverMetrics; },

  // UI state
  get connected()      { return connected; },
  set connected(v)     { connected = v; },

  get lastUpdated()    { return lastUpdated; },
  set lastUpdated(v)   { lastUpdated = v; },

  // Derived — read-only
  get counters()          { return counters; },
  get stateBarSegments()  { return stateBarSegments; },
  get avgCpu()            { return avgCpu; },
  get avgMem()            { return avgMem; },
  get avgInputDelay()     { return avgInputDelay; },
  get totalSessions()     { return totalSessions; },
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
