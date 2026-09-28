/** Session action lifecycle helpers. Pending records deliberately contain IDs only. */

let dependencies = null;

/**
 * Configure the browser-facing dependencies once the authenticated admin shell is available.
 * Repeating an identical configuration is intentionally a no-op.
 */
export function configureSessionActionDependencies(next) {
    const required = ['fetchSessionActionStatus', 'queueSessionAction', 'notify', 'storage'];
    if (!next || required.some((name) => typeof next[name] !== 'function')) {
        throw new TypeError('Session action dependencies must provide status, queue, notify, and storage functions.');
    }
    if (dependencies && required.every((name) => dependencies[name] === next[name])) return;
    dependencies = { ...next };
}

function notify(kind, message) {
    if (dependencies) dependencies.notify(kind, message);
}

export const SESSION_ACTION_STORAGE_KEY = 'drainctl:pending-session-actions';
export const TERMINAL_ACTION_STATES = new Set(['completed', 'failed', 'expired', 'session_changed', 'unsupported']);
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const RFC1123_HOST =
    /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$/;

const timers = new Map();
const controllers = new Map();
let pending = $state(new Map());

export const sessionActions = {
    get pending() {
        return pending;
    },
};

export function isTerminalActionState(state) {
    return TERMINAL_ACTION_STATES.has(state);
}

export function isSafeShadowTarget(host, sessionId) {
    return (
        RFC1123_HOST.test(String(host)) &&
        Number.isInteger(Number(sessionId)) &&
        Number(sessionId) >= 0 &&
        Number(sessionId) <= 4294967295
    );
}

export function shadowCommand(host, sessionId) {
    if (!isSafeShadowTarget(host, sessionId)) return null;
    return `mstsc.exe /v:${host} /shadow:${Number(sessionId)} /control`;
}

/** Format a decimal byte string without converting it to Number. */
export function formatDecimalBytes(value) {
    if (value === null || value === undefined || value === '') return '—';
    const raw = String(value);
    if (!/^(?:0|[1-9]\d*)$/.test(raw)) return '—';
    let bytes;
    try {
        bytes = BigInt(raw);
    } catch {
        return '—';
    }
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    let unit = 0;
    let divisor = 1n;
    while (unit < units.length - 1 && bytes >= divisor * 1024n) {
        divisor *= 1024n;
        unit += 1;
    }
    if (unit === 0) return `${bytes} B`;
    const whole = bytes / divisor;
    const decimal = ((bytes % divisor) * 10n) / divisor;
    return `${whole}.${decimal} ${units[unit]}`;
}

export function actionOutcomeMessage(action) {
    const subject = action?.type === 'message' ? 'Message' : action?.type === 'disconnect' ? 'Disconnect' : 'Log off';
    switch (action?.state) {
        case 'completed':
            return `${subject} completed.`;
        case 'failed':
            return `${subject} failed.`;
        case 'expired':
            return `${subject} expired before delivery.`;
        case 'session_changed':
            return `${subject} was not run because the session changed.`;
        case 'unsupported':
            return `${subject} is unsupported by the host.`;
        default:
            return `${subject} status updated.`;
    }
}

function storage() {
    try {
        return dependencies?.storage() ?? null;
    } catch {
        return null;
    }
}

function readStoredIDs() {
    try {
        const value = JSON.parse(storage()?.getItem(SESSION_ACTION_STORAGE_KEY) ?? '[]');
        return Array.isArray(value)
            ? value.filter((id) => typeof id === 'string' && UUID_PATTERN.test(id)).slice(0, 20)
            : [];
    } catch {
        return [];
    }
}

function saveStoredIDs() {
    const ids = [...pending.keys()].slice(0, 20);
    try {
        storage()?.setItem(SESSION_ACTION_STORAGE_KEY, JSON.stringify(ids));
    } catch {
        /* unavailable storage is non-fatal */
    }
}

function removePending(actionID) {
    if (!actionID) return false;
    const existed = pending.has(actionID);
    const next = new Map(pending);
    next.delete(actionID);
    pending = next;
    const timer = timers.get(actionID);
    if (timer) clearInterval(timer);
    timers.delete(actionID);
    controllers.get(actionID)?.abort();
    controllers.delete(actionID);
    saveStoredIDs();
    return existed;
}

