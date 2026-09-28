import assert from 'node:assert/strict';
import test from 'node:test';

import {
    DEFAULT_LOAD_VISIBILITY,
    LOAD_SERIES_META,
    PRIMARY_METRIC_FILL_OPACITY,
    SECONDARY_METRIC_COLOR,
    SECONDARY_METRIC_FILL_OPACITY,
    SINGLE_METRIC_FILL_OPACITY,
    hostLoadVisibilityKey,
    readLoadVisibility,
    writeLoadVisibility,
} from './chart-contracts.js';

function storage(initial = {}) {
    const values = new Map(Object.entries(initial));
    return {
        getItem: (key) => values.get(key) ?? null,
        setItem: (key, value) => values.set(key, value),
        value: (key) => values.get(key),
    };
}

test('load visibility defaults safely on missing or malformed storage', () => {
    assert.deepEqual(readLoadVisibility(storage(), 'x'), DEFAULT_LOAD_VISIBILITY);
    assert.deepEqual(readLoadVisibility(storage({ x: '{bad json' }), 'x'), DEFAULT_LOAD_VISIBILITY);
});

test('load visibility persists known toggles and ignores unknown values', () => {
    const s = storage({ x: JSON.stringify({ cpu: false, cpuP95: true, mem: 'no', sessions: false, extra: false }) });
    assert.deepEqual(readLoadVisibility(s, 'x'), { cpu: false, cpuP95: true, mem: true, sessions: false });

    writeLoadVisibility(s, 'x', { cpu: true, cpuP95: false, mem: false, sessions: true, extra: false });
    assert.deepEqual(JSON.parse(s.value('x')), { cpu: true, cpuP95: false, mem: false, sessions: true });
});

test('host visibility keys isolate canonicalized hostnames', () => {
    assert.equal(hostLoadVisibilityKey(' RDS01.Example.COM '), 'drainctl:chart-visibility:host-load:rds01.example.com');
    assert.notEqual(hostLoadVisibilityKey('RDS01'), hostLoadVisibilityKey('RDS02'));
});

test('LOAD contract preserves foreground order, translucent fills, and stable colors', () => {
    const byForeground = [...LOAD_SERIES_META].sort((a, b) => b.foregroundRank - a.foregroundRank);
    assert.deepEqual(
        byForeground.map(({ key, label, color, lineOnly, fillOpacity, dash = '' }) => ({
            key,
            label,
            color,
            lineOnly,
            fillOpacity,
            dash,
        })),
        [
            {
                key: 'cpu',
                label: 'CPU AVG',
                color: 'var(--color-accent)',
                lineOnly: false,
                fillOpacity: 0.68,
                dash: '',
            },
            {
                key: 'mem',
                label: 'Memory',
                color: 'var(--color-green)',
                lineOnly: false,
                fillOpacity: 0.58,
                dash: '',
            },
            {
                key: 'sessions',
                label: 'Sessions',
                color: 'var(--color-blue)',
                lineOnly: false,
                fillOpacity: 0.5,
                dash: '7,4',
            },
            {
                key: 'cpuP95',
                label: 'CPU P95',
                color: 'var(--color-amber)',
                lineOnly: false,
                fillOpacity: 0.42,
                dash: '',
            },
        ],
    );
    assert.equal(SECONDARY_METRIC_COLOR, 'var(--color-series-secondary)');
    assert.equal(PRIMARY_METRIC_FILL_OPACITY, 0.46);
    assert.equal(SECONDARY_METRIC_FILL_OPACITY, 0.34);
    assert.equal(SINGLE_METRIC_FILL_OPACITY, 0.58);
});
