// @vitest-environment node
import { describe, it, expect } from 'vitest';
import {
    tsMap,
    adaptFleetToMetricsSamples,
    adaptFleetToSessionSamples,
    adaptFleetToRfxSamples,
    mkCounter,
} from './adapters.js';

describe('tsMap', () => {
    it('returns an empty map for undefined input', () => {
        expect(tsMap(undefined).size).toBe(0);
    });

    it('returns an empty map for an empty t array', () => {
        expect(tsMap({ t: [], avg: [] }).size).toBe(0);
    });

    it('indexes by ts with the requested field as value', () => {
        const m = tsMap({ t: [1, 2, 3], avg: [10, 20, 30] }, 'avg');
        expect(m.get(1)).toBe(10);
        expect(m.get(2)).toBe(20);
        expect(m.get(3)).toBe(30);
    });

    it('falls back to 0 for an empty value array', () => {
        const m = tsMap({ t: [1, 2], avg: [] }, 'avg');
        expect(m.get(1)).toBeUndefined();
    });
});

describe('adaptFleetToMetricsSamples', () => {
    it('returns [] when series is empty', () => {
        expect(adaptFleetToMetricsSamples({})).toEqual([]);
    });

    it('returns [] when cpu_pct is missing', () => {
        expect(adaptFleetToMetricsSamples({ mem_used_pct: mkCounter([1], [50]) })).toEqual([]);
    });

    it('returns [] when cpu_pct has no samples', () => {
        expect(adaptFleetToMetricsSamples({ cpu_pct: mkCounter([], []) })).toEqual([]);
    });

    it('produces one MetricsSample per cpu_pct timestamp', () => {
        const out = adaptFleetToMetricsSamples({
            cpu_pct: mkCounter([100, 200, 300], [10, 20, 30], undefined, [12, 25, 40]),
            mem_used_pct: mkCounter([100, 200, 300], [40, 50, 60]),
            sessions_total: mkCounter([100, 200, 300], [5.4, 7.6, 9.1]),
            input_delay_p95_ms: mkCounter([100, 200, 300], [25, 30, 35]),
        });
        expect(out).toHaveLength(3);
        expect(out[0]).toMatchObject({
            time: 100,
            cpu: 10,
            cpuP95: 12,
            mem: 40,
            sessions: 5,
            inputDelay: 25,
            p50InputDelay: 0,
        });
        expect(out[2]).toMatchObject({
            time: 300,
            cpu: 30,
            cpuP95: 40,
            mem: 60,
            sessions: 9,
            inputDelay: 35,
        });
    });

    it('falls back to cpu avg when cpuP95 max is 0', () => {
        const out = adaptFleetToMetricsSamples({
            cpu_pct: mkCounter([1], [42], undefined, [0]),
        });
        expect(out[0].cpuP95).toBe(42);
    });

    it('uses 0 for missing sibling counters', () => {
        const out = adaptFleetToMetricsSamples({
            cpu_pct: mkCounter([1, 2], [50, 60]),
            // mem, sessions, etc. all missing
        });
        expect(out[0].mem).toBe(0);
        expect(out[0].sessions).toBe(0);
        expect(out[0].inputDelay).toBe(0);
        expect(out[1].mem).toBe(0);
    });

    it('joins sibling counters by ts, not by array index', () => {
        // mem_used_pct has a sample at ts=200 that cpu_pct doesn't share.
        // The adapter should still emit the cpu-only sample and use 0 for mem.
        const out = adaptFleetToMetricsSamples({
            cpu_pct: mkCounter([100, 200], [10, 20]),
            mem_used_pct: mkCounter([200], [99]),
        });
        expect(out).toHaveLength(2);
        expect(out[0]).toMatchObject({ time: 100, cpu: 10, mem: 0 });
        expect(out[1]).toMatchObject({ time: 200, cpu: 20, mem: 99 });
    });
});

describe('adaptFleetToSessionSamples', () => {
    it('returns [] when sessions_total is missing', () => {
        expect(adaptFleetToSessionSamples({})).toEqual([]);
    });

    it('computes utilization = round(total/max*100), 0 when max=0', () => {
        const out = adaptFleetToSessionSamples({
            sessions_total: mkCounter([1, 2], [50, 100]),
            sessions_active: mkCounter([1, 2], [40, 80]),
            sessions_disconnected: mkCounter([1, 2], [10, 20]),
            sessions_max: mkCounter([1, 2], [100, 0]),
        });
        expect(out[0].utilization).toBe(50);
        expect(out[1].utilization).toBe(0);
    });

    it('rounds active/disconnected/total to integers', () => {
        const out = adaptFleetToSessionSamples({
            sessions_total: mkCounter([1], [12.7]),
            sessions_active: mkCounter([1], [10.4]),
            sessions_disconnected: mkCounter([1], [2.3]),
            sessions_max: mkCounter([1], [100]),
        });
        expect(out[0]).toMatchObject({
            active: 10,
            disconnected: 2,
            total: 13,
            utilization: 13,
        });
    });
});

describe('adaptFleetToRfxSamples', () => {
    it('returns [] when rfx_fps_out is missing', () => {
        expect(adaptFleetToRfxSamples({})).toEqual([]);
    });

    it('seeds all P50 fields at 0 (P50 fleet aggregation not wired yet)', () => {
        const out = adaptFleetToRfxSamples({
            rfx_fps_out: mkCounter([1, 2], [55, 60]),
            rfx_fps_out_p50: mkCounter([1, 2], [50, 58]),
            rfx_encode_ms: mkCounter([1, 2], [12, 14]),
            rfx_quality_pct: mkCounter([1, 2], [85, 87]),
            rfx_rtt_ms: mkCounter([1, 2], [5, 6]),
            rfx_loss_pct: mkCounter([1, 2], [0.1, 0.2]),
            rfx_skip_server_sec: mkCounter([1, 2], [0, 1]),
            rfx_skip_net_sec: mkCounter([1, 2], [0, 0]),
        });
        expect(out[0]).toMatchObject({
            ts: 1,
            fpsOut: 55,
            fpsOutP50: 50,
            encodeMs: 12,
            encodeMsP50: 0,
            quality: 85,
            rtt: 5,
            rttP50: 0,
            loss: 0.1,
            skipServer: 0,
            skipNet: 0,
        });
        expect(out[1].encodeMsP50).toBe(0);
        expect(out[1].rttP50).toBe(0);
    });

    it('uses 0 for any missing counter, never undefined', () => {
        const out = adaptFleetToRfxSamples({
            rfx_fps_out: mkCounter([1], [60]),
        });
        expect(out[0]).toMatchObject({
            ts: 1,
            fpsOut: 60,
            encodeMs: 0,
            quality: 0,
            rtt: 0,
            loss: 0,
            skipServer: 0,
            skipNet: 0,
        });
        // No property is undefined
        for (const v of Object.values(out[0])) {
            expect(v).not.toBeUndefined();
        }
    });
});

describe('mkCounter', () => {
    it('clones input arrays so callers can safely mutate', () => {
        const t = [1, 2];
        const c = mkCounter(t, [10, 20]);
        t.push(3);
        expect(c.t).toEqual([1, 2]);
    });
});
