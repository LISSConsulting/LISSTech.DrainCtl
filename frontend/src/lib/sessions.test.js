import assert from 'node:assert/strict';
import test from 'node:test';
import {
    buildShadowCommand,
    copyText,
    formatDecimalBytes,
    isSafeShadowProtocolURI,
    isSafeShadowTarget,
    launchShadowProtocol,
    normalizeSessionsQuery,
    orderPendingActions,
    sessionFreshnessRank,
    sessionStateRank,
    sessionStatusRank,
} from './sessions.js';
import {
    mockSSEFrame,
    mockSessionMatchesQuery,
    SESSION_DETAIL_SESSION_KEYS,
    SESSION_PROCESS_KEYS,
    sessionDetailFixture,
} from '../../dev/mock-api.js';

test('normalizeSessionsQuery keeps only bounded API parameters', () => {
    assert.deepEqual(
        normalizeSessionsQuery({ q: ' rdsh-07 ', state: 'active', sort: 'users', dir: 'desc', page: 2, page_size: 50 }),
        {
            q: 'rdsh-07',
            state: 'active',
            sort: 'users',
            dir: 'desc',
            page: 2,
            page_size: 50,
        },
    );
    assert.deepEqual(
        normalizeSessionsQuery({ q: 'bad\nquery', state: 'bad', sort: 'bad', dir: 'up', page: 0, page_size: 20 }),
        {
            q: '',
            state: 'all',
            sort: 'host',
            dir: 'asc',
            page: 1,
            page_size: 30,
        },
    );
});

test('session ranks place unknown values last', () => {
    assert.ok(sessionStateRank('active') < sessionStateRank('idle'));
    assert.ok(sessionStateRank('idle') < sessionStateRank('unknown'));
    assert.ok(sessionStatusRank('alert') < sessionStatusRank('ok'));
    assert.ok(sessionStatusRank('ok') < sessionStatusRank('unexpected'));
    assert.ok(sessionFreshnessRank('fresh') < sessionFreshnessRank('unavailable'));
    assert.equal(sessionFreshnessRank('mystery'), 99);
});

test('formatDecimalBytes never loses precision', () => {
    assert.equal(formatDecimalBytes('123731968'), '118 MiB');
    assert.equal(formatDecimalBytes('9007199254740993'), '8 PiB');
    assert.equal(formatDecimalBytes(null), '—');
    assert.equal(formatDecimalBytes('1e6'), '—');
});

test('mock detail fixture retains the complete nullable session and process wire shapes', () => {
    const [session] = sessionDetailFixture();
    assert.deepEqual(Object.keys(session), SESSION_DETAIL_SESSION_KEYS);
    assert.equal(session.connect_at_ms !== undefined, true);
    assert.equal(session.disconnect_at_ms, null);
    assert.equal(session.idle_since_ms, null);
    assert.equal(session.cpu_percent, 3.4);
    assert.equal(session.working_set_bytes, '9223372036854775807');
    assert.deepEqual(Object.keys(session.remotefx), [
        'fps',
        'quality_percent',
        'encode_time_ms',
        'rtt_ms',
        'loss_percent',
        'server_skipped_fps',
        'network_skipped_fps',
    ]);
    assert.deepEqual(Object.keys(session.processes[0]), SESSION_PROCESS_KEYS);
});

test('mock search exposes session IDs and only fully visible identities', () => {
    const [session] = sessionDetailFixture();
    assert.equal(mockSessionMatchesQuery(session, String(session.session_id)), true);
    assert.equal(mockSessionMatchesQuery(session, session.user), true);
    assert.equal(mockSessionMatchesQuery(session, session.domain), true);
    assert.equal(mockSessionMatchesQuery(session, session.user, 'masked'), false);
});

test('mock SSE uses named raw session frames', () => {
    const snapshot = mockSSEFrame('session_snapshot', { host: 'rdsh-01.contoso.com' }, 'rdsh-01.contoso.com');
    assert.match(snapshot, /^event: session_snapshot\ndata: {"host":"rdsh-01\.contoso\.com"}\n\n$/);
    const action = mockSSEFrame('session_action', { action_id: 'action-1' }, 'rdsh-01.contoso.com');
    assert.match(action, /^event: session_action\ndata: {"action_id":"action-1"}\n\n$/);
});

