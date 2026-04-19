/**
 * mock-api.js — Vite plugin that serves realistic mock data for the
 * DrainCtl dashboard API during local development.
 *
 * Usage: add mockApi() to the plugins array in vite.config.js.
 *
 * Data is dynamic — server statuses shift over time, perf metrics jitter,
 * sessions fluctuate — so the dashboard feels alive while iterating on UI.
 *
 * MOCK_VERSION must be bumped whenever the fleet definition changes so that
 * state.svelte.js can detect and discard stale localStorage data.
 */

// Bump this string whenever the mock fleet definition changes.
// state.svelte.js reads the matching constant and auto-clears stale localStorage.
export const MOCK_VERSION = '3.6';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Clamp n to [lo, hi]. */
const clamp = (n, lo, hi) => Math.min(hi, Math.max(lo, n));

/** Random float in [lo, hi). */
const rand = (lo, hi) => lo + Math.random() * (hi - lo);

/** Random int in [lo, hi]. */
const randInt = (lo, hi) => Math.floor(rand(lo, hi + 1));

/** Pick a random element. */
const pick = (arr) => arr[Math.floor(Math.random() * arr.length)];

/** ISO-8601 timestamp string. */
const isoNow = () => new Date().toISOString();

/** ISO-8601 timestamp N minutes ago. */
const isoAgo = (minutes) => new Date(Date.now() - minutes * 60_000).toISOString();

/** ISO-8601 timestamp N minutes from now. */
const isoFuture = (minutes) => new Date(Date.now() + minutes * 60_000).toISOString();

// ---------------------------------------------------------------------------
// Server definitions — realistic RDS farm (50-node fleet)
// ---------------------------------------------------------------------------

