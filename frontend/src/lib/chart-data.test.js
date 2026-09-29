import assert from 'node:assert/strict';
import test from 'node:test';

import { adaptFleetToMetricsSamples, adaptFleetToSessionSamples } from './chart-data.js';

const t = [1];
function counter({ avg = 0, min = avg, max = avg, p50 = avg } = {}) {
    return { t, avg: [avg], min: [min], max: [max], p50: [p50] };
}

test('LOAD uses CPU average and retained CPU P95 tail, not max CPU average', () => {
    const [sample] = adaptFleetToMetricsSamples({
        cpu_pct: counter({ avg: 40, max: 70 }),
        cpu_p95_pct: counter({ avg: 65, max: 92 }),
        mem_used_pct: counter({ avg: 75 }),
        sessions_total: counter({ avg: 30 }),
    });
    assert.equal(sample.cpu, 40);
    assert.equal(sample.cpuP95, 92);
    assert.equal(sample.mem, 75);
    assert.equal(sample.sessions, 30);
});

test('Health Indicators expose accurate fleet-average, worst-host, and median-host fields', () => {
    const [sample] = adaptFleetToMetricsSamples({
        cpu_pct: counter({ avg: 10 }),
        input_delay_p95_ms: counter({ avg: 50, max: 140, p50: 70 }),
        input_delay_p50_ms: counter({ avg: 30, p50: 25 }),
        pages_sec: counter({ avg: 80, p50: 45 }),
        tcp_retrans_sec: counter({ avg: 9, p50: 4 }),
        disk_queue: counter({ avg: 2.5, p50: 1.2 }),
    });
    assert.equal(sample.inputDelay, 140);
    assert.equal(sample.p50InputDelay, 25);
    assert.equal(sample.pagesPerSec, 80);
    assert.equal(sample.p50PagesPerSec, 45);
    assert.equal(sample.tcpRetrans, 9);
    assert.equal(sample.p50TcpRetrans, 4);
    assert.equal(sample.diskQueue, 2.5);
    assert.equal(sample.p50DiskQueue, 1.2);
});

test('Session charts use peak and typical host P95 with CPU activity counts', () => {
    const [sample] = adaptFleetToSessionSamples({
        sessions_total: counter({ avg: 30 }),
        sessions_active: counter({ avg: 24 }),
        sessions_disconnected: counter({ avg: 6 }),
        sessions_max: counter({ avg: 40 }),
        session_cpu_p95_pct: counter({ avg: 20, max: 55, p50: 18 }),
        session_mem_p95_bytes: counter({ avg: 500, max: 900, p50: 600 }),
        session_cpu_observed_count: counter({ avg: 24 }),
        session_cpu_ge_5_count: counter({ avg: 5 }),
        session_cpu_ge_20_count: counter({ avg: 2 }),
    });
    assert.equal(sample.total, 30);
    assert.equal(sample.active, 24);
    assert.equal(sample.disconnected, 6);
    assert.equal(sample.utilization, 75);
    assert.equal(sample.sessionCpuPeakP95, 55);
    assert.equal(sample.sessionCpuTypicalP95, 18);
    assert.equal(sample.sessionMemPeakP95, 900);
    assert.equal(sample.sessionMemTypicalP95, 600);
    assert.equal(sample.sessionCpuObserved, 24);
    assert.equal(sample.sessionCpuAtOrAbove5, 5);
    assert.equal(sample.sessionCpuAtOrAbove20, 2);
});

test('Session charts leave unavailable operational metrics as gaps', () => {
    const [sample] = adaptFleetToSessionSamples({
        sessions_total: counter({ avg: 30 }),
        sessions_active: counter({ avg: 24 }),
    });
    assert.equal(sample.sessionCpuPeakP95, undefined);
    assert.equal(sample.sessionCpuTypicalP95, undefined);
    assert.equal(sample.sessionMemPeakP95, undefined);
    assert.equal(sample.sessionMemTypicalP95, undefined);
    assert.equal(sample.sessionCpuObserved, undefined);
    assert.equal(sample.sessionCpuAtOrAbove5, undefined);
    assert.equal(sample.sessionCpuAtOrAbove20, undefined);
});
