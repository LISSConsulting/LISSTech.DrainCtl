import test from 'node:test';
import assert from 'node:assert/strict';
import { exportAriaLabel, shouldDisableExport } from '../lib/export-graph-config.js';

const valid = { loading: false, failed: false, empty: false, hasEnabledSeries: true, isStale: false };

test('shouldDisableExport disables every invalid chart state', () => {
    for (const key of ['loading', 'failed', 'empty', 'isStale']) {
        assert.equal(shouldDisableExport({ ...valid, [key]: true }), true, key);
    }
    assert.equal(shouldDisableExport({ ...valid, hasEnabledSeries: false }), true, 'no enabled series');
});

test('shouldDisableExport allows a current chart with enabled data', () => {
    assert.equal(shouldDisableExport(valid), false);
});

test('export aria label identifies the graph', () => {
    assert.equal(exportAriaLabel({ label: 'Overview Load' }), 'Export Overview Load');
});
