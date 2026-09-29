/**
 * Build a timestamp-to-value map for one fleet counter series.
 * @param {{t:number[],avg:number[],min:number[],max:number[],p50:number[]}|undefined} series
 * @param {'avg'|'min'|'max'|'p50'} field
 */
function tsMap(series, field) {
    const values = new Map();
    if (!series) return values;
    for (let i = 0; i < (series.t ?? []).length; i++) {
        values.set(series.t[i], (series[field] ?? [])[i]);
    }
    return values;
}

/**
 * Return a cohort-compatible median, otherwise a gap.
 * @param {number|undefined} primary
 * @param {number|undefined} median
 * @param {boolean} higherIsBetter
 */
export function orderedP50(primary, median, higherIsBetter = false) {
    if (primary == null || median == null || !Number.isFinite(primary) || !Number.isFinite(median)) {
        return undefined;
    }
    return higherIsBetter ? (median >= primary ? median : undefined) : median <= primary ? median : undefined;
}

/**
 * Adapt fleet counter arrays to RemoteFX chart samples. Service floors use the
 * lowest participating host, conventional P95 uses the highest, and P50 uses
 * the exact median reporting host. Incompatible historical pairs become gaps.
 * @param {Record<string,{t:number[],avg:number[],min:number[],max:number[],p50:number[]}>} series
 * @returns {import('./state.svelte.js').RfxSample[]}
 */
export function adaptFleetToRfxSamples(series) {
    const fps = series.rfx_fps_out;
    if (!fps || fps.t.length === 0) return [];

    const fpsFloor = tsMap(fps, 'min');
    const fpsP50 = tsMap(series.rfx_fps_out_p50, 'p50');
    const encodeP95 = tsMap(series.rfx_encode_ms, 'max');
    const encodeP50 = tsMap(series.rfx_encode_ms_p50, 'p50');
    const qualityFloor = tsMap(series.rfx_quality_pct, 'min');
    const qualityP50 = tsMap(series.rfx_quality_pct_p50, 'p50');
    const skipServerP95 = tsMap(series.rfx_skip_server_sec, 'max');
    const skipServerP50 = tsMap(series.rfx_skip_server_sec_p50, 'p50');
    const skipNetworkP95 = tsMap(series.rfx_skip_net_sec, 'max');
    const skipNetworkP50 = tsMap(series.rfx_skip_net_sec_p50, 'p50');
    const rttP95 = tsMap(series.rfx_rtt_ms, 'max');
    const rttP50 = tsMap(series.rfx_rtt_ms_p50, 'p50');
    const lossP95 = tsMap(series.rfx_loss_pct, 'max');
    const lossP50 = tsMap(series.rfx_loss_pct_p50, 'p50');

    return fps.t.map((ts) => {
        const fpsOut = fpsFloor.get(ts);
        const fpsMedian = fpsP50.get(ts);
        const encodeMs = encodeP95.get(ts);
        const quality = qualityFloor.get(ts);
        const qualityMedian = qualityP50.get(ts);
        const skipServer = skipServerP95.get(ts);
        const skipNetwork = skipNetworkP95.get(ts);
        const rtt = rttP95.get(ts);
        const loss = lossP95.get(ts);
        return {
            ts,
            fpsOut,
            fpsOutP50:
                fpsOut != null && fpsMedian != null && fpsMedian > 0 ? orderedP50(fpsOut, fpsMedian, true) : undefined,
            encodeMs,
            encodeMsP50: orderedP50(encodeMs, encodeP50.get(ts)),
            quality,
            qualityP50:
                quality != null && qualityMedian != null && qualityMedian > 0
                    ? orderedP50(quality, qualityMedian, true)
                    : undefined,
            skipServer,
            skipServerP50: orderedP50(skipServer, skipServerP50.get(ts)),
            skipNet: skipNetwork,
            skipNetP50: orderedP50(skipNetwork, skipNetworkP50.get(ts)),
            rtt,
            rttP50: orderedP50(rtt, rttP50.get(ts)),
            loss,
            lossP50: orderedP50(loss, lossP50.get(ts)),
        };
    });
}

/**
 * Combine server and network skips without converting a missing component to zero.
 * @param {import('./state.svelte.js').RfxSample[]} history
 */
export function processRfxHistory(history) {
    return history.map((sample) => ({
        ...sample,
        skipTotal: (sample.skipServer ?? 0) + (sample.skipNet ?? 0),
        skipTotalP50:
            sample.skipServerP50 != null && sample.skipNetP50 != null
                ? sample.skipServerP50 + sample.skipNetP50
                : undefined,
    }));
}
