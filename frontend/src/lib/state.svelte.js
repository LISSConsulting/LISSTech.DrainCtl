/**
 * state.svelte.js — Global reactive state using Svelte 5 runes.
 *
 * Import `appState` anywhere in the component tree without prop drilling.
 * Mutation helpers `addEvent`, `appendMetricsSample`, and
 * `appendServerMetricsSample` keep array caps enforced.
 */

const MAX_EVENTS = 200;
const MAX_METRICS = 60;

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
let servers = $state([]);

/** @type {HealthResponse|null} */
let health = $state(null);

/** @type {NotifyConfig|null} */
let config = $state(null);

/** @type {string[]} */
let events = $state([]);

/** @type {MetricsSample[]} */
let metricsHistory = $state([]);

/**
 * Per-server metric ring buffers (capped at MAX_METRICS each).
 * Key = hostname, value = MetricsSample[].
 * @type {Map<string, MetricsSample[]>}
 */
let serverMetrics = $state(new Map());

// UI state
let connected = $state(false);

/** @type {Date|null} */
let lastUpdated = $state(null);

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
  const perf = servers.map(s => s.perf).filter(p => p != null && p.sample_count > 0);
  if (perf.length === 0) return 0;
  return perf.reduce((sum, p) => sum + p.cpu_pct, 0) / perf.length;
});

const avgMem = $derived.by(() => {
  const perf = servers.map(s => s.perf).filter(p => p != null && p.sample_count > 0 && p.mem_total_mb > 0);
  if (perf.length === 0) return 0;
  const usedPcts = perf.map(p => ((p.mem_total_mb - p.mem_free_mb) / p.mem_total_mb) * 100);
  return usedPcts.reduce((sum, v) => sum + v, 0) / usedPcts.length;
});

const avgInputDelay = $derived.by(() => {
  const perf = servers.map(s => s.perf).filter(p => p != null && p.sample_count > 0);
  if (perf.length === 0) return 0;
  return perf.reduce((sum, p) => sum + p.input_delay_ms, 0) / perf.length;
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
 * Prepend a log event string, capping the array at MAX_EVENTS.
 * @param {string} msg
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
