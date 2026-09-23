// @vitest-environment node
import { describe, it, expect } from 'vitest';
import { PER_HOST_PRESETS, findPreset } from './presets.js';

describe('PER_HOST_PRESETS', () => {
    it('contains 15M, 1H, 1D, 3D, 5D in ascending ms order', () => {
        expect(PER_HOST_PRESETS.map((p) => p.label)).toEqual(['15M', '1H', '1D', '3D', '5D']);
        for (let i = 1; i < PER_HOST_PRESETS.length; i++) {
            expect(PER_HOST_PRESETS[i].ms).toBeGreaterThan(PER_HOST_PRESETS[i - 1].ms);
        }
    });

    it('matches the millisecond values expected by callers', () => {
        expect(PER_HOST_PRESETS[0].ms).toBe(15 * 60 * 1000);
        expect(PER_HOST_PRESETS[1].ms).toBe(60 * 60 * 1000);
        expect(PER_HOST_PRESETS[2].ms).toBe(24 * 60 * 60 * 1000);
        expect(PER_HOST_PRESETS[3].ms).toBe(3 * 24 * 60 * 60 * 1000);
        expect(PER_HOST_PRESETS[4].ms).toBe(5 * 24 * 60 * 60 * 1000);
    });
});

describe('findPreset', () => {
    it('returns the matching preset when ms is exact', () => {
        const p = findPreset(PER_HOST_PRESETS, 60 * 60 * 1000);
        expect(p).not.toBeNull();
        expect(p.label).toBe('1H');
    });

    it('returns null when ms is between presets', () => {
        expect(findPreset(PER_HOST_PRESETS, 2 * 60 * 60 * 1000)).toBeNull();
    });

    it('returns null for negative or zero spans', () => {
        expect(findPreset(PER_HOST_PRESETS, 0)).toBeNull();
        expect(findPreset(PER_HOST_PRESETS, -1)).toBeNull();
    });

    it('returns null for unknown spans even if numerically close', () => {
        expect(findPreset(PER_HOST_PRESETS, 60 * 60 * 1000 + 1)).toBeNull();
    });
});
