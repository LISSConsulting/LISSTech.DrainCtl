/** Query defaults and validation shared by the fleet sessions UI. */
export const SESSION_PAGE_SIZES = Object.freeze([15, 30, 50]);
export const SESSION_SORTS = Object.freeze([
    'host',
    'status',
    'mode',
    'sessions',
    'active',
    'idle',
    'disconnected',
    'users',
    'last_activity',
]);
export const SESSION_STATES = Object.freeze(['all', 'active', 'disconnected', 'idle']);

const CONTROL = /[\u0000-\u001f\u007f]/;
const HOSTNAME =
    /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$/;
const UINT32 = /^(?:0|[1-9]\d{0,9})$/;
const SHADOW_PROTOCOL_URI = /^drainctl-shadow:\/\/shadow\?host=([a-z0-9.-]+)&session=(0|[1-9]\d{0,9})$/;

/**
 * Normalize browser-controlled fleet query values before serializing them.
 * The API remains authoritative; this prevents accidental invalid requests.
 * @param {Partial<{q:string,state:string,sort:string,dir:string,page:number,page_size:number}>} query
 */
export function normalizeSessionsQuery(query = {}) {
    const rawQuery = String(query.q ?? '').trim();
    const q = CONTROL.test(rawQuery) || new TextEncoder().encode(rawQuery).length > 128 ? '' : rawQuery;
    const state = SESSION_STATES.includes(query.state) ? query.state : 'all';
    const sort = SESSION_SORTS.includes(query.sort) ? query.sort : 'host';
    const dir = query.dir === 'desc' ? 'desc' : 'asc';
    const page = Number.isInteger(query.page) && query.page >= 1 && query.page <= 10_000 ? query.page : 1;
    const page_size = SESSION_PAGE_SIZES.includes(query.page_size) ? query.page_size : 30;
    return { q, state, sort, dir, page, page_size };
}

/** @param {string|null|undefined} state */
export function sessionStateRank(state) {
    return (
        [
            'active',
            'connected',
            'connect_query',
            'shadow',
            'disconnected',
            'idle',
            'listen',
            'reset',
            'down',
            'init',
            'unknown',
        ].indexOf(state ?? '') + 1 || 99
    );
}

/** @param {string|null|undefined} status */
export function sessionStatusRank(status) {
    return ['alert', 'warning', 'grace', 'off', 'ok'].indexOf(status ?? '') + 1 || 99;
}

/** @param {string|null|undefined} freshness */
export function sessionFreshnessRank(freshness) {
    return ['fresh', 'stale', 'unavailable'].indexOf(freshness ?? '') + 1 || 99;
}

/**
 * The Sessions endpoints provide a server timestamp so the browser can measure
 * elapsed receipt time without trusting its wall-clock alignment.
 * @param {number} serverNowMs
 * @param {number} [receivedAtMs]
 * @returns {number|null}
 */
export function sessionServerTimeOffsetMs(serverNowMs, receivedAtMs = Date.now()) {
    if (!Number.isFinite(serverNowMs) || !Number.isFinite(receivedAtMs)) return null;
    return serverNowMs - receivedAtMs;
}

/**
 * Resolve the configured heartbeat used by the server freshness contract.
 * A missing or malformed setting intentionally yields null: treating an
 * unverified threshold as fresh would expose an action control unsafely.
 * @param {{poll_interval?:number}|null|undefined} config
 * @param {{heartbeat_interval_ms?:number}|null|undefined} [response]
 * @returns {number|null}
 */
export function effectiveSessionHeartbeatMs(config, response = undefined) {
    const responseInterval = Number(response?.heartbeat_interval_ms);
    if (Number.isFinite(responseInterval) && responseInterval >= 10_000 && responseInterval <= 86_400_000) {
        return responseInterval;
    }
    const configuredSeconds = Number(config?.poll_interval);
    if (!Number.isFinite(configuredSeconds) || configuredSeconds < 10 || configuredSeconds > 86_400) return null;
    return configuredSeconds * 1000;
}

/**
 * Compute current freshness from the server's last successful receipt. This
 * deliberately ignores the response freshness field, which becomes stale as
 * time passes without a new SSE event.
 * @param {{last_success_received_at_ms?:number|null,status?:string|null,mode?:string|null}} snapshot
 * @param {{serverTimeOffsetMs?:number|null,heartbeatMs?:number|null,nowMs?:number}} [clock]
 * @returns {'fresh'|'stale'|'offline'|'unknown'}
 */
export function deriveSessionFreshness(snapshot, clock = {}) {
    if (snapshot?.status === 'off' || snapshot?.mode === 'offline' || snapshot?.mode === 'off') return 'offline';
    const receivedAtMs = Number(snapshot?.last_success_received_at_ms);
    const heartbeatMs = Number(clock.heartbeatMs);
    const serverTimeOffsetMs = Number(clock.serverTimeOffsetMs);
    const nowMs = clock.nowMs ?? Date.now();
    if (!Number.isFinite(receivedAtMs) || !Number.isFinite(heartbeatMs) || !Number.isFinite(serverTimeOffsetMs)) {
        return 'unknown';
    }
    const ageMs = Math.max(0, nowMs + serverTimeOffsetMs - receivedAtMs);
    if (ageMs > 10 * heartbeatMs) return 'offline';
    if (ageMs > 3 * heartbeatMs) return 'stale';
    return 'fresh';
}

