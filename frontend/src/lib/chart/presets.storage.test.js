import { describe, it, expect, beforeEach } from 'vitest';
import {
    PER_HOST_ZOOM_PRESET_STORAGE_KEY,
    readStoredZoomPresetMs,
    writeStoredZoomPresetMs,
} from './presets.js';

describe('zoom preset localStorage round-trip (jsdom)', () => {
    beforeEach(() => {
        localStorage.clear();
    });

    it('readStoredZoomPresetMs returns null when nothing is persisted', () => {
        expect(readStoredZoomPresetMs()).toBeNull();
    });

    it('round-trips a valid preset ms', () => {
        writeStoredZoomPresetMs(60 * 60 * 1000);
        expect(readStoredZoomPresetMs()).toBe(60 * 60 * 1000);
    });

    it('ignores non-numeric, zero, and negative stored values', () => {
        // @ts-ignore — force a malformed value
        localStorage.setItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY, 'abc');
        expect(readStoredZoomPresetMs()).toBeNull();
        localStorage.setItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY, '0');
        expect(readStoredZoomPresetMs()).toBeNull();
        localStorage.setItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY, '-1');
        expect(readStoredZoomPresetMs()).toBeNull();
        localStorage.setItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY, 'NaN');
        expect(readStoredZoomPresetMs()).toBeNull();
    });

    it('uses the documented storage key', () => {
        writeStoredZoomPresetMs(24 * 60 * 60 * 1000);
        expect(localStorage.getItem(PER_HOST_ZOOM_PRESET_STORAGE_KEY)).toBe(String(24 * 60 * 60 * 1000));
    });
});
