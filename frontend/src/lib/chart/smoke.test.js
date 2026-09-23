// @vitest-environment node
import { describe, it, expect } from 'vitest';

/**
 * Smoke test: confirms vitest is wired correctly and the pure-function test
 * path runs in node. Real chart adapter and zoom tests land in commits 2-3
 * of the chart-library-migration PR; this file's only job is to fail the
 * build if the toolchain regresses.
 */
describe('chart module smoke', () => {
    it('runs in node', () => {
        expect(typeof window).toBe('undefined');
    });

    it('arithmetic still works', () => {
        expect(2 + 2).toBe(4);
    });
});
