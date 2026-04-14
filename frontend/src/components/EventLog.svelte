<script>
    import { appState } from '../lib/state.svelte.js';
    import { resolveThresholds, getThresholdColor } from '../lib/thresholds.js';

    // Consume host filter from History button navigation.
    let initialFilter = appState.eventHostFilter || '';
    appState.eventHostFilter = '';

    let search = $state(initialFilter);
    let expanded = $state(false);

    /** @type {HTMLDivElement|null} */
    let logEl = $state(null);

    // Collapse the expanded overlay when Escape is pressed.
    $effect(() => {
        function onKey(e) {
            if (e.key === 'Escape' && expanded) expanded = false;
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    });

    /**
     * Format seconds into a compact human-readable duration.
     * @param {number|null} secs
     * @returns {string}
     */
    function fmtDuration(secs) {
        if (secs == null || secs < 0) return '—';
        if (secs < 60) return `${secs}s`;
        const m = Math.floor(secs / 60);
        const s = secs % 60;
        if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
        const h = Math.floor(m / 60);
        const rm = m % 60;
        return rm > 0 ? `${h}h ${rm}m` : `${h}h`;
    }

    /**
     * Compact drain mode label.
     * @param {string|null} mode
     * @returns {string}
     */
    function fmtMode(mode) {
        if (!mode) return '—';
        if (mode === 'ALLOW_ALL_CONNECTIONS') return 'ALLOW-ALL';
        if (mode.includes('PREVENT')) return 'RECONNECT-ONLY';
        return mode;
    }

    // Config-derived thresholds — matches the rest of the app.
    let perfCfg = $derived(appState.config?.performance ?? null);
    let cpuT = $derived(resolveThresholds('cpu', perfCfg));
    let memT = $derived(resolveThresholds('mem', perfCfg));
    let delayT = $derived(resolveThresholds('inputDelay', perfCfg));

    /** @param {number} v @param {{warn:number,crit:number}} t @returns {'ok'|'warn'|'crit'} */
    function sev(v, t) {
        const c = getThresholdColor(v, t.warn, t.crit);
        return c === 'red' ? 'crit' : c === 'amber' ? 'warn' : 'ok';
    }

    /** @param {number} pct */
    function cpuSev(pct) {
        return sev(pct, cpuT);
    }

    /** @param {number} usedPct — percentage USED (event stores % used) */
    function memSev(usedPct) {
        return sev(usedPct, memT);
    }

    /** @param {number} ms */
    function delaySev(ms) {
        return sev(ms, delayT);
    }

    /**
     * Parse a raw event (string or structured object) into a display-friendly object.
     *
     * @param {unknown} raw
     * @returns {{
     *   time: string, host: string, text: string, sev: 'ok'|'grace'|'alert'|'off',
     *   transition: boolean,
     *   drain_state: string|null, drain_mode: string|null,
     *   state_duration_seconds: number|null, changed_by: string,
     *   sessions_active: number|null, sessions_disconnected: number|null, sessions_max: number|null,
     *   cpu_pct: number|null, mem_used_pct: number|null, input_delay_p95_ms: number|null,
     *   pages_sec: number|null, tcp_retrans_sec: number|null, disk_queue: number|null,
     *   fleet_servers: number|null, fleet_sessions: number|null,
     *   fleet_cpu_pct: number|null, fleet_mem_used_pct: number|null,
     *   fleet_input_delay_p95: number|null, fleet_pages_sec: number|null,
     *   fleet_tcp_retrans_sec: number|null, fleet_disk_queue: number|null,
     * }}
     */
    function parseEvent(raw) {
        if (raw && typeof raw === 'object') {
            const e = /** @type {any} */ (raw);
            return {
                time: e.time ?? '',
                host: e.host ?? '',
                text: e.text ?? String(raw),
                sev: e.sev ?? 'ok',
                transition: e.transition ?? false,
                drain_state: e.drain_state ?? null,
                drain_mode: e.drain_mode ?? null,
                state_duration_seconds: e.state_duration_seconds ?? null,
                changed_by: e.changed_by ?? '',
                sessions_active: e.sessions_active ?? null,
                sessions_disconnected: e.sessions_disconnected ?? null,
                sessions_max: e.sessions_max ?? null,
                cpu_pct: e.cpu_pct ?? null,
                mem_used_pct: e.mem_used_pct ?? null,
                input_delay_p95_ms: e.input_delay_p95_ms ?? null,
                pages_sec: e.pages_sec ?? null,
                tcp_retrans_sec: e.tcp_retrans_sec ?? null,
                disk_queue: e.disk_queue ?? null,
                fleet_servers: e.fleet_servers ?? null,
                fleet_sessions: e.fleet_sessions ?? null,
                fleet_cpu_pct: e.fleet_cpu_pct ?? null,
                fleet_mem_used_pct: e.fleet_mem_used_pct ?? null,
                fleet_input_delay_p95: e.fleet_input_delay_p95 ?? null,
                fleet_pages_sec: e.fleet_pages_sec ?? null,
                fleet_tcp_retrans_sec: e.fleet_tcp_retrans_sec ?? null,
                fleet_disk_queue: e.fleet_disk_queue ?? null,
            };
        }

        // Plain string fallback (legacy format "[10:30:00] Some message")
        const str = String(raw);
        const timeMatch = str.match(/^\[([^\]]+)\]/);
        const time = timeMatch ? timeMatch[1] : '';
        const rest = timeMatch ? str.slice(timeMatch[0].length).trim() : str;

        /** @type {'ok'|'grace'|'alert'|'off'} */
        let sev = 'ok';
        if (/fail|error|disconnect/i.test(rest)) sev = 'alert';
        else if (/warn|grace/i.test(rest)) sev = 'grace';

        return {
            time,
            host: '',
            text: rest,
            sev,
            transition: false,
            drain_state: null,
            drain_mode: null,
            state_duration_seconds: null,
            changed_by: '',
            sessions_active: null,
            sessions_disconnected: null,
            sessions_max: null,
            cpu_pct: null,
            mem_used_pct: null,
            input_delay_p95_ms: null,
            pages_sec: null,
            tcp_retrans_sec: null,
            disk_queue: null,
            fleet_servers: null,
            fleet_sessions: null,
            fleet_cpu_pct: null,
            fleet_mem_used_pct: null,
            fleet_input_delay_p95: null,
            fleet_pages_sec: null,
            fleet_tcp_retrans_sec: null,
            fleet_disk_queue: null,
        };
    }

    let parsed = $derived(appState.events.map(parseEvent));

    let filtered = $derived(
        search
            ? parsed.filter((e) => `${e.time} ${e.host} ${e.text}`.toLowerCase().includes(search.toLowerCase()))
            : parsed,
    );

    // Auto-scroll to top when new events arrive (newest is prepended = top of list).
    $effect(() => {
        void appState.events.length;
        if (!logEl) return;
        if (logEl.scrollTop < 80) {
            logEl.scrollTop = 0;
        }
    });
