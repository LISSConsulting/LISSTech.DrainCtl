import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.$state = (value) => value;
globalThis.$derived = (value) => value;
globalThis.$derived.by = (fn) => fn();
globalThis.$effect = () => {};
globalThis.$effect.root = () => () => {};

class MemorySessionStorage {
    values = new Map();

    getItem(key) {
        return this.values.get(key) ?? null;
    }

    setItem(key, value) {
        this.values.set(key, String(value));
    }

    removeItem(key) {
        this.values.delete(key);
    }
}

const sessionStorage = new MemorySessionStorage();
const {
    SESSION_ACTION_STORAGE_KEY,
    actionOutcomeMessage,
    clearSessionActionPolling,
    enqueueSessionAction,
    formatDecimalBytes,
    handleSessionActionEvent,
    isSafeShadowTarget,
    isTerminalActionState,
    resumeSessionActionPolling,
    configureSessionActionDependencies,
    shadowCommand,
    stopSessionActionPolling,
} = await import('./session-actions.svelte.js');

const notifications = [];
let fetchStatus = async () => {
    throw new Error('Unexpected session action status request');
};
configureSessionActionDependencies({
    storage: () => sessionStorage,
    fetchSessionActionStatus: (...args) => fetchStatus(...args),
    queueSessionAction: async () => {
        throw new Error('Unexpected session action queue request');
    },
    notify: (kind, message) => notifications.push({ kind, message }),
});

const { SESSION_ACTION_STATUS_KEYS, mockShadowCommand, sessionActionSSEStatusFixture, sessionActionStatusFixture } =
    await import('../../dev/mock-api.js');

const ACTION_ID = '019b0000-0000-7000-8000-000000000001';

function storePending(...actionIDs) {
    sessionStorage.setItem(SESSION_ACTION_STORAGE_KEY, JSON.stringify(actionIDs));
}

async function settlePolling() {
    await new Promise((resolve) => setImmediate(resolve));
}

test('formats canonical decimal byte strings without Number precision loss', () => {
    assert.equal(formatDecimalBytes('9007199254740993'), '8.0 PiB');
    assert.equal(formatDecimalBytes('0'), '0 B');
    assert.equal(formatDecimalBytes(null), '—');
    assert.equal(formatDecimalBytes('01'), '—');
});

test('creates only the exact safe copy-only shadow command', () => {
    assert.equal(shadowCommand('rdsh-07.example.test', 3), 'mstsc.exe /v:rdsh-07.example.test /shadow:3 /control');
    assert.equal(shadowCommand('bad host', 3), null);
    assert.equal(shadowCommand('rdsh-07.example.test', -1), null);
    assert.equal(isSafeShadowTarget('rdsh-07.example.test', 4294967295), true);
});

test('mock action statuses preserve the complete production projection and command order', () => {
    const action = sessionActionStatusFixture();
    assert.deepEqual(Object.keys(action), SESSION_ACTION_STATUS_KEYS);
    assert.equal(action.expected_logon_at_ms, 1_700_000_000_000);
    assert.equal(action.completed_at_ms, 1_700_000_001_000);
    assert.equal(action.result_code, 'completed');
    assert.equal(mockShadowCommand('rdsh-07.example.test', 3), shadowCommand('rdsh-07.example.test', 3));
});

test('mock action SSE status exposes exactly the four permitted metadata fields', () => {
    assert.deepEqual(Object.keys(sessionActionSSEStatusFixture()), [
        'action_id',
        'state',
        'completed_at_ms',
        'result_code',
    ]);
});

test('maps only durable terminal action states to outcomes', () => {
    assert.equal(isTerminalActionState('queued'), false);
    assert.equal(isTerminalActionState('delivered'), false);
    assert.equal(isTerminalActionState('session_changed'), true);
    assert.equal(
        actionOutcomeMessage({ type: 'logoff', state: 'session_changed' }),
        'Log off was not run because the session changed.',
    );
    assert.equal(actionOutcomeMessage({ type: 'message', state: 'expired' }), 'Message expired before delivery.');
    assert.equal(actionOutcomeMessage({ type: 'disconnect', state: 'completed' }), 'Disconnect completed.');
    assert.equal(
        actionOutcomeMessage({ type: 'disconnect', state: 'session_changed' }),
        'Disconnect was not run because the session changed.',
    );
});