function setPending(action) {
    if (!action?.action_id || isTerminalActionState(action.state)) return removePending(action?.action_id);
    pending = new Map(pending).set(action.action_id, {
        action_id: action.action_id,
        state: action.state,
        type: action.type ?? pending.get(action.action_id)?.type,
    });
    saveStoredIDs();
}

async function readStatus(actionID) {
    const previous = controllers.get(actionID);
    previous?.abort();
    const controller = new AbortController();
    controllers.set(actionID, controller);
    try {
        if (controllers.get(actionID) !== controller) return;
        const action = await dependencies.fetchSessionActionStatus(actionID, { signal: controller.signal });
        if (controllers.get(actionID) !== controller || !action || action.action_id !== actionID) return;
        if (isTerminalActionState(action.state)) {
            if (!removePending(actionID)) return;
            notify(action.state === 'completed' ? 'ok' : 'err', actionOutcomeMessage(action));
        } else {
            setPending(action);
        }
    } catch (error) {
        if (controllers.get(actionID) !== controller) return;
        if (error?.status === 404) removePending(actionID);
        // A network failure is retried on the next bounded polling tick.
    }
}

export function trackSessionAction(action) {
    if (!dependencies || !action?.action_id) return;
    setPending(action);
    if (isTerminalActionState(action.state)) return;
    if (!timers.has(action.action_id)) {
        readStatus(action.action_id);
        timers.set(
            action.action_id,
            setInterval(() => readStatus(action.action_id), 2000),
        );
    }
}

export function stopSessionActionPolling() {
    for (const timer of timers.values()) clearInterval(timer);
    for (const controller of controllers.values()) controller.abort();
    timers.clear();
    controllers.clear();
}

export function resumeSessionActionPolling() {
    if (!dependencies) return;
    for (const actionID of readStoredIDs()) trackSessionAction({ action_id: actionID, state: 'queued' });
}

export function clearSessionActionPolling() {
    stopSessionActionPolling();
    pending = new Map();
    try {
        storage()?.removeItem(SESSION_ACTION_STORAGE_KEY);
    } catch {
        /* unavailable storage is non-fatal */
    }
}

/** SSE is advisory: refetch the authoritative status rather than trusting its payload. */
export function handleSessionActionEvent(event) {
    let data;
    try {
        data = typeof event === 'string' ? JSON.parse(event) : JSON.parse(event.data);
    } catch {
        return;
    }
    if (data && pending.has(data.action_id)) readStatus(data.action_id);
}

function uuid() {
    if (!globalThis.crypto?.randomUUID) throw new Error('Secure action IDs are unavailable in this browser.');
    return globalThis.crypto.randomUUID();
}

/** Normalize exactly as the action request fingerprint does. */
export function normalizeSessionActionMessage(message) {
    return String(message ?? '').trim();
}

/** This is only called by the confirmation dialog's explicit confirm button. */
export async function enqueueSessionAction({ host, sessionId, expectedLogonAtMs, type, message = '' }) {
    if (!dependencies) throw new Error('Session actions are unavailable.');
    const body = { type, expected_logon_at_ms: expectedLogonAtMs };
    if (type === 'message') body.message = normalizeSessionActionMessage(message);
    let action;
    try {
        action = await dependencies.queueSessionAction(host, sessionId, body, { idempotencyKey: uuid() });
    } catch (cause) {
        const code = cause?.detail;
        throw new Error(
            code === 'session_identity_changed'
                ? 'The session changed; review it before trying again.'
                : code === 'actions_unsupported'
                  ? 'This host no longer supports session actions.'
                  : code === 'host_not_fresh'
                    ? 'This host is no longer fresh.'
                    : code === 'sessions_disabled'
                      ? 'Session actions are disabled.'
                      : code === 'idempotency_conflict'
                        ? 'This action request conflicts with an earlier request.'
                        : 'Could not queue the session action.',
        );
    }
    if (!action?.action_id) throw new Error('Could not queue the session action.');
    trackSessionAction(action);
    notify('info', `${type === 'message' ? 'Message' : type === 'disconnect' ? 'Disconnect' : 'Log off'} queued.`);
    return action;
}

if (typeof window !== 'undefined') window.addEventListener('pagehide', stopSessionActionPolling, { once: true });
