import assert from 'node:assert/strict';
import test from 'node:test';

import { DEFAULTS, getThresholdColor, resolveThresholds } from './thresholds.js';

test('higher-worse ignores disabled negative thresholds', () => {
    assert.equal(getThresholdColor(95, -1, -1), 'green');
    assert.equal(getThresholdColor(95, 70, -1), 'amber');
    assert.equal(getThresholdColor(95, -1, 85), 'red');
});

test('lower-worse ignores disabled negative thresholds', () => {
    assert.equal(getThresholdColor(5, -1, -1, 'lower-worse'), 'green');
    assert.equal(getThresholdColor(15, 20, -1, 'lower-worse'), 'amber');
    assert.equal(getThresholdColor(5, -1, 10, 'lower-worse'), 'red');
});

test('configured thresholds preserve the disabled sentinel and default zero values', () => {
    const config = {
        cpu_warn_pct: -1,
        cpu_crit_pct: 0,
        mem_warn_pct: 0,
        mem_crit_pct: -1,
        input_delay_warn_ms: -1,
        input_delay_crit_ms: 0,
    };

    assert.deepEqual(resolveThresholds('cpu', config), { warn: -1, crit: DEFAULTS.cpu.crit });
    assert.deepEqual(resolveThresholds('mem', config), { warn: DEFAULTS.mem.warn, crit: -1 });
    assert.deepEqual(resolveThresholds('inputDelay', config), {
        warn: -1,
        crit: DEFAULTS.inputDelay.crit,
    });
});
