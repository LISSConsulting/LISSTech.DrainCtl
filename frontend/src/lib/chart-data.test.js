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

test('Session charts expose pooled workload, breadth, and coverage without using host-summary counters', () => {
    const [sample] = adaptFleetToSessionSamples(
        {
            sessions_total: counter({ avg: 30 }),
            sessions_active: counter({ avg: 24 }),
            sessions_disconnected: counter({ avg: 6 }),
            sessions_max: counter({ avg: 40 }),
            session_cpu_p95_pct: counter({ max: 99, p50: 88 }),
        },
        [
            {
                t: 1,
                cpu: {
                    p95_pct: 55,
                    avg_pct: 7.5,
                    observed_sessions: 24,
                    ge_5_avg: 5.2,
                    ge_5_max: 8,
                    ge_5_rate_pct: 21.7,
                    ge_20_avg: 2,
                    ge_20_max: 3,
                    ge_20_rate_pct: 8.3,
                },
                memory: { p95_bytes: 900, avg_bytes: 500, observed_sessions: 22 },
                coverage: { expected_hosts: 4, contributing_hosts: 3, partial: true },
            },
        ],
    );
    assert.equal(sample.total, 30);
    assert.equal(sample.utilization, 75);
    assert.equal(sample.sessionCpuP95, 55);
    assert.equal(sample.sessionCpuAvg, 7.5);
    assert.equal(sample.sessionMemP95, 900);
    assert.equal(sample.sessionMemAvg, 500);
    assert.equal(sample.sessionCpuAtOrAbove5, 5.2);
    assert.equal(sample.sessionCpuAtOrAbove5Max, 8);
    assert.equal(sample.sessionCpuAtOrAbove5Rate, 21.7);
    assert.equal(sample.sessionCoverage.contributing_hosts, 3);
});

test('Session charts leave unavailable anonymous workload metrics as gaps', () => {
    const [sample] = adaptFleetToSessionSamples({
        sessions_total: counter({ avg: 30 }),
        sessions_active: counter({ avg: 24 }),
    });
    assert.equal(sample.sessionCpuP95, undefined);
    assert.equal(sample.sessionCpuAvg, undefined);
    assert.equal(sample.sessionMemP95, undefined);
    assert.equal(sample.sessionMemAvg, undefined);
    assert.equal(sample.sessionCpuObserved, undefined);
    assert.equal(sample.sessionCpuAtOrAbove5, undefined);
    assert.equal(sample.sessionCpuAtOrAbove20, undefined);
});
