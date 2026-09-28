import assert from 'node:assert/strict';
import test from 'node:test';
import {
    mockSSEFrame,
    mockShadowCommand,
    mockShadowProtocolURI,
    sessionActionRequestIsValid,
    sessionActionSSEStatusFixture,
    sessionQueryIsValid,
} from './mock-api.js';

test('accepts the bounded Fleet Sessions query vocabulary', () => {
    assert.equal(sessionQueryIsValid({ state: 'active', sort: 'last_activity', dir: 'desc', page_size: '50' }), true);
    assert.equal(sessionQueryIsValid({}), true);
});

test('rejects unsupported session filters, sorts, directions, and page sizes', () => {
    for (const query of [{ state: 'unknown' }, { sort: 'user' }, { dir: 'sideways' }, { page_size: 20 }])
        assert.equal(sessionQueryIsValid(query), false);
});

test('accepts a disconnect request only with its exact no-message wire shape', () => {
    assert.equal(sessionActionRequestIsValid({ type: 'disconnect', expected_logon_at_ms: 1_700_000_000_000 }), true);
    assert.equal(
        sessionActionRequestIsValid({
            type: 'disconnect',
            expected_logon_at_ms: 1_700_000_000_000,
            message: 'not permitted',
        }),
        false,
    );
});

test('frames session snapshot as one named EventSource record', () => {
    const snapshot = { host: 'rdsh-01.contoso.com', session_count: 2 };
    assert.equal(
        mockSSEFrame('session_snapshot', snapshot, 'must-not-be-enveloped.contoso.com'),
        'event: session_snapshot\ndata: {"host":"rdsh-01.contoso.com","session_count":2}\n\n',
    );
});

test('frames session action as raw four-field named EventSource data', () => {
    const action = sessionActionSSEStatusFixture();
    const frame = mockSSEFrame('session_action', action, 'must-not-be-enveloped.contoso.com');
    assert.equal(
        frame,
        'event: session_action\ndata: {"action_id":"019b0000-0000-7000-8000-000000000001","state":"completed","completed_at_ms":1700000001000,"result_code":"completed"}\n\n',
    );
    const data = JSON.parse(frame.split('\n')[1].slice('data: '.length));
    assert.deepEqual(Object.keys(data), ['action_id', 'state', 'completed_at_ms', 'result_code']);
    assert.equal('host' in data, false);
    assert.equal('data' in data, false);
});

test('provides the endpoint-owned Shadow protocol URI and copy fallback command', () => {
    assert.equal(
        mockShadowProtocolURI('rdsh-01.contoso.com', 42),
        'drainctl-shadow://shadow?host=rdsh-01.contoso.com&session=42',
    );
    assert.equal(mockShadowCommand('rdsh-01.contoso.com', 42), 'mstsc.exe /v:rdsh-01.contoso.com /shadow:42 /control');
});