test('surfaces the contract sessions-disabled error in the confirmation helper', async () => {
    configureSessionActionDependencies({
        storage: () => sessionStorage,
        fetchSessionActionStatus: (...args) => fetchStatus(...args),
        queueSessionAction: async () => {
            const error = new Error('conflict');
            error.detail = 'sessions_disabled';
            throw error;
        },
        notify: (kind, message) => notifications.push({ kind, message }),
    });
    await assert.rejects(
        enqueueSessionAction({
            host: 'rdsh-07.example.test',
            sessionId: 3,
            expectedLogonAtMs: 1_700_000_000_000,
            type: 'logoff',
        }),
        { message: 'Session actions are disabled.' },
    );
});

test('queues disconnect with only its required identity binding', async () => {
    clearSessionActionPolling();
    notifications.length = 0;
    let queued;
    configureSessionActionDependencies({
        storage: () => sessionStorage,
        fetchSessionActionStatus: (...args) => fetchStatus(...args),
        queueSessionAction: async (...args) => {
            queued = args;
            return { action_id: ACTION_ID, state: 'completed', type: 'disconnect' };
        },
        notify: (kind, message) => notifications.push({ kind, message }),
    });

    try {
        await enqueueSessionAction({
            host: 'rdsh-07.example.test',
            sessionId: 3,
            expectedLogonAtMs: 1_700_000_000_000,
            type: 'disconnect',
        });
        assert.deepEqual(queued.slice(0, 3), [
            'rdsh-07.example.test',
            3,
            { type: 'disconnect', expected_logon_at_ms: 1_700_000_000_000 },
        ]);
        assert.equal(queued[3].idempotencyKey.match(/^[0-9a-f-]{36}$/i) !== null, true);
        assert.deepEqual(notifications, [{ kind: 'info', message: 'Disconnect queued.' }]);
    } finally {
        clearSessionActionPolling();
    }
});

test('restores a persisted action from the admin shell regardless of the active view', async () => {
    clearSessionActionPolling();
    storePending(ACTION_ID);
    let calls = 0;
    fetchStatus = async () => {
        calls += 1;
        return { action_id: ACTION_ID, state: 'queued', type: 'logoff' };
    };

    try {
        // App.svelte invokes this after authentication, whether it renders
        // Overview or a collapsed Sessions view.
        resumeSessionActionPolling();
        await settlePolling();
        assert.equal(calls, 1);
    } finally {
        stopSessionActionPolling();
    }
});

test('duplicate shell and detail recovery share one poll loop', async () => {
    clearSessionActionPolling();
    storePending(ACTION_ID);
    let calls = 0;
    fetchStatus = async () => {
        calls += 1;
        return { action_id: ACTION_ID, state: 'queued', type: 'logoff' };
    };

    try {
        resumeSessionActionPolling();
        resumeSessionActionPolling();
        await settlePolling();
        assert.equal(calls, 1);
    } finally {
        stopSessionActionPolling();
    }
});

test('a stopped poll resumes from sessionStorage on the next admin view', async () => {
    clearSessionActionPolling();
    storePending(ACTION_ID);
    let calls = 0;
    fetchStatus = async () => {
        calls += 1;
        return { action_id: ACTION_ID, state: 'queued', type: 'logoff' };
    };

    try {
        resumeSessionActionPolling();
        await settlePolling();
        stopSessionActionPolling();
        resumeSessionActionPolling();
        await settlePolling();
        assert.equal(calls, 2);
    } finally {
        stopSessionActionPolling();
    }
});

test('logout or non-admin cleanup removes persisted actions and active recovery', async () => {
    clearSessionActionPolling();
    storePending(ACTION_ID);
    clearSessionActionPolling();
    let calls = 0;
    fetchStatus = async () => {
        calls += 1;
        return { action_id: ACTION_ID, state: 'queued', type: 'logoff' };
    };

    try {
        resumeSessionActionPolling();
        await settlePolling();
        assert.equal(sessionStorage.getItem(SESSION_ACTION_STORAGE_KEY), null);
        assert.equal(calls, 0);
    } finally {
        stopSessionActionPolling();
    }
});

test('terminal SSE acceleration reports its outcome once', async () => {
    clearSessionActionPolling();
    storePending(ACTION_ID);
    notifications.length = 0;
    fetchStatus = async () => ({ action_id: ACTION_ID, state: 'completed', type: 'logoff' });

    try {
        resumeSessionActionPolling();
        handleSessionActionEvent(JSON.stringify({ action_id: ACTION_ID, state: 'completed' }));
        await settlePolling();
        assert.deepEqual(notifications, [{ kind: 'ok', message: 'Log off completed.' }]);
    } finally {
        stopSessionActionPolling();
    }
});
