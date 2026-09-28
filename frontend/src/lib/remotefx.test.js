import assert from 'node:assert/strict';
import test from 'node:test';

import { adaptFleetToRfxSamples, processRfxHistory } from './remotefx.js';

const t = [1];
function counter({ avg = 0, min = avg, max = avg, p50 = avg } = {}) {
    return { t, avg: [avg], min: [min], max: [max], p50: [p50] };
}

function baseSeries() {
    return {
        rfx_fps_out: counter({ avg: 30, min: 20, max: 40, p50: 30 }),
        rfx_fps_out_p50: counter({ avg: 32, min: 30, max: 35, p50: 32 }),
        rfx_encode_ms: counter({ avg: 60, min: 20, max: 100, p50: 55 }),
        rfx_encode_ms_p50: counter({ avg: 52, min: 40, max: 60, p50: 50 }),
        rfx_quality_pct: counter({ avg: 70, min: 60, max: 80, p50: 70 }),
        rfx_quality_pct_p50: counter({ avg: 75, min: 70, max: 85, p50: 75 }),
        rfx_skip_server_sec: counter({ avg: 5, min: 1, max: 9, p50: 5 }),
        rfx_skip_server_sec_p50: counter({ avg: 3, min: 2, max: 4, p50: 3 }),
        rfx_skip_net_sec: counter({ avg: 4, min: 1, max: 7, p50: 4 }),
        rfx_skip_net_sec_p50: counter({ avg: 2, min: 1, max: 3, p50: 2 }),
        rfx_rtt_ms: counter({ avg: 70, min: 30, max: 120, p50: 65 }),
        rfx_rtt_ms_p50: counter({ avg: 85, min: 70, max: 90, p50: 87 }),
        rfx_loss_pct: counter({ avg: 2, min: 0, max: 5, p50: 2 }),
        rfx_loss_pct_p50: counter({ avg: 1, min: 0, max: 2, p50: 1 }),
    };
}

test('uses service floors, worst-host P95, and median-host P50', () => {
    const [sample] = adaptFleetToRfxSamples(baseSeries());
    assert.equal(sample.fpsOut, 20);
    assert.equal(sample.fpsOutP50, 32);
    assert.equal(sample.quality, 60);
    assert.equal(sample.qualityP50, 75);
    assert.equal(sample.encodeMs, 100);
    assert.equal(sample.encodeMsP50, 50);
    assert.equal(sample.rtt, 120);
    assert.equal(sample.rttP50, 87);
});

test('gaps missing or directionally incompatible P50 values', () => {
    const series = baseSeries();
    series.rfx_encode_ms = counter({ max: 5 });
    series.rfx_encode_ms_p50 = counter({ p50: 87 });
    series.rfx_rtt_ms_p50 = undefined;
    series.rfx_fps_out = counter({ min: 30 });
    series.rfx_fps_out_p50 = counter({ p50: 20 });

    const [sample] = adaptFleetToRfxSamples(series);
    assert.equal(sample.encodeMs, 5);
    assert.equal(sample.encodeMsP50, undefined);
    assert.equal(sample.rttP50, undefined);
    assert.equal(sample.fpsOut, 30);
    assert.equal(sample.fpsOutP50, undefined);
});

test('combined skipped-frame P50 stays a gap unless both components are present', () => {
    const [sample] = adaptFleetToRfxSamples(baseSeries());
    assert.equal(processRfxHistory([sample])[0].skipTotalP50, 5);

    sample.skipNetP50 = undefined;
    assert.equal(processRfxHistory([sample])[0].skipTotalP50, undefined);
});
