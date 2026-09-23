// @vitest-environment node
import { describe, it, expect } from 'vitest';
import {
    THRESHOLD_WARN_OPACITY,
    THRESHOLD_CRIT_OPACITY,
    AXIS_STROKE_WIDTH_PX,
    LINE_STROKE_WIDTH_PX,
    CROSSHAIR_STROKE_WIDTH_PX,
    CROSSHAIR_DOT_RADIUS_PX,
    TOOLTIP_BG,
    TOOLTIP_PADDING_PX,
} from './theme.js';

describe('chart theme tokens', () => {
    it('THRESHOLD_WARN_OPACITY is a number in (0, 1)', () => {
        expect(typeof THRESHOLD_WARN_OPACITY).toBe('number');
        expect(THRESHOLD_WARN_OPACITY).toBeGreaterThan(0);
        expect(THRESHOLD_WARN_OPACITY).toBeLessThan(1);
    });

    it('THRESHOLD_CRIT_OPACITY exceeds WARN so crit reads as more serious', () => {
        expect(THRESHOLD_CRIT_OPACITY).toBeGreaterThan(THRESHOLD_WARN_OPACITY);
    });

    it('stroke widths are positive numbers', () => {
        expect(AXIS_STROKE_WIDTH_PX).toBeGreaterThan(0);
        expect(LINE_STROKE_WIDTH_PX).toBeGreaterThan(0);
        expect(CROSSHAIR_STROKE_WIDTH_PX).toBeGreaterThan(0);
    });

    it('CROSSHAIR_DOT_RADIUS_PX is a positive number', () => {
        expect(CROSSHAIR_DOT_RADIUS_PX).toBeGreaterThan(0);
    });

    it('TOOLTIP_BG is a CSS color string', () => {
        expect(typeof TOOLTIP_BG).toBe('string');
        expect(TOOLTIP_BG.length).toBeGreaterThan(0);
    });

    it('TOOLTIP_PADDING_PX is a positive integer', () => {
        expect(Number.isInteger(TOOLTIP_PADDING_PX)).toBe(true);
        expect(TOOLTIP_PADDING_PX).toBeGreaterThanOrEqual(0);
    });
});
