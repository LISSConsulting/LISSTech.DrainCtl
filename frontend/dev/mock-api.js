/**
 * mock-api.js — Vite plugin that serves realistic mock data for the
 * DrainCtl dashboard API during local development.
 *
 * Usage: add mockApi() to the plugins array in vite.config.js.
 *
 * Data is dynamic — server statuses shift over time, perf metrics jitter,
 * sessions fluctuate — so the dashboard feels alive while iterating on UI.
 */

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
// Server definitions — realistic RDS farm
// ---------------------------------------------------------------------------

const SERVERS = [
  { host: 'RDSH01.contoso.com', role: 'primary',   maxSessions: 50 },
  { host: 'RDSH02.contoso.com', role: 'primary',   maxSessions: 50 },
  { host: 'RDSH03.contoso.com', role: 'primary',   maxSessions: 50 },
  { host: 'RDSH04.contoso.com', role: 'secondary', maxSessions: 30 },
  { host: 'RDSH05.contoso.com', role: 'secondary', maxSessions: 30 },
  { host: 'RDSH06.contoso.com', role: 'standby',   maxSessions: 30 },
];

const ADMINS = ['admin@contoso', 'svc-rds@contoso', 'jsmith@contoso', ''];

// ---------------------------------------------------------------------------
// Stateful mock data — evolves over time
// ---------------------------------------------------------------------------

/** @type {Map<string, { status: string, sessions: number, changedBy: string, graceDeadline: string|null, registeredAt: string, perf: object|null }>} */
const state = new Map();

/** Per-host history ring buffers. @type {Map<string, object[]>} */
const history = new Map();

/** Notification config (mutable via PUT). */
let notifyConfig = {
  grace_period: 45,
  session_warning_threshold: 80,
  performance: {
    enabled: true,
    force_disabled: false,
    sample_interval_sec: 30,
    cpu_warn_pct: 70,
    cpu_crit_pct: 90,
    mem_warn_pct: 80,
    mem_crit_pct: 95,
    input_delay_warn_ms: 30,
    input_delay_crit_ms: 80,
    collect_per_session: false,
    collect_remotefx: false,
  },
  notifications: [
    {
      type: 'webhook',
      url: 'https://hooks.example.com/drainctl',
      secret: '',
      triggers: ['alert', 'grace', 'recovery'],
      repeat_minutes: 15,
      enabled: true,
    },
    {
      type: 'ntfy',
      url: 'https://ntfy.example.com/drainctl-alerts',
      secret: '',
      triggers: ['alert'],
      repeat_minutes: 0,
      enabled: true,
    },
  ],
};

/** Initialise server state on first access. */
function ensureState() {
  if (state.size > 0) return;
  const statuses = ['ok', 'ok', 'ok', 'grace', 'alert', 'off'];
  for (let i = 0; i < SERVERS.length; i++) {
    const def = SERVERS[i];
    const status = statuses[i];
    const sessions = status === 'off' ? 0 : randInt(2, def.maxSessions);
    state.set(def.host, {
      status,
      sessions,
      changedBy: status === 'ok' ? '' : pick(ADMINS),
      graceDeadline: status === 'grace' ? isoFuture(randInt(10, 45)) : null,
      registeredAt: isoAgo(randInt(60 * 24 * 7, 60 * 24 * 30)),
      perf: status === 'off' ? null : genPerf(status),
    });
    // Seed history
    history.set(def.host, seedHistory(def.host, status));
  }
}

/** Generate realistic perf metrics for a given status. */
function genPerf(status) {
  const base = {
    ok:    { cpu: [15, 55], mem: [40, 65], delay: [3, 15],  disk: [0.1, 0.5], pages: [5, 30] },
    grace: { cpu: [40, 70], mem: [55, 78], delay: [15, 40], disk: [0.3, 1.2], pages: [20, 80] },
    alert: { cpu: [65, 95], mem: [75, 95], delay: [35, 90], disk: [1.0, 3.0], pages: [60, 200] },
  }[status] ?? { cpu: [15, 55], mem: [40, 65], delay: [3, 15], disk: [0.1, 0.5], pages: [5, 30] };

  const cpu = rand(...base.cpu);
  const memTotalMb = 16384;
  const memUsedPct = rand(...base.mem) / 100;
  const memAvailMb = Math.round(memTotalMb * (1 - memUsedPct));
  const p50 = rand(...base.delay);
  const p95 = p50 * rand(1.5, 3.0);
  const max = p95 * rand(1.2, 2.0);

  return {
    cpu_pct:             Math.round(cpu * 10) / 10,
    mem_avail_mb:        memAvailMb,
    mem_total_mb:        memTotalMb,
    pages_sec:           Math.round(rand(...base.pages) * 10) / 10,
    disk_queue:          Math.round(rand(...base.disk) * 100) / 100,
    tcp_retrans_sec:     Math.round(rand(0, 3) * 10) / 10,
    input_delay_p50_ms:  Math.round(p50 * 10) / 10,
    input_delay_p95_ms:  Math.round(p95 * 10) / 10,
    input_delay_max_ms:  Math.round(max * 10) / 10,
  };
}

