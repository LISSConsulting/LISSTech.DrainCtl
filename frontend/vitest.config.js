import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

/**
 * Vitest config for the drainctl dashboard.
 *
 * Two test populations:
 *  - Pure-function unit tests in frontend/src/lib/chart/*.test.js run in the
 *    node environment (the default for files matching that path). They cover
 *    data adapters and zoom math; they do not mount Svelte and need no DOM.
 *  - Component sanity tests in *.test.svelte files use jsdom +
 *    @testing-library/svelte. Currently none committed; placeholder.
 *
 * Charts are not SSR'd (embedded dashboard only), so Svelte's compile step
 * is wired via the same plugin the production build uses.
 *
 * Note: vitest 5 dropped `environmentMatchGlobs` in favor of per-file
 * `// @vitest-environment node` doc comments or per-file imports. Pure chart
 * unit tests use the doc-comment form so the Svelte plugin is irrelevant
 * to their resolution.
 */
export default defineConfig({
    plugins: [svelte()],
    test: {
        environment: 'jsdom',
        include: ['src/**/*.test.{js,svelte}'],
        coverage: {
            provider: 'v8',
            reporter: ['text', 'lcov'],
            include: ['src/lib/chart/**/*.js'],
        },
    },
});