</script>

<div class="log-section">
    <div class="section-label">EVENT LOG</div>
    <div class="log-bar">
        <input
            class="log-search"
            type="search"
            placeholder="Filter events..."
            aria-label="Filter event log"
            bind:value={search}
        />
        <button
            class="btn-expand btn-brutal"
            onclick={() => {
                expanded = !expanded;
            }}
        >
            {expanded ? '⤡ Collapse' : '⤢ Expand'}
        </button>
    </div>

    <div class="log scrollbar-styled" class:expanded bind:this={logEl}>
        {#if expanded}
            <div class="expanded-header">
                <div class="log-title">EVENT LOG · {filtered.length} events</div>
                <input
                    class="log-search expanded-search"
                    type="search"
                    placeholder="Filter events..."
                    aria-label="Filter event log"
                    bind:value={search}
                />
                <button
                    class="btn-brutal expanded-close"
                    onclick={() => {
                        expanded = false;
                    }}>✕ Close</button
                >
            </div>
        {/if}
        {#if !filtered.length}
            <div class="evt-empty">
                {search ? 'No matching events.' : 'Waiting for data...'}
            </div>
        {:else}
            {#each filtered as evt}
                <div class="evt sev-{evt.sev}" class:is-transition={evt.transition}>
                    <!-- Primary line: time · host · message -->
                    <div class="evt-row">
                        <span class="evt-time">{evt.time}</span>
                        {#if evt.host}<span class="evt-host">{evt.host}</span>{/if}
                        <span class="evt-msg sev-{evt.sev}">{evt.text}</span>
                    </div>

                    {#if evt.drain_state !== null}
                        <!-- Per-server transition: drain state + mode + duration + changed_by -->
                        <div class="evt-detail">
                            <span class="det-badge state-{evt.drain_state}">{evt.drain_state.toUpperCase()}</span>
                            <span class="det-kv"
                                ><span class="det-k">MODE</span><span class="det-v">{fmtMode(evt.drain_mode)}</span
                                ></span
                            >
                            {#if evt.state_duration_seconds !== null}
                                <span class="det-kv"
                                    ><span class="det-k">DUR</span><span class="det-v"
                                        >{fmtDuration(evt.state_duration_seconds)}</span
                                    ></span
                                >
                            {/if}
                            {#if evt.changed_by}
                                <span class="det-kv"
                                    ><span class="det-k">BY</span><span class="det-v">{evt.changed_by}</span></span
                                >
                            {/if}
                        </div>

                        <!-- Per-server metrics row -->
                        {#if evt.sessions_active !== null}
                            {@const totalSess = (evt.sessions_active ?? 0) + (evt.sessions_disconnected ?? 0)}
                            {@const sessPct =
                                evt.sessions_max && evt.sessions_max > 0
                                    ? Math.round((totalSess / evt.sessions_max) * 100)
                                    : null}
                            <div class="evt-metrics">
                                <span class="det-kv">
                                    <span class="det-k">SESS</span>
                                    <span class="det-v"
                                        >{evt.sessions_active}<span class="det-dim">/</span
                                        >{evt.sessions_disconnected}<span class="det-dim">/</span
                                        >{evt.sessions_max}{sessPct !== null ? ` (${sessPct}%)` : ''}</span
                                    >
                                </span>
                                {#if evt.cpu_pct !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">CPU</span><span class="det-v val-{cpuSev(evt.cpu_pct)}"
                                            >{evt.cpu_pct.toFixed(1)}%</span
                                        ></span
                                    >
                                {/if}
                                {#if evt.mem_used_pct !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">MEM</span><span
                                            class="det-v val-{memSev(evt.mem_used_pct)}">{evt.mem_used_pct}%</span
                                        ></span
                                    >
                                {/if}
                                {#if evt.input_delay_p95_ms !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">DELAY P95</span><span
                                            class="det-v val-{delaySev(evt.input_delay_p95_ms)}"
                                            >{evt.input_delay_p95_ms.toFixed(1)}ms</span
                                        ></span
                                    >
                                {/if}
                                {#if evt.pages_sec !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">PGS/S</span><span class="det-v"
                                            >{evt.pages_sec.toFixed(1)}</span
                                        ></span
                                    >
                                {/if}
                                {#if evt.tcp_retrans_sec !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">TCP-RET/S</span><span class="det-v"
                                            >{evt.tcp_retrans_sec.toFixed(1)}</span
                                        ></span
                                    >
                                {/if}
                                {#if evt.disk_queue !== null}
                                    <span class="det-kv"
                                        ><span class="det-k">DISK-Q</span><span class="det-v"
                                            >{evt.disk_queue.toFixed(2)}</span
                                        ></span
                                    >
                                {/if}
                            </div>
                        {/if}
                    {:else if evt.fleet_cpu_pct !== null}
                        <!-- Periodic refresh: fleet-wide metrics summary -->
                        <div class="evt-metrics">
                            <span class="det-kv"
                                ><span class="det-k">CPU</span><span class="det-v val-{cpuSev(evt.fleet_cpu_pct)}"
                                    >{evt.fleet_cpu_pct}%</span
                                ></span
                            >
                            {#if evt.fleet_mem_used_pct !== null}
                                <span class="det-kv"
                                    ><span class="det-k">MEM</span><span
                                        class="det-v val-{memSev(evt.fleet_mem_used_pct)}"
                                        >{evt.fleet_mem_used_pct}%</span
                                    ></span
                                >
                            {/if}
                            {#if evt.fleet_input_delay_p95 !== null}
                                <span class="det-kv"
                                    ><span class="det-k">DELAY P95</span><span
                                        class="det-v val-{delaySev(evt.fleet_input_delay_p95)}"
                                        >{evt.fleet_input_delay_p95}ms</span
                                    ></span
                                >
                            {/if}
                            {#if evt.fleet_pages_sec !== null}
                                <span class="det-kv"
                                    ><span class="det-k">PGS/S</span><span class="det-v">{evt.fleet_pages_sec}</span
                                    ></span
                                >
                            {/if}
                            {#if evt.fleet_tcp_retrans_sec !== null}
                                <span class="det-kv"
                                    ><span class="det-k">TCP-RET/S</span><span class="det-v"
                                        >{evt.fleet_tcp_retrans_sec}</span
                                    ></span
                                >
                            {/if}
                            {#if evt.fleet_disk_queue !== null}
                                <span class="det-kv"
                                    ><span class="det-k">DISK-Q</span><span class="det-v">{evt.fleet_disk_queue}</span
                                    ></span
                                >
                            {/if}
                        </div>
                    {/if}
                </div>
            {/each}
        {/if}
    </div>
</div>

<style>
    .log-section {
        margin-bottom: 24px;
    }

    .log-bar {
        display: flex;
        align-items: center;
        gap: 10px;
        margin-bottom: 10px;
    }

    .log-search {
        flex: 1;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
        padding: 8px 12px;
        background: var(--color-card);
        color: var(--color-fg);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        outline: none;
    }

    .log-search:focus {
        box-shadow: 0 0 0 2px var(--color-accent);
    }

    .log-search::placeholder {
        color: var(--color-subtle);
    }

    .btn-expand {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.72rem;
        font-weight: 700;
        padding: 6px 14px;
        background: var(--color-card);
        color: var(--color-fg);
        white-space: nowrap;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        cursor: pointer;
        transition:
            box-shadow 0.12s,
            transform 0.12s;
    }

    .btn-expand:active {
        box-shadow: 1px 1px 0 var(--color-shadow);
        transform: translate(1px, 1px);
    }

    /* ── Log panel ───────────────────────────────────────────────────── */

    .log {
        background: var(--color-surface);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
        height: calc(100vh - 260px);
        min-height: 300px;
        overflow-y: auto;
        padding: 14px 16px;
    }

    .log.expanded {
        position: fixed;
        inset: 0;
        z-index: 200;
        height: 100vh;
        border-radius: 0;
        box-shadow: none;
        border: none;
        padding-top: 0;
    }

    .expanded-header {
        position: sticky;
        top: 0;
        z-index: 1;
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 12px 0;
        background: var(--color-surface);
        border-bottom: 1.5px solid var(--color-border);
        margin-bottom: 8px;
    }

    .expanded-search {
        flex: 1;
        max-width: 400px;
    }

    .expanded-close {
        font-size: 0.7rem;
        padding: 5px 14px;
        background: var(--color-card);
        color: var(--color-fg);
        flex-shrink: 0;
    }

    .log-title {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.1em;
        color: var(--color-muted);
        white-space: nowrap;
        flex-shrink: 0;
    }

    /* ── Event entry ─────────────────────────────────────────────────── */

    .evt {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.74rem;
        line-height: 1.6;
        color: var(--color-fg);
        padding: 6px 8px 6px 10px;
        border-bottom: 1px solid color-mix(in srgb, var(--color-border) 18%, transparent);
        border-left: 3px solid transparent;
        animation: fadeIn 0.12s linear;
    }

    .evt.sev-ok {
        border-left-color: var(--color-green);
    }
    .evt.sev-grace {
        border-left-color: var(--color-amber);
        background: color-mix(in srgb, var(--color-amber) 8%, transparent);
    }
    .evt.sev-alert {
        border-left-color: var(--color-red);
        background: color-mix(in srgb, var(--color-red) 8%, transparent);
    }
    .evt.sev-off {
        border-left-color: var(--color-subtle);
        opacity: 0.65;
    }

    .evt.is-transition {
        border-left-width: 4px;
    }
    .evt.sev-grace.is-transition {
        background: color-mix(in srgb, var(--color-amber) 12%, transparent);
    }
    .evt.sev-alert.is-transition {
        background: color-mix(in srgb, var(--color-red) 12%, transparent);
    }

    /* ── Primary row ─────────────────────────────────────────────────── */

    .evt-row {
        display: flex;
        align-items: baseline;
        gap: 6px;
        flex-wrap: wrap;
    }

    .evt-time {
        color: var(--color-muted);
        flex-shrink: 0;
    }

    .evt-host {
        color: var(--color-accent);
        font-weight: 700;
        flex-shrink: 0;
    }

    .evt-msg {
        color: var(--color-fg);
    }
    .evt-msg.sev-ok {
        color: var(--color-green);
    }
    .evt-msg.sev-alert {
        color: var(--color-red);
    }
    .evt-msg.sev-grace {
        color: var(--color-amber);
    }
    .evt-msg.sev-off {
        color: var(--color-subtle);
    }

    /* ── Detail rows ─────────────────────────────────────────────────── */

    .evt-detail,
    .evt-metrics {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 0 10px;
        margin-top: 3px;
        padding-left: 2px;
    }

    .evt-metrics {
        margin-top: 2px;
        opacity: 0.9;
    }

    /* Drain-state badge */
    .det-badge {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.62rem;
        font-weight: 700;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        padding: 1px 7px;
        border-radius: 3px;
        border: 1.5px solid color-mix(in srgb, var(--color-border) 30%, transparent);
        flex-shrink: 0;
    }

    .det-badge.state-ok {
        background: color-mix(in srgb, var(--color-green) 18%, var(--color-surface));
        color: var(--color-green);
    }
    .det-badge.state-grace {
        background: color-mix(in srgb, var(--color-amber) 18%, var(--color-surface));
        color: var(--color-amber);
    }
    .det-badge.state-alert {
        background: color-mix(in srgb, var(--color-red) 18%, var(--color-surface));
        color: var(--color-red);
    }
    .det-badge.state-off {
        background: color-mix(in srgb, var(--color-subtle) 18%, var(--color-surface));
        color: var(--color-muted);
    }

    /* Key–value pairs */
    .det-kv {
        display: inline-flex;
        align-items: baseline;
        gap: 3px;
        white-space: nowrap;
    }

    .det-k {
        font-size: 0.6rem;
        font-weight: 700;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--color-subtle);
    }

    .det-v {
        font-size: 0.72rem;
        font-weight: 600;
        color: var(--color-fg);
    }

    .det-dim {
        color: var(--color-subtle);
    }

    /* Metric value severity colouring */
    .det-v.val-ok {
        color: var(--color-green);
    }
    .det-v.val-warn {
        color: var(--color-amber);
    }
    .det-v.val-crit {
        color: var(--color-red);
    }

    /* ── Empty state ─────────────────────────────────────────────────── */

    .evt-empty {
        color: var(--color-subtle);
        font-style: italic;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.82rem;
        padding: 20px 0;
        text-align: center;
    }
</style>
