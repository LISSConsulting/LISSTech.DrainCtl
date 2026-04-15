<script>
    import RingGauge from './RingGauge.svelte';
    import Sparkline from './Sparkline.svelte';
    import { DEFAULTS, resolveThresholds } from '../lib/thresholds.js';
    import { appState } from '../lib/state.svelte.js';
    import { rel, dur, modeLabel, formatTs } from '../lib/utils.js';

    let { server, onhistoryclick = undefined, onremove = undefined } = $props();

    let perf = $derived(server.perf || {});
    let memPct = $derived(perf.mem_total_mb > 0 ? (1 - perf.mem_avail_mb / perf.mem_total_mb) * 100 : 0);

    // Sessions ring
    let sessionWarnThresh = $derived(appState.config?.session_warning_threshold ?? 80);
    let sessionCritThresh = $derived(Math.min(sessionWarnThresh + 15, 100));
    let sessionsPct = $derived(
        server.max_sessions > 0 ? Math.min((server.sessions / server.max_sessions) * 100, 100) : null,
    );

    // Per-server ring buffer; fall back to fleet aggregate
    let serverHistory = $derived(appState.serverMetrics.get(server.host) ?? appState.metricsHistory);
    let cpuHistory = $derived(serverHistory.map((h) => h.cpu ?? 0));
    let memHistory = $derived(serverHistory.map((h) => h.mem ?? 0));
    let sessionsHistory = $derived(serverHistory.map((h) => h.sessions ?? 0));
    let delayHistory = $derived(serverHistory.map((h) => h.inputDelay ?? 0));
    let diskQueueHistory = $derived(serverHistory.map((h) => h.diskQueue ?? 0));
    let tcpRetransHistory = $derived(serverHistory.map((h) => h.tcpRetrans ?? 0));

    // Time label for sparklines: age of the oldest sample in the ring buffer.
    let sparkTimeLabel = $derived.by(() => {
        const oldest = serverHistory[0]?.time;
        if (!oldest) return '';
        const ms = Date.now() - oldest;
        const s = Math.floor(ms / 1000);
        if (s < 60) return s + 's ago';
        const m = Math.floor(s / 60);
        if (m < 60) return m + 'm ago';
        const h = Math.floor(m / 60);
        const rem = m % 60;
        return h + 'h' + (rem > 0 ? ' ' + rem + 'm' : '') + ' ago';
    });

    // Config-aware thresholds
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuThresh = $derived(resolveThresholds('cpu', perfCfg));
    let memThresh = $derived(resolveThresholds('mem', perfCfg));
    let delayThresh = $derived(resolveThresholds('inputDelay', perfCfg));

    // Grace period in seconds (from per-server field, fall back to global config)
    let gracePeriodSec = $derived(
        (server.grace_period_seconds ?? 0) > 0
            ? server.grace_period_seconds
            : (appState.config?.grace_period ?? 0) > 0
              ? appState.config.grace_period * 60
              : null,
    );

    // Grace left (when in grace status) / grace exceeded (when in alert)
    let graceLeft = $derived(
        server.status === 'grace' && server.grace_deadline
            ? Math.max(0, Math.round((new Date(server.grace_deadline).getTime() - Date.now()) / 1000))
            : null,
    );
    let graceExceeded = $derived(
        server.status === 'alert' && gracePeriodSec != null && server.state_duration_seconds != null
            ? Math.max(0, server.state_duration_seconds - gracePeriodSec)
            : null,
    );

    // Last seen staleness color
    function lastSeenColor(iso) {
        if (!iso) return 'var(--color-subtle)';
        const secAgo = (Date.now() - new Date(iso).getTime()) / 1000;
        if (secAgo < 120) return 'var(--color-green)';
        if (secAgo < 300) return 'var(--color-amber)';
        return 'var(--color-red)';
    }
    let lsColor = $derived(lastSeenColor(server.last_seen));

    // State Since formatting — use formatTs for locale-aware local time display
    let stateSinceStr = $derived(server.state_changed_at ? formatTs(server.state_changed_at) : '—');

    let isOff = $derived(server.status === 'off');