// initStatus / initSessions produce a realistic starting snapshot:
//   25 ok · 12 grace · 8 alert · 5 off  ≈ 2955 total sessions
//
// Session profile:
//   alert  → heavy  100–120  sessions  (8 servers)
//   grace  → mid    45–82    sessions  (12 servers)
//   ok     → heavy  70–95    (8), mid 35–65 (12), light 11–28 (5)
//   off    → 0                          (5 servers)
const SERVERS = [
  // ── 8 alert — at/near capacity (RDSH01–RDSH08) ──────────────────────────
  { host: 'RDSH01.contoso.com', role: 'primary',   maxSessions: 120, initStatus: 'alert', initSessions: 117 },
  { host: 'RDSH02.contoso.com', role: 'primary',   maxSessions: 120, initStatus: 'alert', initSessions: 113 },
  { host: 'RDSH03.contoso.com', role: 'primary',   maxSessions: 120, initStatus: 'alert', initSessions: 110 },
  { host: 'RDSH04.contoso.com', role: 'primary',   maxSessions: 120, initStatus: 'alert', initSessions: 107 },
  { host: 'RDSH05.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'alert', initSessions: 103 },
  { host: 'RDSH06.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'alert', initSessions: 100 },
  { host: 'RDSH07.contoso.com', role: 'secondary', maxSessions: 100, initStatus: 'alert', initSessions:  97 },
  { host: 'RDSH08.contoso.com', role: 'secondary', maxSessions: 100, initStatus: 'alert', initSessions:  93 },

  // ── 12 grace — draining, mid-heavy load (RDSH09–RDSH20) ─────────────────
  { host: 'RDSH09.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'grace', initSessions:  82 },
  { host: 'RDSH10.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'grace', initSessions:  76 },
  { host: 'RDSH11.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'grace', initSessions:  71 },
  { host: 'RDSH12.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  67 },
  { host: 'RDSH13.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  64 },
  { host: 'RDSH14.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  69 },
  { host: 'RDSH15.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  61 },
  { host: 'RDSH16.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  74 },
  { host: 'RDSH17.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'grace', initSessions:  66 },
  { host: 'RDSH18.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'grace', initSessions:  52 },
  { host: 'RDSH19.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'grace', initSessions:  48 },
  { host: 'RDSH20.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'grace', initSessions:  46 },

  // ── 25 ok — healthy fleet, varied load (RDSH21–RDSH45) ──────────────────
  // 8 heavy-ok (primary / secondary, 70–95 sessions)
  { host: 'RDSH21.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'ok', initSessions:  94 },
  { host: 'RDSH22.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'ok', initSessions:  90 },
  { host: 'RDSH23.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'ok', initSessions:  87 },
  { host: 'RDSH24.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'ok', initSessions:  83 },
  { host: 'RDSH25.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'ok', initSessions:  79 },
  { host: 'RDSH26.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  75 },
  { host: 'RDSH27.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  73 },
  { host: 'RDSH28.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  70 },
  // 12 mid-ok (secondary / standby, 35–65 sessions)
  { host: 'RDSH29.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  65 },
  { host: 'RDSH30.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  62 },
  { host: 'RDSH31.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  59 },
  { host: 'RDSH32.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  55 },
  { host: 'RDSH33.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'ok', initSessions:  53 },
  { host: 'RDSH34.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  50 },
  { host: 'RDSH35.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  47 },
  { host: 'RDSH36.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  45 },
  { host: 'RDSH37.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  43 },
  { host: 'RDSH38.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  41 },
  { host: 'RDSH39.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  38 },
  { host: 'RDSH40.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  35 },
  // 5 light-ok (standby, 11–28 sessions)
  { host: 'RDSH41.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  28 },
  { host: 'RDSH42.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  23 },
  { host: 'RDSH43.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  19 },
  { host: 'RDSH44.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  14 },
  { host: 'RDSH45.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'ok', initSessions:  11 },

  // ── 5 offline (RDSH46–RDSH50) ────────────────────────────────────────────
  { host: 'RDSH46.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'off', initSessions:   0 },
  { host: 'RDSH47.contoso.com', role: 'standby',   maxSessions:  50, initStatus: 'off', initSessions:   0 },
  { host: 'RDSH48.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'off', initSessions:   0 },
  { host: 'RDSH49.contoso.com', role: 'secondary', maxSessions:  75, initStatus: 'off', initSessions:   0 },
  { host: 'RDSH50.contoso.com', role: 'primary',   maxSessions: 100, initStatus: 'off', initSessions:   0 },
];

const ADMINS = ['admin@contoso', 'svc-rds@contoso', 'jsmith@contoso', ''];

// ---------------------------------------------------------------------------
// Stateful mock data — evolves over time
// ---------------------------------------------------------------------------

/** @type {Map<string, { status: string, sessions: number, sessionsDisconnected: number, stateChangedAt: string, changedBy: string, graceDeadline: string|null, registeredAt: string, perf: object|null }>} */
const state = new Map();

/** Per-host state-transition history ring buffers. @type {Map<string, object[]>} */
const history = new Map();

/**
 * Per-host perf metric ring buffers — same shape as the MetricsSample records
 * that App.svelte appends to appState.serverMetrics on each poll.
 * Exposed via GET /api/v1/metrics so the client can seed its own ring buffers
 * immediately on first load instead of waiting 30 min to fill them.
 * @type {Map<string, object[]>}
 */
const perfHistory = new Map();
const MAX_PERF_HISTORY = 60;

/** Notification config (mutable via PUT). */
let mockSettings = {
  grace_period: 45,
  session_warning_threshold: 80,
  performance: {
    enabled: true,
    force_disabled: false,
    sample_interval_sec: 30,
    cpu_warn_pct: 70,
    cpu_crit_pct: 90,
    mem_warn_pct: 20,
    mem_crit_pct: 10,
    input_delay_warn_ms: 30,
    input_delay_crit_ms: 80,
    collect_per_session: false,
    collect_remotefx: false,
  },
  notifications: [
    { type: 'webhook', url: 'https://hooks.example.com/drainctl', secret: 'hmac-secret-1', triggers: ['alert', 'grace_entered', 'healthy'], repeat_minutes: 15, enabled: true },
    { type: 'ntfy', url: 'https://ntfy.example.com/drainctl-alerts', secret: '', triggers: ['alert'], repeat_minutes: 0, enabled: true },
    { type: 'webhook', url: 'https://teams.example.com/webhook/rds', secret: '', triggers: ['drain_on', 'drain_off', 'alert'], repeat_minutes: 60, enabled: true },
    { type: 'email', url: 'smtp://mail.contoso.com:587', from: 'drainctl@contoso.com', to: ['ops@contoso.com', 'rds-team@contoso.com'], triggers: ['alert', 'healthy'], repeat_minutes: 240, enabled: true },
    { type: 'ntfy', url: 'https://ntfy.internal/rds-ops', secret: '', triggers: ['cpu_critical', 'memory_critical'], repeat_minutes: 15, enabled: true },
    { type: 'webhook', url: 'https://pagerduty.example.com/v2/enqueue', secret: 'pd-routing-key', triggers: ['alert'], repeat_minutes: 0, enabled: true },
    { type: 'webhook', url: 'https://slack.example.com/hooks/T0001/B0001/xxxx', secret: '', triggers: ['drain_on', 'drain_off', 'grace_entered', 'alert', 'healthy'], repeat_minutes: 0, enabled: true },
    { type: 'ntfy', url: 'https://ntfy.example.com/rds-sessions', secret: '', triggers: ['session_warning'], repeat_minutes: 60, enabled: false },
    { type: 'email', url: 'smtps://smtp.office365.com:587', from: 'alerts@contoso.com', to: ['manager@contoso.com'], triggers: ['alert', 'cpu_critical', 'memory_critical', 'input_delay_critical'], repeat_minutes: 480, enabled: true },
    { type: 'webhook', url: 'https://grafana.internal/api/annotations', secret: '', triggers: ['drain_on', 'drain_off', 'alert', 'healthy'], repeat_minutes: 0, enabled: true },
  ],
  evtspike: {
    // Only `enabled` is exposed via /api/v1/settings per FR-028. Admin-only
    // evtspike fields live in config.json and are not surfaced in the mock.
    enabled: false,
  },
};

/**
 * Seed a perf-metrics ring buffer with MAX_PERF_HISTORY plausible past samples.
 * Each sample is generated independently with genPerf so the sparkline shows
 * natural variation rather than a flat line.
 * @param {string} host
 * @param {string} status  - 'ok' | 'grace' | 'alert'
 * @param {number} initSessions
 */
function seedPerfHistory(host, status, initSessions) {
  const now = Date.now();
  const def = SERVERS.find(d => d.host === host);
  const maxSessions = def?.maxSessions ?? 100;
  const samples = [];
  for (let i = MAX_PERF_HISTORY - 1; i >= 0; i--) {
    const p = genPerf(status);
    const svMemPct = p.mem_total_mb > 0
      ? (1 - p.mem_avail_mb / p.mem_total_mb) * 100 : 0;
    // Slightly vary sessions around the initial count so history looks live.
    const jitterSessions = Math.max(0, Math.round(initSessions + rand(-5, 5)));
    const sessDisc = randInt(0, 5);
    samples.push({
      time:               now - i * 30_000,
      cpu:                p.cpu_pct,
      cpuP95:             p.cpu_p95_pct,
      mem:                Math.round(svMemPct * 10) / 10,
      inputDelay:         p.input_delay_p95_ms,
      sessions:           jitterSessions,
      diskQueue:          p.disk_queue,
      tcpRetrans:         p.tcp_retrans_sec,
      pagesPerSec:        p.pages_sec,
      sessionsActive:     jitterSessions,
      sessionsDisconnected: sessDisc,
      maxSessions,
      sessionCpuP95:      p.session_cpu_p95_pct,
      sessionCpuP50:      p.session_cpu_p50_pct,
      sessionMemP95:      p.session_mem_p95_bytes,
      sessionMemP50:      p.session_mem_p50_bytes,
      rfxFpsOut:          p.rfx_fps_out,
      rfxEncodeMs:        p.rfx_encode_ms,
      rfxQuality:         p.rfx_quality_pct,
      rfxRtt:             p.rfx_rtt_ms,
      rfxLoss:            p.rfx_loss_pct,
      rfxSkipServer:      p.rfx_skip_server_sec,
      rfxSkipNet:         p.rfx_skip_net_sec,
      rfxFpsOutP50:       p.rfx_fps_out_p50,
      rfxEncodeMsP50:     p.rfx_encode_ms_p50,
      rfxQualityP50:      p.rfx_quality_pct_p50,
      rfxRttP50:          p.rfx_rtt_ms_p50,
      rfxLossP50:         p.rfx_loss_pct_p50,
      rfxSkipServerP50:   p.rfx_skip_server_sec_p50,
      rfxSkipNetP50:      p.rfx_skip_net_sec_p50,
    });
  }
  return samples;
}

/** Initialise server state on first access. */
function ensureState() {
  if (state.size > 0) return;
  for (const def of SERVERS) {
    const status = def.initStatus;
    const sessions = def.initSessions;
    state.set(def.host, {
      status,
      sessions,
      sessionsDisconnected: status === 'off' ? 0 : randInt(0, 8),
      stateChangedAt: isoAgo(randInt(5, 90)),
      changedBy: status === 'ok' ? '' : pick(ADMINS),
      graceDeadline: status === 'grace' ? isoFuture(randInt(10, 45)) : null,
      registeredAt: isoAgo(randInt(60 * 24 * 7, 60 * 24 * 30)),
      perf: status === 'off' ? null : genPerf(status),
    });
    // Seed state-transition history
    history.set(def.host, seedHistory(def.host, status));
    // Seed perf-metrics history (offline servers get an empty buffer)
    perfHistory.set(
      def.host,
      status === 'off' ? [] : seedPerfHistory(def.host, status, sessions),
    );
  }
}

/** Generate realistic perf metrics for a given status. */
function genPerf(status) {
  const base = {
    ok:      { cpu: [15, 55], mem: [40, 65], delay: [3,   15],  disk: [0.1, 1.5],  pages: [5,  45],   retrans: [0,  8]  },
    warning: { cpu: [68, 82], mem: [74, 84], delay: [95, 145],  disk: [1.5, 3.5],  pages: [20,  80],  retrans: [3, 18]  },
    grace:   { cpu: [40, 70], mem: [55, 78], delay: [15,  40],  disk: [1.0, 4.0],  pages: [30, 120],  retrans: [5, 30]  },
    alert:   { cpu: [65, 95], mem: [75, 95], delay: [35,  90],  disk: [3.5, 8.5],  pages: [80, 350],  retrans: [20, 75] },
  }[status] ?? { cpu: [15, 55], mem: [40, 65], delay: [3, 15], disk: [0.1, 1.5], pages: [5, 45], retrans: [0, 8] };

  const sessionBase = {
    ok:      { cpuP95: [1,  12], memMbP95: [200, 500] },
    warning: { cpuP95: [4,  16], memMbP95: [260, 560] },
    grace:   { cpuP95: [5,  20], memMbP95: [300, 600] },
    alert:   { cpuP95: [12, 35], memMbP95: [450, 850] },
  }[status] ?? { cpuP95: [1, 12], memMbP95: [200, 500] };

  const rfxBase = {
    ok:      { fps: [20, 30], enc: [5,  15], qual: [85, 99], rtt: [10, 40], loss: [0.0, 0.8], skipSvr: [0.0, 1.0], skipNet: [0.0, 0.5] },
    warning: { fps: [16, 26], enc: [10, 22], qual: [78, 93], rtt: [18, 52], loss: [0.1, 1.5], skipSvr: [0.1, 2.0], skipNet: [0.0, 0.8] },
    grace:   { fps: [14, 24], enc: [12, 30], qual: [72, 88], rtt: [25, 65], loss: [0.3, 2.5], skipSvr: [0.2, 3.0], skipNet: [0.1, 1.5] },
    alert:   { fps: [7,  18], enc: [25, 60], qual: [52, 78], rtt: [40, 90], loss: [1.0, 7.0], skipSvr: [2.0, 8.0], skipNet: [0.5, 4.0] },
  }[status] ?? { fps: [20, 30], enc: [5, 15], qual: [85, 99], rtt: [10, 40], loss: [0, 0.8], skipSvr: [0, 1], skipNet: [0, 0.5] };

  const cpu = rand(...base.cpu);
  const memTotalMb = 16384;
  const memUsedPct = rand(...base.mem) / 100;
  const memAvailMb = Math.round(memTotalMb * (1 - memUsedPct));
  const p50 = rand(...base.delay);
  const p95 = p50 * rand(1.5, 3.0);
  const max = p95 * rand(1.2, 2.0);

  return {
    cpu_pct:             Math.round(cpu * 10) / 10,
    cpu_p95_pct:         Math.round(Math.min(100, cpu * rand(1.05, 1.30)) * 10) / 10,
    mem_avail_mb:        memAvailMb,
    mem_total_mb:        memTotalMb,
    pages_sec:           Math.round(rand(...base.pages) * 10) / 10,
    disk_queue:          Math.round(rand(...base.disk) * 100) / 100,
    tcp_retrans_sec:     Math.round(rand(...base.retrans) * 10) / 10,
    input_delay_p50_ms:  Math.round(p50 * 10) / 10,
    input_delay_p95_ms:  Math.round(p95 * 10) / 10,
    input_delay_max_ms:  Math.round(max * 10) / 10,
    session_cpu_p95_pct:     Math.round(rand(...sessionBase.cpuP95) * 10) / 10,
    session_cpu_p50_pct:     Math.round(rand(...sessionBase.cpuP95) * rand(0.40, 0.60) * 10) / 10,
    session_mem_p95_bytes:   Math.round(rand(...sessionBase.memMbP95) * 1024 * 1024),
    session_mem_p50_bytes:   Math.round(rand(...sessionBase.memMbP95) * rand(0.45, 0.65) * 1024 * 1024),
    rfx_available:           true,
    rfx_fps_out:             Math.round(rand(...rfxBase.fps) * 10) / 10,
    rfx_encode_ms:           Math.round(rand(...rfxBase.enc) * 10) / 10,
    rfx_quality_pct:         Math.round(rand(...rfxBase.qual) * 10) / 10,
    rfx_rtt_ms:              Math.round(rand(...rfxBase.rtt) * 10) / 10,
    rfx_loss_pct:            Math.round(rand(...rfxBase.loss) * 100) / 100,
    rfx_skip_server_sec:     Math.round(rand(...rfxBase.skipSvr) * 10) / 10,
    rfx_skip_net_sec:        Math.round(rand(...rfxBase.skipNet) * 10) / 10,
    // P50 fields — FPS/Quality inverted (higher=better) so P50 > P95 per session; others lower
    rfx_fps_out_p50:         Math.round(clamp(rand(...rfxBase.fps) * rand(1.05, 1.25), 0, 60) * 10) / 10,
    rfx_encode_ms_p50:       Math.round(rand(...rfxBase.enc) * rand(0.45, 0.65) * 10) / 10,
    rfx_quality_pct_p50:     Math.round(clamp(rand(...rfxBase.qual) * rand(1.02, 1.10), 0, 100) * 10) / 10,
    rfx_rtt_ms_p50:          Math.round(rand(...rfxBase.rtt) * rand(0.45, 0.65) * 10) / 10,
    rfx_loss_pct_p50:        Math.round(rand(...rfxBase.loss) * rand(0.40, 0.60) * 100) / 100,
    rfx_skip_server_sec_p50: Math.round(Math.max(0, rand(...rfxBase.skipSvr) * rand(0.40, 0.60)) * 10) / 10,
    rfx_skip_net_sec_p50:    Math.round(Math.max(0, rand(...rfxBase.skipNet) * rand(0.40, 0.60)) * 10) / 10,
  };
}

/** Jitter existing perf metrics slightly (simulates live variation). */
function jitterPerf(perf) {
  if (!perf) return null;
  const j = (v, pct = 0.08) => {
    const delta = v * pct;
    return Math.round(clamp(v + rand(-delta, delta), 0, 100000) * 10) / 10;
  };
  // For byte-scale values (session_mem_p95_bytes) the 100000 clamp in j() is wrong — use this instead.
  const jBig = (v, pct = 0.05) => Math.round(Math.max(0, v * (1 + rand(-pct, pct))));
  return {
    ...perf,
    cpu_pct:                 j(perf.cpu_pct),
    cpu_p95_pct:             Math.round(Math.min(100, j(perf.cpu_p95_pct ?? perf.cpu_pct * 1.15)) * 10) / 10,
    mem_avail_mb:            Math.round(j(perf.mem_avail_mb, 0.03)),
    pages_sec:               j(perf.pages_sec, 0.15),
    disk_queue:              Math.round(j(perf.disk_queue, 0.12) * 100) / 100,
    tcp_retrans_sec:         j(perf.tcp_retrans_sec, 0.2),
    input_delay_p50_ms:      j(perf.input_delay_p50_ms, 0.1),
    input_delay_p95_ms:      j(perf.input_delay_p95_ms, 0.1),
    input_delay_max_ms:      j(perf.input_delay_max_ms, 0.1),
    session_cpu_p95_pct:     Math.round(clamp(j(perf.session_cpu_p95_pct, 0.12), 0, 100) * 10) / 10,
    session_cpu_p50_pct:     Math.round(clamp(j(perf.session_cpu_p50_pct ?? perf.session_cpu_p95_pct * 0.5, 0.12), 0, 100) * 10) / 10,
    session_mem_p95_bytes:   jBig(perf.session_mem_p95_bytes, 0.05),
    session_mem_p50_bytes:   jBig(perf.session_mem_p50_bytes ?? perf.session_mem_p95_bytes * 0.55, 0.05),
    rfx_fps_out:             Math.round(clamp(j(perf.rfx_fps_out, 0.08), 0, 60) * 10) / 10,
    rfx_encode_ms:           j(perf.rfx_encode_ms, 0.12),
    rfx_quality_pct:         Math.round(clamp(j(perf.rfx_quality_pct, 0.05), 0, 100) * 10) / 10,
    rfx_rtt_ms:              j(perf.rfx_rtt_ms, 0.15),
    rfx_loss_pct:            Math.round(clamp(j(perf.rfx_loss_pct, 0.20), 0, 100) * 100) / 100,
    rfx_skip_server_sec:     Math.round(Math.max(0, j(perf.rfx_skip_server_sec, 0.25)) * 10) / 10,
    rfx_skip_net_sec:        Math.round(Math.max(0, j(perf.rfx_skip_net_sec, 0.25)) * 10) / 10,
    rfx_fps_out_p50:         Math.round(clamp(j(perf.rfx_fps_out_p50 ?? perf.rfx_fps_out * 1.1, 0.08), 0, 60) * 10) / 10,
    rfx_encode_ms_p50:       j(perf.rfx_encode_ms_p50 ?? perf.rfx_encode_ms * 0.55, 0.12),
    rfx_quality_pct_p50:     Math.round(clamp(j(perf.rfx_quality_pct_p50 ?? perf.rfx_quality_pct * 1.05, 0.05), 0, 100) * 10) / 10,
    rfx_rtt_ms_p50:          j(perf.rfx_rtt_ms_p50 ?? perf.rfx_rtt_ms * 0.55, 0.15),
    rfx_loss_pct_p50:        Math.round(clamp(j(perf.rfx_loss_pct_p50 ?? perf.rfx_loss_pct * 0.50, 0.20), 0, 100) * 100) / 100,
    rfx_skip_server_sec_p50: Math.round(Math.max(0, j(perf.rfx_skip_server_sec_p50 ?? perf.rfx_skip_server_sec * 0.50, 0.25)) * 10) / 10,
    rfx_skip_net_sec_p50:    Math.round(Math.max(0, j(perf.rfx_skip_net_sec_p50 ?? perf.rfx_skip_net_sec * 0.50, 0.25)) * 10) / 10,
  };
}

/**
 * Return a spiked version of perf — high input delay, TCP retrans, and disk queue.
 * Used to simulate a troubled server so P95 charts show meaningful spikes.
 * Values are intentionally dramatic so fleet P95 (95th pct across 50 servers) shows
 * visible spikes when 3–5 servers are simultaneously troubled.
 */
function spikePerf(perf) {
  if (!perf) return null;
  return {
    ...perf,
    cpu_p95_pct:        Math.round(rand(85, 99) * 10) / 10,
    input_delay_p95_ms: Math.round(rand(280, 480) * 10) / 10,
    input_delay_p50_ms: Math.round(rand(120, 260) * 10) / 10,
    input_delay_max_ms: Math.round(rand(550, 950) * 10) / 10,
    tcp_retrans_sec:    Math.round(rand(30,  70)  * 10) / 10,
    disk_queue:         Math.round(rand(5.5, 9.5) * 100) / 100,
    pages_sec:          Math.round(rand(160, 400) * 10) / 10,
    session_cpu_p95_pct:     Math.round(rand(30, 65) * 10) / 10,
    session_cpu_p50_pct:     Math.round(rand(12, 35) * 10) / 10,
    session_mem_p95_bytes:   Math.round(rand(700, 1300) * 1024 * 1024),
    session_mem_p50_bytes:   Math.round(rand(350, 750) * 1024 * 1024),
    rfx_fps_out:             Math.round(rand(2, 8)    * 10) / 10,
    rfx_encode_ms:           Math.round(rand(70, 140) * 10) / 10,
    rfx_quality_pct:         Math.round(rand(28, 58)  * 10) / 10,
    rfx_rtt_ms:              Math.round(rand(90, 220) * 10) / 10,
    rfx_loss_pct:            Math.round(rand(5, 15)   * 100) / 100,
    rfx_skip_server_sec:     Math.round(rand(8, 22)   * 10) / 10,
    rfx_skip_net_sec:        Math.round(rand(4, 14)   * 10) / 10,
    rfx_fps_out_p50:         Math.round(rand(4, 15)   * 10) / 10,
    rfx_encode_ms_p50:       Math.round(rand(40, 90)  * 10) / 10,
    rfx_quality_pct_p50:     Math.round(rand(38, 68)  * 10) / 10,
    rfx_rtt_ms_p50:          Math.round(rand(60, 140) * 10) / 10,
    rfx_loss_pct_p50:        Math.round(rand(3, 9)    * 100) / 100,
    rfx_skip_server_sec_p50: Math.round(rand(5, 14)   * 10) / 10,
    rfx_skip_net_sec_p50:    Math.round(rand(2, 8)    * 10) / 10,
  };
}

/** Seed a history ring buffer with plausible past entries. */
function seedHistory(host, currentStatus) {
  const entries = [];
  const statuses = ['ok', 'warning', 'grace', 'alert', 'ok', 'ok'];
  let prevStatus = 'ok';
  for (let i = 20; i >= 1; i--) {
    const s = i === 1 ? currentStatus : pick(statuses);
    const transition = s !== prevStatus;
    entries.push({
      timestamp: isoAgo(i * 30),
      host,
      status: s,
      drain_mode: (s === 'ok' || s === 'warning') ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
      state_duration_seconds: randInt(120, 7200),
      transition,
      transition_from: transition ? prevStatus : undefined,
      changed_by: transition && s !== 'ok' && s !== 'warning' ? pick(ADMINS) : undefined,
      version: '26.103.4',
      message: transition
        ? `Status changed: ${prevStatus} → ${s}`
        : `Check-in: ${s}`,
    });
    prevStatus = s;
  }
  return entries;
}

// ---------------------------------------------------------------------------
// SSE — broadcast real-time events to connected dev browsers
// ---------------------------------------------------------------------------

/** Active SSE response objects — each is a Node.js ServerResponse. */
const sseClients = new Set();

/**
 * Broadcast a DrainCtl SSE event to all connected clients.
 * Silently ignores clients whose connections have already closed.
 * @param {'server_update'|'server_deleted'|'settings_update'} type
 * @param {object|null} data
 * @param {string} [host]
 */
function broadcastSSE(type, data, host) {
  const payload = JSON.stringify({ type, host, data, timestamp: new Date().toISOString() });
  const frame = `data: ${payload}\n\n`;
  for (const res of sseClients) {
    try {
      res.write(frame);
    } catch {
      sseClients.delete(res);
    }
  }
}

// ---------------------------------------------------------------------------
// Periodic state evolution — statuses shift, metrics jitter
// ---------------------------------------------------------------------------

let evolveTimer = null;

function startEvolution() {
  if (evolveTimer) return;
  evolveTimer = setInterval(() => {
    ensureState();
    const now = Date.now();
    for (const [host, s] of state) {
      // Perf: spike on ~10% of ticks per server to make P95 charts interesting.
      // With 50 servers at 10% each, ~5 servers spike simultaneously → fleet P95
      // captures the spike (95th pct of 50 = position 47.5, within the top 5).
      // A spike lasts 2–4 ticks (20–40 s) so the P95 line jumps visibly.
      if (s.status !== 'off' && s.perf) {
        s.spikeUntil = s.spikeUntil ?? 0;
        if (now < s.spikeUntil) {
          s.perf = spikePerf(s.perf);
        } else if (Math.random() < 0.10) {
          s.spikeUntil = now + randInt(2, 4) * 10_000;
          s.perf = spikePerf(s.perf);
        } else {
          s.perf = jitterPerf(s.perf);
        }
      } else {
        s.perf = jitterPerf(s.perf);
      }

      // Append current perf snapshot to the per-host perf-history ring buffer.
      if (s.status !== 'off' && s.perf) {
        const svMemPct = s.perf.mem_total_mb > 0
          ? (1 - s.perf.mem_avail_mb / s.perf.mem_total_mb) * 100 : 0;
        const def = SERVERS.find(d => d.host === host);
        const hist = perfHistory.get(host) ?? [];
        hist.push({
          time:                now,
          cpu:                 s.perf.cpu_pct,
          cpuP95:              s.perf.cpu_p95_pct,
          mem:                 Math.round(svMemPct * 10) / 10,
          inputDelay:          s.perf.input_delay_p95_ms,
          sessions:            s.sessions,
          diskQueue:           s.perf.disk_queue,
          tcpRetrans:          s.perf.tcp_retrans_sec,
          pagesPerSec:         s.perf.pages_sec,
          sessionsActive:      s.sessions,
          sessionsDisconnected: s.sessionsDisconnected ?? 0,
          maxSessions:         def?.maxSessions ?? 100,
          sessionCpuP95:       s.perf.session_cpu_p95_pct,
          sessionCpuP50:       s.perf.session_cpu_p50_pct,
          sessionMemP95:       s.perf.session_mem_p95_bytes,
          sessionMemP50:       s.perf.session_mem_p50_bytes,
          rfxFpsOut:           s.perf.rfx_fps_out,
          rfxEncodeMs:         s.perf.rfx_encode_ms,
          rfxQuality:          s.perf.rfx_quality_pct,
          rfxRtt:              s.perf.rfx_rtt_ms,
          rfxLoss:             s.perf.rfx_loss_pct,
          rfxSkipServer:       s.perf.rfx_skip_server_sec,
          rfxSkipNet:          s.perf.rfx_skip_net_sec,
          rfxFpsOutP50:        s.perf.rfx_fps_out_p50,
          rfxEncodeMsP50:      s.perf.rfx_encode_ms_p50,
          rfxQualityP50:       s.perf.rfx_quality_pct_p50,
          rfxRttP50:           s.perf.rfx_rtt_ms_p50,
          rfxLossP50:          s.perf.rfx_loss_pct_p50,
          rfxSkipServerP50:    s.perf.rfx_skip_server_sec_p50,
          rfxSkipNetP50:       s.perf.rfx_skip_net_sec_p50,
        });
        if (hist.length > MAX_PERF_HISTORY) hist.splice(0, hist.length - MAX_PERF_HISTORY);
        perfHistory.set(host, hist);
      }

      // Fluctuate sessions
      if (s.status !== 'off') {
        const def = SERVERS.find(d => d.host === host);
        s.sessions = clamp(s.sessions + randInt(-3, 3), 0, def?.maxSessions ?? 50);
      }

      // Occasional status transition (~5% chance per tick per server)
      if (Math.random() < 0.05) {
        const transitions = {
          ok:      ['grace', 'warning'],
          warning: ['ok', 'alert'],
          grace:   ['ok', 'alert'],
          alert:   ['grace', 'off'],
          off:     ['ok'],
        };
        const prev = s.status;
        s.status = pick(transitions[prev] ?? ['ok']);
        s.changedBy = (s.status === 'ok' || s.status === 'warning') ? '' : pick(ADMINS);
        s.graceDeadline = s.status === 'grace' ? isoFuture(randInt(10, 45)) : null;
        s.stateChangedAt = isoNow();
        s.perf = s.status === 'off' ? null : genPerf(s.status);
        if (s.status === 'off') { s.sessions = 0; s.sessionsDisconnected = 0; }
        else if (prev === 'off') {
          const def = SERVERS.find(d => d.host === host);
          s.sessions = randInt(1, (def?.maxSessions ?? 30) / 2);
          s.sessionsDisconnected = randInt(0, 5);
        }

        // Record in history
        const hist = history.get(host) ?? [];
        hist.push({
          timestamp: isoNow(),
          host,
          status: s.status,
          drain_mode: (s.status === 'ok' || s.status === 'warning') ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
          state_duration_seconds: randInt(60, 3600),
          transition: true,
          transition_from: prev,
          changed_by: s.changedBy,
          version: '26.103.4',
          message: `Status changed: ${prev} → ${s.status}`,
        });
        // Cap at 100 entries
        if (hist.length > 100) hist.splice(0, hist.length - 100);
        history.set(host, hist);

        // Broadcast the state change to all connected SSE clients.
        broadcastSSE('server_update', serverView(host), host);
      }
    }
  }, 10_000); // every 10 seconds
}

// ---------------------------------------------------------------------------
// Build response objects
// ---------------------------------------------------------------------------

function serverView(host) {
  const s = state.get(host);
  if (!s) return null;
  const stateDurationSeconds = s.stateChangedAt
    ? Math.round((Date.now() - new Date(s.stateChangedAt).getTime()) / 1000)
    : null;
  const maxSessions = SERVERS.find(d => d.host === host)?.maxSessions ?? 0;
  const sessActive = s.sessions;
  const sessDisc = s.sessionsDisconnected ?? 0;
  const sessTotal = sessActive + sessDisc;
  return {
    host,
    status: s.status,
    drain_mode: (s.status === 'ok' || s.status === 'warning') ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
    sessions: sessActive,
    sessions_active: sessActive,
    sessions_disconnected: sessDisc,
    total_sessions: sessTotal,
    max_sessions: maxSessions,
    utilization_pct: maxSessions > 0 ? Math.round(sessTotal / maxSessions * 100) : 0,
    state_duration_seconds: stateDurationSeconds,
    state_changed_at: s.stateChangedAt,
    version: s.status === 'off' ? '' : '26.103.4',
    registered_at: s.registeredAt,
    last_seen: s.status === 'off' ? isoAgo(10) : isoNow(),
    changed_by: s.changedBy,
    grace_deadline: s.graceDeadline,
    perf: s.perf,
  };
}

function allServers() {
  return SERVERS.map(d => serverView(d.host)).filter(Boolean);
}

function healthResponse() {
  const servers = allServers();
  const counts = { total: servers.length, ok: 0, warning: 0, grace: 0, alert: 0, off: 0 };
  for (const s of servers) {
    if (s.status in counts) counts[s.status]++;
  }
  return { version: '26.103.4', servers: counts };
}

// ---------------------------------------------------------------------------
// Route handler
// ---------------------------------------------------------------------------

/**
 * Match a URL path against a pattern with :param placeholders.
 * Returns params object or null.
 */
function matchRoute(pattern, pathname) {
  const patParts = pattern.split('/');
  const urlParts = pathname.split('/');
  if (patParts.length !== urlParts.length) return null;
  const params = {};
  for (let i = 0; i < patParts.length; i++) {
    if (patParts[i].startsWith(':')) {
      params[patParts[i].slice(1)] = decodeURIComponent(urlParts[i]);
    } else if (patParts[i] !== urlParts[i]) {
      return null;
    }
  }
  return params;
}

function handleRequest(method, pathname, body, query = {}) {
  ensureState();

  // GET /api/v1/health
  if (method === 'GET' && pathname === '/api/v1/health') {
    return { status: 200, body: healthResponse() };
  }

  // GET /api/v1/servers
  if (method === 'GET' && pathname === '/api/v1/servers') {
    return { status: 200, body: allServers() };
  }

  // GET /api/v1/servers/:host
  const serverMatch = matchRoute('/api/v1/servers/:host', pathname);
  if (method === 'GET' && serverMatch) {
    const sv = serverView(serverMatch.host);
    if (!sv) return { status: 404, body: { error: 'server not found' } };
    return { status: 200, body: sv };
  }

  // DELETE /api/v1/servers/:host
  if (method === 'DELETE' && serverMatch) {
    state.delete(serverMatch.host);
    history.delete(serverMatch.host);
    broadcastSSE('server_deleted', { changed_by: 'admin' }, serverMatch.host);
    return { status: 204, body: null };
  }

  // GET /api/v1/history/:host
  const histMatch = matchRoute('/api/v1/history/:host', pathname);
  if (method === 'GET' && histMatch) {
    let entries = history.get(histMatch.host) ?? [];
    const limit = parseInt(query.limit) || 50;
    const changesOnly = query.changes_only === 'true';
    if (changesOnly) entries = entries.filter(e => e.transition);
    const reversed = [...entries].reverse();
    return { status: 200, body: reversed.slice(0, limit) };
  }

  // GET /api/v1/metrics/:host — durable per-host time-series for chart.svelte
  // (spec 007 / FR-019). Maps counter names to the perfHistory ring buffer
  // fields so the new chart renders during dev without a real SQLite store.
  const metricsMatch = matchRoute('/api/v1/metrics/:host', pathname);
  if (method === 'GET' && metricsMatch) {
    const host = metricsMatch.host;
    if (!state.has(host)) {
      return { status: 404, body: { error: 'unknown_host' } };
    }
    const from = query.from ? Date.parse(query.from) : NaN;
    const to = query.to ? Date.parse(query.to) : NaN;
    if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) {
      return { status: 400, body: { error: 'invalid_range' } };
    }
    const reqRes = query.resolution || 'auto';
    if (!['raw', '5min', 'hourly', 'auto'].includes(reqRes)) {
      return { status: 400, body: { error: 'invalid_resolution' } };
    }
    const counters = (query.counters || '').split(',').map(s => s.trim()).filter(Boolean);
    const wanted = counters.length > 0 ? counters : null;
    // perfHistory keys → counter names per contract/checkResultSamples().
    const counterMap = {
      cpu_pct:            'cpu',
      cpu_p95_pct:        'cpuP95',
      mem_avail_mb:       null, // derived below
      pages_sec:          'pagesPerSec',
      disk_queue:         'diskQueue',
      tcp_retrans_sec:    'tcpRetrans',
      input_delay_p95_ms: 'inputDelay',
      sessions_active:    'sessionsActive',
    };
    const hist = perfHistory.get(host) ?? [];
    const windowed = hist.filter(s => s.time >= from && s.time < to);
    const series = {};
    for (const [counter, field] of Object.entries(counterMap)) {
      if (wanted && !wanted.includes(counter)) continue;
      const t = [];
      const avg = [];
      for (const s of windowed) {
        let v;
        if (counter === 'mem_avail_mb') {
          v = 16384 * (1 - (s.mem ?? 0) / 100);
        } else {
          v = s[field];
        }
        if (v == null || !Number.isFinite(v)) continue;
        t.push(s.time);
        avg.push(Math.round(v * 10) / 10);
      }
      if (t.length === 0) continue;
      series[counter] = { t, avg, min: avg.slice(), max: avg.slice() };
    }
    const oldest = hist.length > 0 ? new Date(hist[0].time).toISOString() : null;
    const newest = hist.length > 0 ? new Date(hist[hist.length - 1].time).toISOString() : null;
    const tier = reqRes === 'auto' ? 'raw' : reqRes;
    return {
      status: 200,
      body: {
        host,
        tier,
        from: new Date(from).toISOString(),
        to: new Date(to).toISOString(),
        oldest_available: Object.keys(series).length === 0 ? null : oldest,
        newest_available: Object.keys(series).length === 0 ? null : newest,
        series,
      },
    };
  }

  // GET /api/v1/metrics — returns per-server perf history for sparkline seeding
  if (method === 'GET' && pathname === '/api/v1/metrics') {
    const result = Object.fromEntries(perfHistory);
    return { status: 200, body: result };
  }

  // GET /api/evtspike/status?host=<host> — evtspike detector status (feature 006).
  // Dev-mock rotates across the four states so the UI pill renders in all its
  // color variants without requiring a real backend.
  if (method === 'GET' && pathname === '/api/evtspike/status') {
    const host = query.host;
    if (!host || !state.has(host)) {
      return { status: 404, body: { error: 'unknown_host' } };
    }
    const evtStates = ['healthy', 'training', 'disabled', 'error'];
    let h = 0;
    for (let i = 0; i < host.length; i++) h = (h * 31 + host.charCodeAt(i)) | 0;
    const evtState = evtStates[Math.abs(h) % evtStates.length];
    return {
      status: 200,
      body: {
        host,
        state: evtState,
        enabled_channels: evtState === 'disabled' ? 0 : 54,
        mature_channels: evtState === 'healthy' ? 54 : evtState === 'training' ? 31 : 0,
        ...(evtState === 'error' ? { error_reason: 'EvtSubscribe failed: provider unavailable' } : {}),
      },
    };
  }

  // GET /api/evtspike/spikes?host=<host>&limit=<n> — recent confirmed spikes.
  // Dev-mock generates a deterministic 0..3-spike history per host so the
  // ServerDetail "Recent Spikes" tile renders in all its populated states
  // without a real backend.
  if (method === 'GET' && pathname === '/api/evtspike/spikes') {
    const host = query.host;
    if (!host || !state.has(host)) {
      return { status: 404, body: { error: 'unknown_host' } };
    }
    let h = 0;
    for (let i = 0; i < host.length; i++) h = (h * 31 + host.charCodeAt(i)) | 0;
    // Mostly populated (1–3 spikes) with ~12% of hosts showing the empty state
    // so the dev dashboard exercises both populated and empty paths of the tile.
    const mod = Math.abs(h) % 8;
    const count = mod === 0 ? 0 : 1 + (mod % 3);
    const channels = [
      'Microsoft-Windows-Winlogon/Operational',
      'Microsoft-Windows-TerminalServices-LocalSessionManager/Operational',
      'Application',
      'Security',
    ];
    const nowMs = Date.now();
    const limit = Math.min(50, Math.max(1, Number(query.limit ?? 20)));
    const spikes = [];
    for (let i = 0; i < Math.min(count, limit); i++) {
      const ageSec = 120 + i * 900 + Math.abs(h >> (i + 1)) % 600;
      const end = nowMs - ageSec * 1000;
      const start = end - 10_000;
      spikes.push({
        id: Math.abs(h) * 100 + i,
        host,
        channel: channels[(Math.abs(h) + i) % channels.length],
        window_start: new Date(start).toISOString(),
        window_end: new Date(end).toISOString(),
        observed: 35 + ((Math.abs(h) >> (i + 2)) & 0x3f),
        expected: 1.2 + (i * 0.6),
        tail_probability: 1e-6 * Math.pow(10, -(i + 1)),
        confirmation_count: 2 + (i % 2),
        first_seen_at: new Date(start - 20_000).toISOString(),
      });
    }
    return { status: 200, body: spikes };
  }

  // GET /api/v1/maintenance/status — mirrors contracts/http-maintenance.md
  if (method === 'GET' && pathname === '/api/v1/maintenance/status') {
    const now = Date.now();
    const iso = (ms) => new Date(ms).toISOString();
    return {
      status: 200,
      body: {
        jobs: [
          {
            name: 'aggregator_5min',
            started: iso(now - 45_000),
            finished: iso(now - 44_900),
            duration_ms: 100,
            outcome: 'success',
            reason: '',
            rows_affected: 300,
            overdue: false,
            expected_interval_seconds: 60,
          },
          {
            name: 'aggregator_hourly',
            started: iso(now - 15 * 60_000),
            finished: iso(now - 15 * 60_000 + 210),
            duration_ms: 210,
            outcome: 'success',
            reason: '',
            rows_affected: 72,
            overdue: false,
            expected_interval_seconds: 3600,
          },
          {
            name: 'retention',
            started: iso(now - 8 * 60_000),
            finished: iso(now - 8 * 60_000 + 4115),
            duration_ms: 4115,
            outcome: 'success',
            reason: '',
            rows_affected: 1287,
            overdue: false,
            expected_interval_seconds: 900,
          },
          {
            name: 'jsonl_migration',
            started: iso(now - 36 * 60 * 60_000),
            finished: iso(now - 36 * 60 * 60_000 + 812),
            duration_ms: 812,
            outcome: 'skipped',
            reason: 'already migrated',
            rows_affected: 0,
            overdue: false,
            expected_interval_seconds: 0,
          },
        ],
        server_time: iso(now),
      },
    };
  }

  // GET /api/v1/settings
  if (method === 'GET' && pathname === '/api/v1/settings') {
    return { status: 200, body: mockSettings };
  }

  // PUT /api/v1/settings
  if (method === 'PUT' && pathname === '/api/v1/settings') {
    if (body) {
      mockSettings = body;
      // Strip secrets before broadcast — mirrors the real backend's broadcastSettingsUpdate
      const redacted = {
        ...mockSettings,
        notifications: (mockSettings.notifications || []).map(n => ({ ...n, secret: '' })),
      };
      broadcastSSE('settings_update', redacted);
    }
    return { status: 200, body: { ok: true } };
  }

  // POST /api/v1/notify-test
  if (method === 'POST' && pathname === '/api/v1/notify-test') {
    return { status: 200, body: { ok: true, message: 'Test notification sent (mock)' } };
  }

  // POST /api/v1/auth/negotiate — always succeeds in dev mode
  if (method === 'POST' && pathname === '/api/v1/auth/negotiate') {
    return { status: 200, body: { username: 'DEV\\mockuser' } };
  }

  // POST /api/v1/auth/login — succeeds for any non-empty credentials
  if (method === 'POST' && pathname === '/api/v1/auth/login') {
    if (body && body.username && body.password) {
      return { status: 200, body: { username: body.username } };
    }
    return { status: 401, body: { error: 'invalid credentials' } };
  }

  // POST /api/v1/auth/logout — always succeeds
  if (method === 'POST' && pathname === '/api/v1/auth/logout') {
    return { status: 200, body: { ok: true } };
  }

  return null; // not handled
}

// ---------------------------------------------------------------------------
// Vite plugin
// ---------------------------------------------------------------------------

export default function mockApi() {
  return {
    name: 'drainctl-mock-api',
    configureServer(server) {
      startEvolution();

      server.middlewares.use((req, res, next) => {
        // Only intercept /api/ requests
        if (!req.url?.startsWith('/api/')) return next();

        const [pathname, qs] = req.url.split('?');
        const query = Object.fromEntries(new URLSearchParams(qs || ''));
        const method = req.method?.toUpperCase() ?? 'GET';

        // SSE endpoint — long-lived streaming response.
        if (method === 'GET' && pathname === '/api/v1/events') {
          res.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-cache',
            'Connection': 'keep-alive',
            'Access-Control-Allow-Origin': '*',
          });
          // Flush headers immediately so the browser establishes the stream.
          res.flushHeaders?.();

          sseClients.add(res);

          // Periodic keepalive comment to prevent proxy/browser connection timeouts.
          const heartbeat = setInterval(() => {
            try { res.write(': keepalive\n\n'); } catch { /* ignore */ }
          }, 25_000);

          req.on('close', () => {
            clearInterval(heartbeat);
            sseClients.delete(res);
          });
          return; // do NOT call next()
        }

        // Collect body for PUT/POST
        if (method === 'PUT' || method === 'POST') {
          let bodyStr = '';
          req.on('data', chunk => { bodyStr += chunk; });
          req.on('end', () => {
            let body = null;
            try { body = JSON.parse(bodyStr); } catch {}
            const result = handleRequest(method, pathname, body, query);
            sendResult(res, result, next);
          });
        } else {
          const result = handleRequest(method, pathname, null, query);
          sendResult(res, result, next);
        }
      });
    },
  };
}

function sendResult(res, result, next) {
  if (!result) return next();
  res.writeHead(result.status, {
    'Content-Type': 'application/json',
    'Access-Control-Allow-Origin': '*',
  });
  res.end(result.body != null ? JSON.stringify(result.body) : '');
}