/**
 * Format canonical unsigned decimal byte strings without converting the value to Number.
 * Null and invalid values are unavailable rather than zero.
 * @param {string|bigint|number|null|undefined} value
 * @returns {string}
 */
export function formatDecimalBytes(value) {
    if (value === null || value === undefined || (typeof value === 'number' && !Number.isSafeInteger(value)))
        return '—';
    const raw = typeof value === 'bigint' ? value.toString() : String(value);
    if (!/^(?:0|[1-9]\d*)$/.test(raw)) return '—';
    const bytes = BigInt(raw);
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];
    let unit = 0;
    let divisor = 1n;
    while (unit < units.length - 1 && bytes >= divisor * 1024n) {
        divisor *= 1024n;
        unit++;
    }
    if (unit === 0) return `${bytes} B`;
    const whole = bytes / divisor;
    const tenth = ((bytes % divisor) * 10n) / divisor;
    return tenth === 0n ? `${whole} ${units[unit]}` : `${whole}.${tenth} ${units[unit]}`;
}

/** @param {string} host @param {number|string} sessionId */
export function isSafeShadowTarget(host, sessionId) {
    const normalizedHost = String(host ?? '').toLowerCase();
    if (!HOSTNAME.test(normalizedHost)) return false;
    const rawSessionId = String(sessionId ?? '');
    return UINT32.test(rawSessionId) && BigInt(rawSessionId) <= 4_294_967_295n;
}

/**
 * Accept only the exact protocol URI shape produced by the shadow endpoint.
 * The dashboard receives this launch target from the API; it never builds one.
 * @param {unknown} protocolURI
 * @param {string} host
 * @param {number|string} sessionId
 */
export function isSafeShadowProtocolURI(protocolURI, host, sessionId) {
    if (!isSafeShadowTarget(host, sessionId) || typeof protocolURI !== 'string') return false;
    const match = SHADOW_PROTOCOL_URI.exec(protocolURI);
    return match !== null && match[1] === String(host).toLowerCase() && match[2] === String(sessionId);
}

/**
 * Navigate only after an explicit UI action has received a valid endpoint URI.
 * @param {string} protocolURI
 * @param {string} host
 * @param {number|string} sessionId
 * @param {{assign?: (url:string) => void}|undefined} location
 * @returns {boolean} Whether browser navigation was requested.
 */
export function launchShadowProtocol(protocolURI, host, sessionId, location = globalThis.location) {
    if (!isSafeShadowProtocolURI(protocolURI, host, sessionId) || typeof location?.assign !== 'function') return false;
    try {
        location.assign(protocolURI);
        return true;
    } catch {
        return false;
    }
}

/**
 * Return the exact copy-only shadow command or null when the target is unsafe.
 * @param {string} host @param {number|string} sessionId
 */
export function buildShadowCommand(host, sessionId) {
    if (!isSafeShadowTarget(host, sessionId)) return null;
    return `mstsc.exe /v:${String(host).toLowerCase()} /shadow:${sessionId} /control`;
}

/**
 * Copy text using the asynchronous Clipboard API when it is available, with a
 * DOM-only fallback for non-secure development contexts.
 *
 * @param {string} text
 * @param {{navigator?: Pick<Navigator, 'clipboard'>, document?: Pick<Document, 'body'|'createElement'|'execCommand'>}} [environment]
 * @returns {Promise<boolean>} Whether the browser reported a successful copy.
 */
export async function copyText(text, { navigator = globalThis.navigator, document = globalThis.document } = {}) {
    if (navigator?.clipboard?.writeText) {
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch {
            return false;
        }
    }

    let textarea;
    try {
        if (!document?.body || !document.createElement || !document.execCommand) return false;

        textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.readOnly = true;
        textarea.style.position = 'fixed';
        textarea.style.left = '-9999px';
        textarea.style.top = '0';
        document.body.appendChild(textarea);
        textarea.select();
        return document.execCommand('copy') === true;
    } catch {
        return false;
    } finally {
        try {
            textarea?.parentNode?.removeChild(textarea);
        } catch {
            // A failed cleanup must not turn a copy failure into an exception.
        }
    }
}

const ACTIVE_ACTION_STATES = new Set(['queued', 'delivered']);

/**
 * Return a deterministic, bounded-friendly order for persisted pending actions.
 * Terminal/malformed records are ignored before polling.
 * @template T extends {{action_id?: string, state?: string, created_at_ms?: number}}
 * @param {readonly T[]} actions
 * @returns {T[]}
 */
export function orderPendingActions(actions) {
    return actions
        .filter((action) => typeof action?.action_id === 'string' && ACTIVE_ACTION_STATES.has(action.state))
        .slice()
        .sort(
            (a, b) =>
                Number(a.created_at_ms ?? 0) - Number(b.created_at_ms ?? 0) || a.action_id.localeCompare(b.action_id),
        );
}
