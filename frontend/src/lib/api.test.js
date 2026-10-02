import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.$state = (value) => value;
const { ApiError, fetchExportMetrics } = await import('./api.js');

const request = {
    host: null,
    from: '2026-09-30T13:00:00Z',
    to: '2026-09-30T14:00:00Z',
    resolution: 'auto',
    counters: ['cpu_pct'],
    graph: 'overview.load',
};

for (const [format, mime] of [
    ['csv', 'text/csv;charset=utf-8'],
    ['xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
]) {
    test(`fetchExportMetrics returns a ${format} Blob`, async () => {
        globalThis.fetch = async (url) => {
            assert.match(String(url), new RegExp(`format=${format}`));
            return new Response('file bytes', { status: 200, headers: { 'Content-Type': mime } });
        };
        try {
            const blob = await fetchExportMetrics({ ...request, format });
            assert.ok(blob instanceof Blob);
            assert.equal(blob.type, mime);
        } finally {
            globalThis.fetch = originalFetch;
        }
    });
}

test('fetchExportMetrics preserves the API error envelope', async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async () =>
        new Response(JSON.stringify({ error: 'payload_too_large', limit_rows: 50000, rows: 50001, message: 'Narrow the time range.' }), {
            status: 413,
            headers: { 'Content-Type': 'application/json' },
        });
    try {
        await assert.rejects(fetchExportMetrics({ ...request, format: 'csv' }), (error) => {
            assert.ok(error instanceof ApiError);
            assert.equal(error.error, 'payload_too_large');
            assert.equal(error.message, 'Narrow the time range.');
            assert.equal(error.limit_rows, 50000);
            assert.equal(error.rows, 50001);
            return true;
        });
    } finally {
        globalThis.fetch = originalFetch;
    }
});

test('fetchExportMetrics serializes every fleet cohort member as host', async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (url) => {
        const parsed = new URL(url);
        assert.deepEqual(parsed.searchParams.getAll('host'), ['srv-a', 'srv-b']);
        return new Response('file bytes', { status: 200, headers: { 'Content-Type': 'text/csv;charset=utf-8' } });
    };
    try {
        await fetchExportMetrics({ ...request, format: 'csv', cohort: ['srv-a', 'srv-b'] });
    } finally {
        globalThis.fetch = originalFetch;
    }
});