test('shadow command and protocol URI require exact canonical safe targets', () => {
    assert.equal(buildShadowCommand('RDSH-07.example.test', 3), 'mstsc.exe /v:rdsh-07.example.test /shadow:3 /control');
    assert.equal(
        isSafeShadowProtocolURI(
            'drainctl-shadow://shadow?host=rdsh-07.example.test&session=3',
            'RDSH-07.example.test',
            3,
        ),
        true,
    );
    assert.equal(
        isSafeShadowProtocolURI(
            'drainctl-shadow://shadow?host=rdsh-07.example.test&session=3&extra=1',
            'RDSH-07.example.test',
            3,
        ),
        false,
    );
    assert.equal(
        isSafeShadowProtocolURI(
            'drainctl-shadow://shadow?host=rdsh-07.example.test&session=4',
            'RDSH-07.example.test',
            3,
        ),
        false,
    );
    assert.equal(isSafeShadowTarget('rdsh.example.test; calc', 3), false);
    assert.equal(buildShadowCommand('rdsh.example.test', '03'), null);
    assert.equal(buildShadowCommand('rdsh.example.test', 4294967296), null);
});

test('Shadow navigation is explicit and rejects invalid endpoint URIs without requesting a launch', () => {
    const requests = [];
    const location = { assign: (uri) => requests.push(uri) };
    const validURI = 'drainctl-shadow://shadow?host=rdsh-07.example.test&session=3';

    assert.equal(launchShadowProtocol(validURI, 'rdsh-07.example.test', 3, location), true);
    assert.deepEqual(requests, [validURI]);
    assert.equal(
        launchShadowProtocol(
            'drainctl-shadow://shadow?host=rdsh-07.example.test&session=3&extra=1',
            'rdsh-07.example.test',
            3,
            location,
        ),
        false,
    );
    assert.deepEqual(requests, [validURI]);
});

function copyFallbackDocument({ copyResult = true, copyError } = {}) {
    const appended = [];
    let selected = false;
    let copied;
    const body = {
        appendChild(textarea) {
            textarea.parentNode = this;
            appended.push(textarea);
        },
        removeChild(textarea) {
            const index = appended.indexOf(textarea);
            if (index >= 0) appended.splice(index, 1);
            textarea.parentNode = null;
        },
    };
    return {
        document: {
            body,
            createElement() {
                return {
                    style: {},
                    select() {
                        selected = true;
                    },
                };
            },
            execCommand(command) {
                copied = appended[0]?.value;
                if (copyError) throw copyError;
                assert.equal(command, 'copy');
                return copyResult;
            },
        },
        copied: () => copied,
        selected: () => selected,
        appended,
    };
}

test('copyText uses the Clipboard API for the exact shadow command', async () => {
    const command = buildShadowCommand('RDSH-07.example.test', 3);
    let copied;
    const copiedSuccessfully = await copyText(command, {
        navigator: { clipboard: { writeText: async (text) => (copied = text) } },
    });

    assert.equal(copiedSuccessfully, true);
    assert.equal(copied, command);
});

test('copyText falls back to a temporary selected textarea for the exact shadow command', async () => {
    const command = buildShadowCommand('RDSH-07.example.test', 3);
    const fallback = copyFallbackDocument();

    assert.equal(await copyText(command, { navigator: {}, document: fallback.document }), true);
    assert.equal(fallback.copied(), command);
    assert.equal(fallback.selected(), true);
    assert.equal(fallback.appended.length, 0);
});

test('copyText reports Clipboard API denials and fallback failures without leaving a textarea', async () => {
    const command = buildShadowCommand('RDSH-07.example.test', 3);
    assert.equal(
        await copyText(command, {
            navigator: { clipboard: { writeText: async () => Promise.reject(new Error('denied')) } },
        }),
        false,
    );

    for (const options of [{ copyResult: false }, { copyError: new Error('denied') }]) {
        const fallback = copyFallbackDocument(options);
        assert.equal(await copyText(command, { navigator: {}, document: fallback.document }), false);
        assert.equal(fallback.appended.length, 0);
    }
});

test('pending action order excludes terminal records and breaks ties by ID', () => {
    const ordered = orderPendingActions([
        { action_id: 'b', state: 'queued', created_at_ms: 2 },
        { action_id: 'z', state: 'completed', created_at_ms: 0 },
        { action_id: 'c', state: 'delivered', created_at_ms: 1 },
        { action_id: 'a', state: 'queued', created_at_ms: 2 },
    ]);
    assert.deepEqual(
        ordered.map((entry) => entry.action_id),
        ['c', 'a', 'b'],
    );
});