</script>

<div class="d-inner">
    <!-- Tile 1: Resource Utilization -->
    <div class="d-tile d-tile-util">
        <div class="d-tile-label">Resource Utilization</div>

        {#if !isOff}
            <!-- Ring row -->
            <div class="d-ring-row">
                <div class="d-ring-cell">
                    <RingGauge
                        value={sessionsPct}
                        max={100}
                        label="Sessions"
                        unit=" "
                        warnThreshold={sessionWarnThresh}
                        critThreshold={sessionCritThresh}
                        centerLabel={String(server.sessions)}
                        size={72}
                    />
                </div>
                <div class="d-ring-cell">
                    <RingGauge
                        value={perf.cpu_pct}
                        label="CPU"
                        unit="%"
                        warnThreshold={cpuThresh.warn}
                        critThreshold={cpuThresh.crit}
                        size={72}
                    />
                </div>
                <div class="d-ring-cell">
                    <RingGauge
                        value={memPct}
                        label="Memory"
                        unit="%"
                        warnThreshold={memThresh.warn}
                        critThreshold={memThresh.crit}
                        size={72}
                    />
                </div>
            </div>

            <!-- Sparkline row — order matches rings: Sessions, CPU, Memory -->
            <div class="d-spark-row">
                <div class="d-spark-cell">
                    <Sparkline data={sessionsHistory} color="var(--color-amber)" height={28} />
                    <div class="d-spark-labels">
                        <span>Sess%</span><span>{sessionsPct != null ? sessionsPct.toFixed(0) + '%' : '—'}</span>
                    </div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
                <div class="d-spark-cell">
                    <Sparkline data={cpuHistory} color="var(--color-accent)" height={28} />
                    <div class="d-spark-labels"><span>CPU</span><span>{perf.cpu_pct?.toFixed(1) ?? '—'}%</span></div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
                <div class="d-spark-cell">
                    <Sparkline data={memHistory} color="var(--color-green)" height={28} />
                    <div class="d-spark-labels"><span>Mem</span><span>{memPct.toFixed(1)}%</span></div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
            </div>

            <!-- Legend -->
            <div class="d-legend">
                <div class="d-leg-col">
                    <div class="d-leg-head">Sessions</div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Active</span><span class="d-leg-v">{server.sessions_active ?? '—'}</span>
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Disconn.</span><span class="d-leg-v"
                            >{server.sessions_disconnected ?? '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Total/Max</span><span class="d-leg-v">
                            {#if server.max_sessions > 0}
                                {server.sessions}/{server.max_sessions}
                            {:else}
                                {server.sessions ?? '—'}
                            {/if}
                        </span>
                    </div>
                </div>
                <div class="d-leg-col">
                    <div class="d-leg-head">CPU</div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Host</span><span class="d-leg-v"
                            >{perf.cpu_pct != null ? perf.cpu_pct.toFixed(0) + '%' : '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k has-tip"
                            >P95<span class="tip"
                                >95th percentile across all sessions — 95% of sessions use less than this value.</span
                            ></span
                        ><span class="d-leg-v"
                            >{perf.session_cpu_p95_pct != null && perf.session_cpu_p95_pct > 0
                                ? perf.session_cpu_p95_pct.toFixed(0) + '%'
                                : '—'}</span
                        >
                    </div>
                </div>
                <div class="d-leg-col">
                    <div class="d-leg-head">Memory</div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Used</span><span class="d-leg-v"
                            >{perf.mem_total_mb > 0
                                ? ((perf.mem_total_mb - perf.mem_avail_mb) / 1024).toFixed(1) + ' GB'
                                : '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Total</span><span class="d-leg-v"
                            >{perf.mem_total_mb ? (perf.mem_total_mb / 1024).toFixed(1) + ' GB' : '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k has-tip"
                            >P95<span class="tip">95th percentile per-session memory usage.</span></span
                        ><span class="d-leg-v"
                            >{perf.session_mem_p95_bytes != null && perf.session_mem_p95_bytes > 0
                                ? (perf.session_mem_p95_bytes / (1024 * 1024)).toFixed(0) + ' MB'
                                : '—'}</span
                        >
                    </div>
                </div>
            </div>
        {:else}
            <div class="d-offline">Server offline — no perf data</div>
        {/if}
    </div>

    <!-- Tile 2: I/O Metrics -->
    <div class="d-tile d-tile-io">
        <div class="d-tile-label">I/O &amp; Network</div>

        {#if !isOff}
            <!-- Ring row -->
            <div class="d-ring-row">
                <div class="d-ring-cell">
                    <RingGauge
                        value={perf.disk_queue}
                        max={DEFAULTS.diskQueue.crit * 2}
                        label="Disk Q"
                        unit=" "
                        warnThreshold={DEFAULTS.diskQueue.warn}
                        critThreshold={DEFAULTS.diskQueue.crit}
                        size={72}
                    />
                </div>
                <div class="d-ring-cell">
                    <RingGauge
                        value={perf.input_delay_p95_ms}
                        max={delayThresh.crit * 2}
                        label="Inp Dly"
                        unit="ms"
                        warnThreshold={delayThresh.warn}
                        critThreshold={delayThresh.crit}
                        size={72}
                    />
                </div>
                <div class="d-ring-cell">
                    <RingGauge
                        value={perf.tcp_retrans_sec}
                        max={DEFAULTS.tcpRetransmits.crit * 2}
                        label="TCP Rx"
                        unit="/s"
                        warnThreshold={DEFAULTS.tcpRetransmits.warn}
                        critThreshold={DEFAULTS.tcpRetransmits.crit}
                        size={72}
                    />
                </div>
            </div>

            <!-- Sparkline row — order matches rings: Disk Q, Input Delay, TCP Rx -->
            <div class="d-spark-row">
                <div class="d-spark-cell">
                    <Sparkline data={diskQueueHistory} color="var(--color-amber)" height={28} />
                    <div class="d-spark-labels">
                        <span>Disk Q</span><span>{perf.disk_queue?.toFixed(2) ?? '—'}</span>
                    </div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
                <div class="d-spark-cell">
                    <Sparkline data={delayHistory} color="var(--color-amber)" height={28} />
                    <div class="d-spark-labels">
                        <span>Inp Dly</span><span>{perf.input_delay_p95_ms?.toFixed(1) ?? '—'}ms</span>
                    </div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
                <div class="d-spark-cell">
                    <Sparkline data={tcpRetransHistory} color="var(--color-amber)" height={28} />
                    <div class="d-spark-labels">
                        <span>TCP Rx</span><span>{perf.tcp_retrans_sec?.toFixed(1) ?? '—'}/s</span>
                    </div>
                    <div class="d-spark-time"><span>{sparkTimeLabel}</span><span>now</span></div>
                </div>
            </div>

            <!-- Legend -->
            <div class="d-legend">
                <div class="d-leg-col">
                    <div class="d-leg-head">Input Delay</div>
                    <div class="d-leg-row">
                        <span class="d-leg-k has-tip"
                            >P50<span class="tip"
                                >Median — half of all measurements are below this. Represents the typical user
                                experience.</span
                            ></span
                        ><span class="d-leg-v"
                            >{perf.input_delay_p50_ms != null
                                ? perf.input_delay_p50_ms.toFixed(0) + ' ms'
                                : '— ms'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k has-tip"
                            >P95<span class="tip"
                                >95th percentile — 95% of measurements below this. Shows worst experience most users
                                face.</span
                            ></span
                        ><span class="d-leg-v"
                            >{perf.input_delay_p95_ms != null
                                ? perf.input_delay_p95_ms.toFixed(0) + ' ms'
                                : '— ms'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Max</span><span class="d-leg-v"
                            >{perf.input_delay_max_ms != null
                                ? perf.input_delay_max_ms.toFixed(0) + ' ms'
                                : '— ms'}</span
                        >
                    </div>
                </div>
                <div class="d-leg-col">
                    <div class="d-leg-head">Disk &amp; Net</div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Pages/sec</span><span class="d-leg-v"
                            >{perf.pages_sec != null ? perf.pages_sec.toFixed(1) : '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">Disk Queue</span><span class="d-leg-v"
                            >{perf.disk_queue != null ? perf.disk_queue.toFixed(2) : '—'}</span
                        >
                    </div>
                    <div class="d-leg-row">
                        <span class="d-leg-k">TCP Retrans</span><span class="d-leg-v"
                            >{perf.tcp_retrans_sec != null ? perf.tcp_retrans_sec.toFixed(1) + '/s' : '—'}</span
                        >
                    </div>
                </div>
            </div>
        {:else}
            <div class="d-offline">Server offline — no I/O data</div>
        {/if}
    </div>

    <!-- Tile 3: Server Details -->
    <div class="d-tile d-tile-details">
        <div class="d-tile-label">Server Details</div>

        {#if !isOff}
            <div class="d-kv-row">
                <span class="d-kv-k">Drain Mode</span><span class="d-kv-v">{modeLabel(server.drain_mode)}</span>
            </div>
            <div class="d-kv-row">
                <span class="d-kv-k">State Since</span><span class="d-kv-v">{stateSinceStr}</span>
            </div>
            <div class="d-kv-row">
                <span class="d-kv-k">Duration</span><span class="d-kv-v">{dur(server.state_duration_seconds)}</span>
            </div>
            {#if server.drain_mode && server.drain_mode !== 'ALLOW_ALL_CONNECTIONS' && gracePeriodSec != null}
                <div class="d-kv-row">
                    <span class="d-kv-k">Grace Period</span><span class="d-kv-v"
                        >{Math.round(gracePeriodSec / 60)}m</span
                    >
                </div>
                {#if graceLeft != null && graceLeft > 0}
                    <div class="d-kv-row">
                        <span class="d-kv-k">Grace Left</span><span class="d-kv-v" style="color:var(--color-amber)"
                            >{dur(graceLeft)}</span
                        >
                    </div>
                {/if}
                {#if graceExceeded != null && graceExceeded > 0}
                    <div class="d-kv-row">
                        <span class="d-kv-k">Grace Exceeded</span><span class="d-kv-v" style="color:var(--color-red)"
                            >{dur(graceExceeded)} ago</span
                        >
                    </div>
                {/if}
            {/if}
            <div class="d-kv-row">
                <span class="d-kv-k">Changed By</span><span class="d-kv-v">{server.changed_by || '—'}</span>
            </div>
        {/if}

        <div class="d-kv-row">
            <span class="d-kv-k">Last Seen</span><span class="d-kv-v" style="color:{lsColor}"
                >{rel(server.last_seen)}</span
            >
        </div>
        <div class="d-kv-row">
            <span class="d-kv-k">Version</span><span class="d-kv-v">{server.version || '—'}</span>
        </div>
        <div class="d-kv-row">
            <span class="d-kv-k">Registered</span><span class="d-kv-v">{rel(server.registered_at)}</span>
        </div>

        <!-- Action buttons -->
        <div class="d-actions">
            <button
                class="btn-brutal d-btn-hist"
                onclick={() => {
                    appState.eventHostFilter = server.host.split('.')[0];
                    appState.currentView = 'events';
                }}>History</button
            >
            {#if onremove}
                <button class="btn-brutal d-btn-rm" onclick={() => onremove(server.host)}>Remove</button>
            {/if}
        </div>
    </div>
</div>

<style>
    .d-inner {
        padding: 12px 16px;
        background: var(--color-bg);
        display: flex;
        gap: 12px;
        align-items: stretch;
        font-family: 'JetBrains Mono', monospace;
        font-size: 12px;
        flex-wrap: wrap;
    }

    /* Neobrutalist tiles */
    .d-tile {
        background: var(--color-card);
        border: 2.5px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 4px 4px 0 var(--color-shadow);
        padding: 10px 12px;
        display: flex;
        flex-direction: column;
    }

    .d-tile-label {
        font-size: 11px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.6px;
        color: var(--color-accent);
        margin-bottom: 8px;
    }

    .d-tile-util {
        flex: 1.5;
        min-width: 280px;
    }
    .d-tile-io {
        flex: 1.3;
        min-width: 260px;
    }
    .d-tile-details {
        flex: 1;
        min-width: 180px;
    }

    /* Ring row */
    .d-ring-row {
        display: flex;
        gap: 8px;
        margin-bottom: 8px;
        justify-content: space-around;
    }
    .d-ring-cell {
        flex: 1;
        display: flex;
        justify-content: center;
        border: none !important;
        outline: none !important;
        box-shadow: none !important;
    }

    /* Sparkline row */
    .d-spark-row {
        display: flex;
        gap: 8px;
        margin-bottom: 6px;
    }
    .d-spark-cell {
        flex: 1;
    }
    .d-spark-labels {
        display: flex;
        justify-content: space-between;
        font-size: 9px;
        color: var(--color-subtle);
        margin-top: 2px;
    }
    .d-spark-time {
        display: flex;
        justify-content: space-between;
        font-size: 8px;
        color: var(--color-subtle);
        opacity: 0.7;
        margin-top: 1px;
    }

    /* Legend */
    .d-legend {
        display: flex;
        gap: 8px;
        margin-top: 6px;
        padding-top: 6px;
        border-top: 1px solid var(--color-surface);
        flex: 1;
    }
    .d-leg-col {
        flex: 1;
    }
    .d-leg-head {
        font-size: 10px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.4px;
        color: var(--color-accent);
        margin-bottom: 2px;
    }
    .d-leg-row {
        display: flex;
        justify-content: space-between;
        padding: 2px 3px;
    }
    .d-leg-row:nth-child(even) {
        background: var(--color-surface);
    }
    .d-leg-k {
        color: var(--color-muted);
    }
    .d-leg-v {
        font-weight: 600;
    }

    /* KV rows in Server Details */
    .d-kv-row {
        display: flex;
        justify-content: space-between;
        font-size: 12px;
        padding: 3px 4px;
    }
    .d-kv-row:nth-child(even) {
        background: var(--color-surface);
    }
    .d-kv-k {
        color: var(--color-muted);
    }
    .d-kv-v {
        font-weight: 600;
        text-align: right;
    }

    /* Tooltip (CSS-only) */
    .has-tip {
        cursor: help;
        border-bottom: 1px dotted var(--color-subtle);
        position: relative;
    }
    .has-tip .tip {
        display: none;
        position: absolute;
        bottom: calc(100% + 6px);
        left: 50%;
        transform: translateX(-50%);
        background: var(--color-fg);
        color: var(--color-bg);
        font-size: 11px;
        font-weight: 400;
        padding: 6px 10px;
        border-radius: 4px;
        white-space: normal;
        width: 220px;
        line-height: 1.4;
        z-index: 20;
        pointer-events: none;
        text-transform: none;
        letter-spacing: 0;
    }
    .has-tip .tip::after {
        content: '';
        position: absolute;
        top: 100%;
        left: 50%;
        transform: translateX(-50%);
        border: 5px solid transparent;
        border-top-color: var(--color-fg);
    }
    .has-tip:hover .tip {
        display: block;
    }

    /* Offline placeholder */
    .d-offline {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-subtle);
        font-style: italic;
        font-size: 11px;
        padding: 20px 0;
    }

    /* Action buttons */
    .d-actions {
        display: flex;
        gap: 6px;
        margin-top: auto;
        padding-top: 8px;
        border-top: 1px solid var(--color-surface);
    }
    .d-actions .btn-brutal {
        flex: 1;
        font-size: 0.7rem;
        padding: 5px 10px;
    }
    .d-btn-hist {
        background: var(--color-card);
        color: var(--color-accent);
        border-color: var(--color-accent);
    }
    .d-btn-rm {
        background: var(--color-card);
        color: var(--color-red);
        border-color: var(--color-red);
    }
</style>