/** Jitter existing perf metrics slightly (simulates live variation). */
function jitterPerf(perf) {
  if (!perf) return null;
  const j = (v, pct = 0.08) => {
    const delta = v * pct;
    return Math.round(clamp(v + rand(-delta, delta), 0, 100000) * 10) / 10;
  };
  return {
    ...perf,
    cpu_pct:            j(perf.cpu_pct),
    mem_avail_mb:       Math.round(j(perf.mem_avail_mb, 0.03)),
    pages_sec:          j(perf.pages_sec, 0.15),
    disk_queue:         Math.round(j(perf.disk_queue, 0.12) * 100) / 100,
    tcp_retrans_sec:    j(perf.tcp_retrans_sec, 0.2),
    input_delay_p50_ms: j(perf.input_delay_p50_ms, 0.1),
    input_delay_p95_ms: j(perf.input_delay_p95_ms, 0.1),
    input_delay_max_ms: j(perf.input_delay_max_ms, 0.1),
  };
}

/** Seed a history ring buffer with plausible past entries. */
function seedHistory(host, currentStatus) {
  const entries = [];
  const statuses = ['ok', 'grace', 'alert', 'ok', 'ok'];
  let prevStatus = 'ok';
  for (let i = 20; i >= 1; i--) {
    const s = i === 1 ? currentStatus : pick(statuses);
    const transition = s !== prevStatus;
    entries.push({
      timestamp: isoAgo(i * 30),
      host,
      status: s,
      drain_mode: s === 'ok' ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
      state_duration_seconds: randInt(120, 7200),
      transition,
      transition_from: transition ? prevStatus : undefined,
      changed_by: transition && s !== 'ok' ? pick(ADMINS) : undefined,
      version: '26.100.9',
      message: transition
        ? `Status changed: ${prevStatus} → ${s}`
        : `Check-in: ${s}`,
    });
    prevStatus = s;
  }
  return entries;
}

// ---------------------------------------------------------------------------
// Periodic state evolution — statuses shift, metrics jitter
// ---------------------------------------------------------------------------

let evolveTimer = null;

function startEvolution() {
  if (evolveTimer) return;
  evolveTimer = setInterval(() => {
    ensureState();
    for (const [host, s] of state) {
      // Jitter perf
      s.perf = jitterPerf(s.perf);

      // Fluctuate sessions
      if (s.status !== 'off') {
        const def = SERVERS.find(d => d.host === host);
        s.sessions = clamp(s.sessions + randInt(-3, 3), 0, def?.maxSessions ?? 50);
      }

      // Occasional status transition (~5% chance per tick per server)
      if (Math.random() < 0.05) {
        const transitions = {
          ok:    ['grace'],
          grace: ['ok', 'alert'],
          alert: ['grace', 'off'],
          off:   ['ok'],
        };
        const prev = s.status;
        s.status = pick(transitions[prev] ?? ['ok']);
        s.changedBy = s.status === 'ok' ? '' : pick(ADMINS);
        s.graceDeadline = s.status === 'grace' ? isoFuture(randInt(10, 45)) : null;
        s.perf = s.status === 'off' ? null : genPerf(s.status);
        if (s.status === 'off') s.sessions = 0;
        else if (prev === 'off') {
          const def = SERVERS.find(d => d.host === host);
          s.sessions = randInt(1, (def?.maxSessions ?? 30) / 2);
        }

        // Record in history
        const hist = history.get(host) ?? [];
        hist.push({
          timestamp: isoNow(),
          host,
          status: s.status,
          drain_mode: s.status === 'ok' ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
          state_duration_seconds: randInt(60, 3600),
          transition: true,
          transition_from: prev,
          changed_by: s.changedBy,
          version: '26.100.9',
          message: `Status changed: ${prev} → ${s.status}`,
        });
        // Cap at 100 entries
        if (hist.length > 100) hist.splice(0, hist.length - 100);
        history.set(host, hist);
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
  return {
    host,
    status: s.status,
    drain_mode: s.status === 'ok' ? 'ALLOW_ALL_CONNECTIONS' : 'ALLOW_RECONNECTIONS_PREVENT_NEW_LOGONS',
    sessions: s.sessions,
    max_sessions: SERVERS.find(d => d.host === host)?.maxSessions ?? 0,
    version: s.status === 'off' ? '' : '26.100.9',
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
  const counts = { total: servers.length, ok: 0, grace: 0, alert: 0, off: 0 };
  for (const s of servers) counts[s.status]++;
  return { version: '26.100.9', servers: counts };
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

function handleRequest(method, pathname, body) {
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
    return { status: 204, body: null };
  }

  // GET /api/v1/history/:host
  const histMatch = matchRoute('/api/v1/history/:host', pathname);
  if (method === 'GET' && histMatch) {
    const entries = history.get(histMatch.host) ?? [];
    // Return newest-first, respect limit param
    const reversed = [...entries].reverse();
    return { status: 200, body: reversed.slice(0, 50) };
  }

  // GET /api/v1/notify-config
  if (method === 'GET' && pathname === '/api/v1/notify-config') {
    return { status: 200, body: notifyConfig };
  }

  // PUT /api/v1/notify-config
  if (method === 'PUT' && pathname === '/api/v1/notify-config') {
    if (body) notifyConfig = body;
    return { status: 200, body: { ok: true } };
  }

  // POST /api/v1/notify-test
  if (method === 'POST' && pathname === '/api/v1/notify-test') {
    return { status: 200, body: { ok: true, message: 'Test notification sent (mock)' } };
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

        const pathname = req.url.split('?')[0];
        const method = req.method?.toUpperCase() ?? 'GET';

        // Collect body for PUT/POST
        if (method === 'PUT' || method === 'POST') {
          let bodyStr = '';
          req.on('data', chunk => { bodyStr += chunk; });
          req.on('end', () => {
            let body = null;
            try { body = JSON.parse(bodyStr); } catch {}
            const result = handleRequest(method, pathname, body);
            sendResult(res, result, next);
          });
        } else {
          const result = handleRequest(method, pathname, null);
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
