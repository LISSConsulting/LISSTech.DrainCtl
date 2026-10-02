import test from 'node:test';
import assert from 'node:assert/strict';
import { shouldDisableExport } from '../lib/export-graph-config.js';

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

test('ExportControl names its graph for assistive technology', async () => {
    const source = await import('node:fs/promises').then((fs) => fs.readFile(new URL('./ExportControl.svelte', import.meta.url), 'utf8'));
    assert.match(source, /aria-label=.*export.*graphLabel/i);
});
