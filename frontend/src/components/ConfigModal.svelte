<script>
    import { fetchSettings, saveSettings, sendNotifyTest, fetchMaintenance } from '../lib/api.js';
    import { appState } from '../lib/state.svelte.js';
    import { toast } from '../lib/toast.svelte.js';
    import { rel } from '../lib/utils.js';
    import NotificationTargets from './NotificationTargets.svelte';
    import TargetEditModal from './TargetEditModal.svelte';
    import TargetDeleteModal from './TargetDeleteModal.svelte';
    import ConfirmDialog from './ConfirmDialog.svelte';
    import { Coffee, Save, X, Play, ChevronDown, ChevronRight, Settings, Award, Radio, ShieldAlert, Users, Activity, Siren, Wrench, Monitor } from 'lucide-svelte';

    let { onclose } = $props();

    let config = $state(null);
    let original = $state(null);
    let loading = $state(true);
    let saving = $state(false);
    let testing = $state(false);
    let showConfirmClose = $state(false);
    let closing = $state(false);
    const CLOSE_MS = 150;

    function animateClose() {
        closing = true;
        setTimeout(() => onclose?.(), CLOSE_MS);
    }

    // Sub-modal state (owned here so modals render outside .settings-modal)
    let editTarget = $state(null);
    let editIdx = $state(-1);
    let deleteIdx = $state(-1);
    let subModalOpen = $derived(editTarget !== null || deleteIdx >= 0);

    function saveTarget(t) {
        if (!config) return;
        if (editIdx >= 0) {
            config.notifications = config.notifications.map((x, i) => (i === editIdx ? t : x));
        } else {
            config.notifications = [...config.notifications, { ...t, id: crypto.randomUUID() }];
        }
        editTarget = null;
    }

    function confirmDelete() {
        if (deleteIdx >= 0 && config) {
            config.notifications = config.notifications.filter((_, i) => i !== deleteIdx);
        }
        deleteIdx = -1;
    }

    let dirty = $derived.by(() => {
        if (!config || !original) return false;
        return JSON.stringify(config) !== JSON.stringify(original);
    });

    // ---------------------------------------------------------------------------
    // Maintenance job widget — moved here from the global Footer. The Config
    // modal is the natural operator landing place for day-two ops info, and
    // keeping the widget out of the footer removes one always-on polling
    // loop from the idle UI. Polling only runs while the modal is open.
    // ---------------------------------------------------------------------------
    /** @typedef {import('../lib/api.js').MaintenanceJob} MaintenanceJob */
    /** @type {MaintenanceJob[]} */
    let maintenanceJobs = $state([]);
    /** @type {string|null} */
    let maintenanceServerTime = $state(null);
    let maintenanceLoading = $state(false);
    let maintenanceError = $state('');
    let maintenanceFetching = false;

    async function refreshMaintenance() {
        if (maintenanceFetching) return;
        maintenanceFetching = true;
        if (maintenanceJobs.length === 0) maintenanceLoading = true;
        try {
            const data = await fetchMaintenance();
            maintenanceJobs = Array.isArray(data?.jobs) ? data.jobs : [];
            maintenanceServerTime = data?.server_time ?? null;
            maintenanceError = '';
        } catch (e) {
            maintenanceError = `Maintenance fetch failed: ${e?.message ?? e}`;
        } finally {
            maintenanceLoading = false;
            maintenanceFetching = false;
        }
    }

    $effect(() => {
        // Modal mounted → kick off polling. 15-second cadence matches the
        // old footer widget and contracts/http-maintenance.md. Cleanup on
        // unmount stops the interval — no orphan fetches.
        refreshMaintenance();
        const id = setInterval(refreshMaintenance, 15_000);
        return () => clearInterval(id);
    });

    const JOB_LONG = {
        aggregator_5min: 'Aggregator 5m',
        aggregator_hourly: 'Aggregator 1h',
        retention: 'Retention',
        jsonl_migration: 'JSONL Migration',
        drift_reconciliation: 'Drift Reconciliation',
    };
    // Stable display order; jobs not listed fall to the tail alphabetically.
    const JOB_ORDER = ['aggregator_5min', 'aggregator_hourly', 'retention', 'drift_reconciliation', 'jsonl_migration'];
    // JSONL migration is a one-shot boot job; rendered for completeness here
    // (unlike in the footer we have the room) — operators have asked what
    // "JSONL = skipped" means at least once.
    const maintNowMs = $derived.by(() => {
        if (!maintenanceServerTime) return Date.now();
        const t = new Date(maintenanceServerTime).getTime();
        return Number.isFinite(t) && t > 0 ? t : Date.now();
    });
    const orderedMaintJobs = $derived.by(() => {
        const idx = (name) => {
            const p = JOB_ORDER.indexOf(name);
            return p === -1 ? JOB_ORDER.length : p;
        };
        return [...maintenanceJobs].sort((a, b) => {
            const ai = idx(a.name);
            const bi = idx(b.name);
            if (ai !== bi) return ai - bi;
            return a.name.localeCompare(b.name);
        });
    });

    /** @param {MaintenanceJob} job */
    function jobGlyph(job) {
        if (job.outcome === 'failure') return '✕';
        if (job.overdue) return '!';
        if (job.outcome === 'skipped') return '—';
        return '✓';
    }
    /** @param {MaintenanceJob} job */
    function jobStateClass(job) {
        if (job.outcome === 'failure') return 'fail';
        if (job.overdue) return 'warn';
        if (job.outcome === 'skipped') return 'skip';
        return 'ok';
    }
    /** @param {MaintenanceJob} job */
    function jobLabel(name) {
        return JOB_LONG[name] ?? name;
    }

    // Maintenance section is collapsed by default — the ops info is useful
    // but it's not what operators open Config for, so don't let it steal
    // viewport at the bottom of the modal unless explicitly asked for.
    let showMaintenance = $state(false);

    $effect(() => {
        loadConfig();
    });

    async function loadConfig() {
        loading = true;
        try {
            const c = await fetchSettings();
            config = JSON.parse(JSON.stringify(c));
            original = JSON.parse(JSON.stringify(c));
        } catch (e) {
            toast.err('Failed to load config: ' + e.message);
        } finally {
            loading = false;
        }
    }

    // ---------------------------------------------------------------------------
    // Global fire-based presets
    // ---------------------------------------------------------------------------

    const FIRE_PRESETS = [
        {
            level: 1,
            label: 'Chill',
            icon: Coffee,
            beans: 1,
            poll_interval: 60,
            sample_interval: 60,
            grace_period: 60,
            session_warning: 90,
            cpu_warn: 80,
            cpu_crit: 95,
            mem_warn: 80,
            mem_crit: 95,
            delay_warn: 75,
            delay_crit: 150,
            delay_percentile: 'p50',
            load_sustain_sec: 120,
            delay_sustain_sec: 120,
        },
        {
            level: 2,
            label: 'Anxious',
            icon: Coffee,
            beans: 2,
            poll_interval: 30,
            sample_interval: 30,
            grace_period: 30,
            session_warning: 80,
            cpu_warn: 70,
            cpu_crit: 90,
            mem_warn: 75,
            mem_crit: 90,
            delay_warn: 50,
            delay_crit: 100,
            delay_percentile: 'p95',
            load_sustain_sec: 60,
            delay_sustain_sec: 120,
        },
        {
            level: 3,
            label: 'Twitchy',
            icon: Coffee,
            beans: 3,
            poll_interval: 15,
            sample_interval: 15,
            grace_period: 15,
            session_warning: 70,
            cpu_warn: 60,
            cpu_crit: 80,
            mem_warn: 65,
            mem_crit: 85,
            delay_warn: 25,
            delay_crit: 60,
            delay_percentile: 'p95',
            load_sustain_sec: 30,
            delay_sustain_sec: 60,
        },
    ];

    const POLL_INTERVAL_PRESETS = [15, 30, 60];
    const SUSTAIN_PRESETS = [30, 60, 120, 300]; // seconds

    // ---------------------------------------------------------------------------
    // Sustain window — bound directly to config.performance.load_alert_delay_sec
    // and input_delay_alert_delay_sec.  Backend computes consecutive polls.
    // ---------------------------------------------------------------------------

    /** Format seconds as a human-readable label. */
    function fmtDuration(sec) {
        if (sec < 60) return sec + 's';
        if (sec % 60 === 0) return (sec / 60) + 'm';
        if (sec % 30 === 0) return (sec / 60).toFixed(1).replace('.0', '') + 'm';
        return Math.floor(sec / 60) + 'm ' + (sec % 60) + 's';
    }

    /** Compute consecutive polls from sustain duration and interval (display only). */
    function sustainToPolls(sustainSec, intervalSec) {
        return Math.max(1, Math.ceil(sustainSec / (intervalSec || 30)));
    }

    let loadPolls = $derived(sustainToPolls(config?.performance?.load_alert_delay_sec || 60, config?.performance?.sample_interval_sec));
    let delayPolls = $derived(sustainToPolls(config?.performance?.input_delay_alert_delay_sec || 90, config?.performance?.sample_interval_sec));

    let activeFireLevel = $derived.by(() => {
        if (!config) return -1;
        const p = config.performance;
        for (const pr of FIRE_PRESETS) {
            if (
                config.poll_interval === pr.poll_interval &&
                config.grace_period === pr.grace_period &&
                config.session_warning_threshold === pr.session_warning &&
                (p?.sample_interval_sec || 30) === pr.sample_interval &&
                p?.cpu_warn_pct === pr.cpu_warn &&
                p?.cpu_crit_pct === pr.cpu_crit &&
                p?.mem_warn_pct === pr.mem_warn &&
                p?.mem_crit_pct === pr.mem_crit &&
                p?.input_delay_warn_ms === pr.delay_warn &&
                p?.input_delay_crit_ms === pr.delay_crit &&
                (p?.input_delay_percentile || 'p95') === pr.delay_percentile &&
                (p?.load_alert_delay_sec || 60) === pr.load_sustain_sec &&
                (p?.input_delay_alert_delay_sec || 90) === pr.delay_sustain_sec
            ) {
                return pr.level;
            }
        }
        return -1;
    });

    function applyFirePreset(preset) {
        if (!config) return;
        config.poll_interval = preset.poll_interval;
        config.grace_period = preset.grace_period;
        config.session_warning_threshold = preset.session_warning;
        if (config.performance) {
            config.performance.enabled = true;
            config.performance.sample_interval_sec = preset.sample_interval;
            config.performance.cpu_warn_pct = preset.cpu_warn;
            config.performance.cpu_crit_pct = preset.cpu_crit;
            config.performance.mem_warn_pct = preset.mem_warn;
            config.performance.mem_crit_pct = preset.mem_crit;
            config.performance.input_delay_warn_ms = preset.delay_warn;
            config.performance.input_delay_crit_ms = preset.delay_crit;
            config.performance.input_delay_percentile = preset.delay_percentile;
            config.performance.load_alert_delay_sec = preset.load_sustain_sec;
            config.performance.input_delay_alert_delay_sec = preset.delay_sustain_sec;
        }
    }

    let showManual = $state(false);

    // ---------------------------------------------------------------------------
    // Validation & actions
    // ---------------------------------------------------------------------------

    function validateThresholds() {
        const p = config?.performance;
        if (!p?.enabled) return null;
        if (p.cpu_warn_pct > 0 && p.cpu_crit_pct > 0 && p.cpu_warn_pct >= p.cpu_crit_pct)
            return 'CPU warn threshold must be less than crit';
        if (p.mem_warn_pct > 0 && p.mem_crit_pct > 0 && p.mem_warn_pct >= p.mem_crit_pct)
            return 'Memory warn threshold must be less than crit';
        if (p.input_delay_warn_ms > 0 && p.input_delay_crit_ms > 0 && p.input_delay_warn_ms >= p.input_delay_crit_ms)
            return 'Input Delay warn threshold must be less than crit';
        return null;
    }

    /** @returns {Promise<boolean>} true on success */
    async function save() {
        if (!config) return false;
        const err = validateThresholds();
        if (err) {
            toast.err(err);
            return false;
        }
        saving = true;
        try {
            await saveSettings(config);
            original = JSON.parse(JSON.stringify(config));
            appState.config = JSON.parse(JSON.stringify(config));
            toast.ok('Settings saved');
            return true;
        } catch (e) {
            toast.err('Save failed: ' + e.message);
            return false;
        } finally {
            saving = false;
        }
    }

    async function sendTest() {
        const targets = config?.notifications?.filter((t) => t.url || t.type === 'email');
        if (!targets?.length) {
            toast.err('No notification targets configured');
            return;
        }
        testing = true;
        try {
            // Test each target individually with the unsaved config — no need to save first.
            // Each call returns {ok, results:[{type,url,ok,error}], error?} so we can show
            // the per-target failure reason rather than a generic count.
            const responses = await Promise.allSettled(targets.map((t) => sendNotifyTest(t)));
            const failures = []; // { type, url, error }
            let okCount = 0;
            responses.forEach((resp, i) => {
                const tgt = targets[i];
                if (resp.status === 'rejected') {
                    failures.push({
                        type: tgt.type,
                        url: tgt.url,
                        error: resp.reason?.detail || resp.reason?.message || String(resp.reason),
                    });
                    return;
                }
                const body = resp.value || {};
                const perTarget = body.results || [];
                if (perTarget.length === 0) {
                    if (body.ok) okCount++;
                    else failures.push({ type: tgt.type, url: tgt.url, error: body.error || 'unknown error' });
                    return;
                }
                for (const r of perTarget) {
                    if (r.ok) okCount++;
                    else failures.push({ type: r.type, url: r.url, error: r.error || 'unknown error' });
                }
            });
            if (failures.length === 0) {
                toast.ok(`Test sent to ${okCount} target${okCount > 1 ? 's' : ''}`);
            } else {
                const lines = failures.map((f) => `${f.type} ${f.url}: ${f.error}`);
                toast.err(`${failures.length} failed — ${lines.join(' | ')}`);
            }
        } catch (e) {
            toast.err('Test failed: ' + (e?.detail ?? e?.message ?? String(e)));
        } finally {
            testing = false;
        }
    }

    function requestClose() {
        if (dirty) {
            showConfirmClose = true;
            return;
        }
        animateClose();
    }

    function handleOverlayClick(e) {
        if (e.target !== e.currentTarget) return;
        requestClose();
    }

    $effect(() => {
        function onKey(e) {
            if (e.key === 'Escape') requestClose();
            if (e.key === 's' && (e.ctrlKey || e.metaKey)) {
                e.preventDefault();
                if (dirty && !saving) save();
            }
        }
        document.addEventListener('keydown', onKey);
        return () => document.removeEventListener('keydown', onKey);
    });

    const GRACE_PRESETS = [15, 30, 60];

    /** @type {HTMLInputElement|null} */
    let gracePeriodInput = $state(null);
    /** @type {HTMLInputElement|null} */
    let pollIntervalInput = $state(null);
    /** @type {HTMLInputElement|null} */
    let pollIntervalAgentInput = $state(null);
    /** @type {HTMLInputElement|null} */
    let sessionWarnInput = $state(null);
    /** @type {HTMLInputElement|null} */
    let loadSustainInput = $state(null);
    /** @type {HTMLInputElement|null} */
    let delaySustainInput = $state(null);
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div
    class="settings-overlay {subModalOpen ? 'sub-open' : ''} {closing ? 'closing' : ''}"
    onclick={handleOverlayClick}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
>
    <div class="modal-wrap">
        <span class="modal-badge"><Settings size={20} /></span>
        <div
            class="settings-modal scrollbar-styled"
            style={dirty ? 'background: color-mix(in srgb, var(--color-amber) 5%, var(--color-card));' : ''}
        >
            <div class="settings-title">
                <h2 class="modal-title serif">Dashboard Configuration</h2>
                <button class="settings-close" onclick={requestClose} aria-label="Close settings"
                    ><X size={20} /></button
                >
            </div>

            {#if loading}
                <div style="text-align:center;padding:40px;color:var(--color-muted)">Loading...</div>
            {:else if config}
                <!-- Global Alert Sensitivity -->
                <div class="settings-group">
                    <div class="section-header"><Coffee size={14} strokeWidth={2.5} /> Alert Sensitivity</div>
                    <div class="fire-row">
                        {#each FIRE_PRESETS as preset}
                            <button
                                class="fire-card fire-level-{preset.level} {activeFireLevel === preset.level
                                    ? 'active'
                                    : ''}"
                                onclick={() => applyFirePreset(preset)}
                            >
                                {#if activeFireLevel === preset.level}
                                    <span class="fire-seal fire-seal-{preset.level}"
                                        ><Award size={20} strokeWidth={2.5} /></span
                                    >
                                {/if}
                                <span class="fire-icon-wrap"
                                    >{#each { length: preset.beans } as _}<svg
                                            class="bean"
                                            viewBox="0 0 20 24"
                                            width="16"
                                            height="19"
                                            ><ellipse cx="10" cy="12" rx="8" ry="11" fill="currentColor" /><path
                                                d="M10 3 C8 8, 8 16, 10 21"
                                                stroke="var(--color-surface)"
                                                stroke-width="1.8"
                                                fill="none"
                                                stroke-linecap="round"
                                            /></svg
                                        >{/each}</span
                                >
                                <span class="fire-label">{preset.label}</span>
                                <span class="fire-tagline"
                                    >{preset.level === 1
                                        ? 'Easy does it'
                                        : preset.level === 2
                                          ? 'Sleep with one eye open'
                                          : 'No Sleep Till Brooklyn'}</span
                                >
                                <span class="fire-detail">
                                    Poll {preset.poll_interval}s · Escalation {preset.grace_period}m · Sessions {preset.session_warning}%
                                </span>
                                <span class="fire-detail">
                                    CPU {preset.cpu_warn}/{preset.cpu_crit}% · Mem {preset.mem_warn}/{preset.mem_crit}%
                                    · Delay {preset.delay_warn}/{preset.delay_crit}ms ({preset.delay_percentile.toUpperCase()})
                                </span>
                                <span class="fire-detail">
                                    Sustain {fmtDuration(preset.load_sustain_sec)} / {fmtDuration(preset.delay_sustain_sec)}
                                </span>
                            </button>
                        {/each}
                    </div>
                </div>

                <button class="fire-custom-toggle" onclick={() => (showManual = !showManual)}>
                    {#if showManual}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                    Customize settings manually
                </button>

                {#if showManual}

                    <!-- 1. Agent Poll Interval -->
                    <div class="settings-group">
                        <div class="section-header"><Radio size={14} strokeWidth={2.5} /> Agent Poll Interval</div>
                        <div class="settings-hint">How often each agent reports drain state, sessions, and metrics to the dashboard.</div>
                        <div class="repeat-pills">
                            {#each [15, 30, 60] as p}
                                <button
                                    class="btn-brutal gp-pill"
                                    class:active={config.poll_interval === p}
                                    onclick={() => (config.poll_interval = p)}>{p < 60 ? p + 's' : p / 60 + 'm'}</button
                                >
                            {/each}
                            <button
                                class="btn-brutal gp-pill gp-pill--dashed"
                                class:active={![15, 30, 60].includes(config.poll_interval)}
                                onclick={() => pollIntervalAgentInput?.focus()}>Custom</button
                            >
                        </div>
                        <div style="display:flex;align-items:center;gap:8px;margin-top:4px">
                            <input
                                type="number"
                                class="settings-num"
                                bind:value={config.poll_interval}
                                bind:this={pollIntervalAgentInput}
                                min="10"
                                max="86400"
                            />
                            <span class="settings-num-label">seconds (10–86400)</span>
                        </div>
                    </div>

                    <!-- 2. Escalation Window -->
                    <div class="settings-group">
                        <div class="section-header"><ShieldAlert size={14} strokeWidth={2.5} /> Escalation Window</div>
                        <div class="settings-hint">How long a server can stay in drain mode before its status escalates from Grace to Alert.</div>
                        <div class="repeat-pills">
                            {#each GRACE_PRESETS as p}
                                <button
                                    class="btn-brutal gp-pill"
                                    class:active={config.grace_period === p}
                                    onclick={() => (config.grace_period = p)}>{p < 60 ? p + 'm' : p / 60 + 'h'}</button
                                >
                            {/each}
                            <button
                                class="btn-brutal gp-pill gp-pill--dashed"
                                class:active={!GRACE_PRESETS.includes(config.grace_period)}
                                onclick={() => gracePeriodInput?.focus()}>Custom</button
                            >
                        </div>
                        <div style="display:flex;align-items:center;gap:8px">
                            <input
                                type="number"
                                class="settings-num"
                                bind:value={config.grace_period}
                                bind:this={gracePeriodInput}
                                min="1"
                                max="1440"
                            />
                            <span class="settings-num-label">minutes</span>
                        </div>
                    </div>

                    <!-- 3. Session Warning -->
                    <div class="settings-group">
                        <div class="section-header"><Users size={14} strokeWidth={2.5} /> Session Warning Threshold</div>
                        <div class="settings-hint">Alert when active sessions reach this percentage of the server's capacity.</div>
                        <div class="repeat-pills">
                            <button
                                class="btn-brutal gp-pill gp-pill--off"
                                class:active={config.session_warning_threshold === 0}
                                onclick={() => (config.session_warning_threshold = 0)}>Off</button
                            >
                            {#each [70, 80, 90] as p}
                                <button
                                    class="btn-brutal gp-pill"
                                    class:active={config.session_warning_threshold === p}
                                    onclick={() => (config.session_warning_threshold = p)}>{p}%</button
                                >
                            {/each}
                            <button
                                class="btn-brutal gp-pill gp-pill--dashed"
                                class:active={config.session_warning_threshold > 0 && ![70, 80, 90].includes(config.session_warning_threshold)}
                                onclick={() => sessionWarnInput?.focus()}>Custom</button
                            >
                        </div>
                        <div style="display:flex;align-items:center;gap:8px;margin-top:4px">
                            <input
                                type="number"
                                class="settings-num"
                                bind:value={config.session_warning_threshold}
                                bind:this={sessionWarnInput}
                                min="0"
                                max="100"
                            />
                            <span class="settings-num-label">%</span>
                        </div>
                    </div>

                    <!-- 4. Performance Monitoring -->
                    {#if config.performance}
                        <div class="settings-group">
                            <div class="section-header"><Activity size={14} strokeWidth={2.5} /> Performance Monitoring</div>
                            <label class="settings-check">
                                <input
                                    type="checkbox"
                                    bind:checked={config.performance.enabled}
                                    disabled={config.performance.force_disabled}
                                />
                                Enable performance monitoring{config.performance.force_disabled
                                    ? ' (disabled by server policy)'
                                    : ''}
                            </label>
                            {#if config.performance.enabled && !config.performance.force_disabled}
                                <!-- Poll Interval -->
                                <div class="subsection" style="margin-top:8px">
                                    <div class="settings-label">Poll Interval</div>
                                    <div class="settings-hint">How often each server is sampled for CPU, memory, and input delay.</div>
                                    <div class="repeat-pills">
                                        {#each POLL_INTERVAL_PRESETS as p}
                                            <button
                                                class="btn-brutal gp-pill"
                                                class:active={config.performance.sample_interval_sec === p}
                                                onclick={() => (config.performance.sample_interval_sec = p)}>{p}s</button
                                            >
                                        {/each}
                                        <button
                                            class="btn-brutal gp-pill gp-pill--dashed"
                                            class:active={!POLL_INTERVAL_PRESETS.includes(config.performance.sample_interval_sec)}
                                            onclick={() => pollIntervalInput?.focus()}>Custom</button
                                        >
                                    </div>
                                    <div style="display:flex;align-items:center;gap:8px;margin-top:4px">
                                        <input
                                            type="number"
                                            class="settings-num"
                                            bind:value={config.performance.sample_interval_sec}
                                            bind:this={pollIntervalInput}
                                            min="10"
                                            max="300"
                                        />
                                        <span class="settings-num-label">seconds (10–300)</span>
                                    </div>
                                </div>

                                <!-- Thresholds: CPU + Memory side by side -->
                                <div class="subsection">
                                <div class="settings-cfg-grid">
                                    <div>
                                        <div class="settings-label">CPU Thresholds</div>
                                        <div class="settings-hint">Alert when CPU stays above these levels.</div>
                                        <div class="repeat-pills">
                                            <button class="btn-brutal gp-pill gp-pill--off"
                                                class:active={config.performance.cpu_warn_pct === -1 && config.performance.cpu_crit_pct === -1}
                                                onclick={() => { config.performance.cpu_warn_pct = -1; config.performance.cpu_crit_pct = -1; }}>Off</button>
                                            {#each [[60,80],[70,90],[80,95]] as [w,c]}
                                                <button class="btn-brutal gp-pill"
                                                    class:active={config.performance.cpu_warn_pct === w && config.performance.cpu_crit_pct === c}
                                                    onclick={() => { config.performance.cpu_warn_pct = w; config.performance.cpu_crit_pct = c; }}>{w}/{c}%</button>
                                            {/each}
                                        </div>
                                        <div class="threshold-row" style="margin-top:4px">
                                            <span class="settings-num-label threshold-lbl">Warn</span>
                                            <input type="number" class="settings-num" bind:value={config.performance.cpu_warn_pct} min="-1" max="100" />
                                            <span class="settings-num-label threshold-unit">%</span>
                                            <span class="settings-num-label threshold-lbl">Crit</span>
                                            <input type="number" class="settings-num" bind:value={config.performance.cpu_crit_pct} min="-1" max="100" />
                                            <span class="settings-num-label threshold-unit">%</span>
                                        </div>
                                    </div>
                                    <div>
                                        <div class="settings-label">Memory Thresholds</div>
                                        <div class="settings-hint">Alert when memory stays above these levels.</div>
                                        <div class="repeat-pills">
                                            <button class="btn-brutal gp-pill gp-pill--off"
                                                class:active={config.performance.mem_warn_pct === -1 && config.performance.mem_crit_pct === -1}
                                                onclick={() => { config.performance.mem_warn_pct = -1; config.performance.mem_crit_pct = -1; }}>Off</button>
                                            {#each [[65,85],[75,90],[80,95]] as [w,c]}
                                                <button class="btn-brutal gp-pill"
                                                    class:active={config.performance.mem_warn_pct === w && config.performance.mem_crit_pct === c}
                                                    onclick={() => { config.performance.mem_warn_pct = w; config.performance.mem_crit_pct = c; }}>{w}/{c}%</button>
                                            {/each}
                                        </div>
                                        <div class="threshold-row" style="margin-top:4px">
                                            <span class="settings-num-label threshold-lbl">Warn</span>
                                            <input type="number" class="settings-num" bind:value={config.performance.mem_warn_pct} min="-1" max="100" />
                                            <span class="settings-num-label threshold-unit">%</span>
                                            <span class="settings-num-label threshold-lbl">Crit</span>
                                            <input type="number" class="settings-num" bind:value={config.performance.mem_crit_pct} min="-1" max="100" />
                                            <span class="settings-num-label threshold-unit">%</span>
                                        </div>
                                    </div>
                                </div>

                                <div class="settings-label" style="margin-top:20px">Input Delay Thresholds</div>
                                <div class="settings-hint">Alert when user input latency stays above these levels.</div>
                                <div class="repeat-pills">
                                    <button class="btn-brutal gp-pill gp-pill--off"
                                        class:active={config.performance.input_delay_warn_ms === -1 && config.performance.input_delay_crit_ms === -1}
                                        onclick={() => { config.performance.input_delay_warn_ms = -1; config.performance.input_delay_crit_ms = -1; }}>Off</button>
                                    {#each [[25,60],[50,100],[75,150]] as [w,c]}
                                        <button class="btn-brutal gp-pill"
                                            class:active={config.performance.input_delay_warn_ms === w && config.performance.input_delay_crit_ms === c}
                                            onclick={() => { config.performance.input_delay_warn_ms = w; config.performance.input_delay_crit_ms = c; }}>{w}/{c}ms</button>
                                    {/each}
                                </div>
                                <div class="threshold-row" style="margin-top:4px">
                                    <span class="settings-num-label threshold-lbl">Warn</span>
                                    <input type="number" class="settings-num" bind:value={config.performance.input_delay_warn_ms} min="-1" />
                                    <span class="settings-num-label threshold-unit">ms</span>
                                    <span class="settings-num-label threshold-lbl">Crit</span>
                                    <input type="number" class="settings-num" bind:value={config.performance.input_delay_crit_ms} min="-1" />
                                    <span class="settings-num-label threshold-unit">ms</span>
                                </div>
                                <div class="threshold-row" style="margin-top:8px">
                                    <span class="settings-num-label threshold-lbl">Percentile</span>
                                    <button
                                        class="btn-brutal pctl-pill"
                                        class:active={config.performance.input_delay_percentile === 'p50'}
                                        onclick={() => config.performance.input_delay_percentile = 'p50'}
                                    >P50</button>
                                    <button
                                        class="btn-brutal pctl-pill"
                                        class:active={config.performance.input_delay_percentile === 'p95'}
                                        onclick={() => config.performance.input_delay_percentile = 'p95'}
                                    >P95</button>
                                </div>
                                </div>

                                <!-- Alert Sustain Window -->
                                <div class="subsection">
                                <div class="settings-label">Alert Sustain Window</div>
                                <div class="settings-hint">How long a metric must breach its threshold before an alert fires.</div>
                                <div class="settings-cfg-grid">
                                    <div>
                                        <div class="settings-num-label threshold-lbl" style="margin-bottom:4px">CPU / Memory</div>
                                        <div class="repeat-pills">
                                            {#each SUSTAIN_PRESETS as s}
                                                <button
                                                    class="btn-brutal gp-pill"
                                                    class:active={config.performance.load_alert_delay_sec === s}
                                                    onclick={() => (config.performance.load_alert_delay_sec = s)}>{fmtDuration(s)}</button
                                                >
                                            {/each}
                                            <button
                                                class="btn-brutal gp-pill gp-pill--dashed"
                                                class:active={!SUSTAIN_PRESETS.includes(config.performance.load_alert_delay_sec)}
                                                onclick={() => loadSustainInput?.focus()}>Custom</button
                                            >
                                        </div>
                                        <div style="display:flex;align-items:center;gap:8px;margin-top:4px">
                                            <input
                                                type="number"
                                                class="settings-num"
                                                bind:value={config.performance.load_alert_delay_sec}
                                                bind:this={loadSustainInput}
                                                min="10"
                                                step="10"
                                            />
                                            <span class="settings-num-label">seconds</span>
                                            <span class="settings-hint" style="margin-bottom:0">({loadPolls} poll{loadPolls === 1 ? '' : 's'})</span>
                                        </div>
                                    </div>
                                    <div>
                                        <div class="settings-num-label threshold-lbl" style="margin-bottom:4px">Input Delay</div>
                                        <div class="repeat-pills">
                                            {#each SUSTAIN_PRESETS as s}
                                                <button
                                                    class="btn-brutal gp-pill"
                                                    class:active={config.performance.input_delay_alert_delay_sec === s}
                                                    onclick={() => (config.performance.input_delay_alert_delay_sec = s)}>{fmtDuration(s)}</button
                                                >
                                            {/each}
                                            <button
                                                class="btn-brutal gp-pill gp-pill--dashed"
                                                class:active={!SUSTAIN_PRESETS.includes(config.performance.input_delay_alert_delay_sec)}
                                                onclick={() => delaySustainInput?.focus()}>Custom</button
                                            >
                                        </div>
                                        <div style="display:flex;align-items:center;gap:8px;margin-top:4px">
                                            <input
                                                type="number"
                                                class="settings-num"
                                                bind:value={config.performance.input_delay_alert_delay_sec}
                                                bind:this={delaySustainInput}
                                                min="10"
                                                step="10"
                                            />
                                            <span class="settings-num-label">seconds</span>
                                            <span class="settings-hint" style="margin-bottom:0">({delayPolls} poll{delayPolls === 1 ? '' : 's'})</span>
                                        </div>
                                    </div>
                                </div>
                                </div>

                                <div style="margin-top:10px">
                                    <label class="settings-check">
                                        <input type="checkbox" bind:checked={config.performance.collect_per_session} />
                                        Per-session CPU accounting
                                    </label>
                                    <label class="settings-check">
                                        <input type="checkbox" bind:checked={config.performance.collect_remotefx} />
                                        RemoteFX monitoring
                                    </label>
                                </div>
                            {/if}
                        </div>
                    {/if}
                {/if}

                <!-- Event Log Anomaly Detection -->
                {#if config.evtspike}
                    <div class="settings-group">
                        <div class="section-header"><Siren size={14} strokeWidth={2.5} /> Event Log Anomaly Detection</div>
                        <div class="settings-hint">Watches Windows event-log channels and fires notifications on the event_spike trigger when a confirmed anomaly is detected.</div>
                        <label class="settings-check">
                            <input type="checkbox" bind:checked={config.evtspike.enabled} />
                            Enable detector
                        </label>
                    </div>
                {/if}

                <!-- Display preferences -->
                <div class="modal-section">
                    <div class="section-header"><Monitor size={14} strokeWidth={2.5} /> Display</div>
                    <label class="display-toggle">
                        <input
                            type="checkbox"
                            checked={appState.reduceMotion}
                            onchange={(e) => (appState.reduceMotion = e.currentTarget.checked)}
                        />
                        <span class="display-toggle-label">
                            <span class="display-toggle-title">Reduce motion</span>
                            <span class="display-toggle-desc">
                                Disables UI animations, chart-overlay backdrop blur, and modal transitions. Flip this on
                                when running the dashboard over RDP to cut the compositor cost; local desktop sessions
                                should leave it off. Remembered per browser; initial value respects
                                <code>prefers-reduced-motion</code>.
                            </span>
                        </span>
                    </label>
                </div>

                <!-- Notification Targets -->
                <NotificationTargets bind:targets={config.notifications} bind:editTarget bind:editIdx bind:deleteIdx />

                <!-- Maintenance jobs — background housekeeping (aggregators,
                     retention, drift reconciliation). Formerly lived in the
                     footer; moved here so the idle dashboard stays quiet.
                     Collapsed by default — not what operators open Config for. -->
                <div class="modal-section maint-section">
                    <button
                        type="button"
                        class="maint-toggle"
                        onclick={() => (showMaintenance = !showMaintenance)}
                        aria-expanded={showMaintenance}
                    >
                        {#if showMaintenance}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
                        <Wrench size={14} strokeWidth={2.5} />
                        <span>Maintenance</span>
                    </button>
                    {#if showMaintenance}
                        <p class="section-hint">
                            Background jobs the service runs against the SQLite telemetry store. Fetched live while
                            this modal is open.
                        </p>
                        {#if maintenanceError}
                            <div class="maint-row maint-error">{maintenanceError}</div>
                        {:else if maintenanceLoading && orderedMaintJobs.length === 0}
                            <div class="maint-row maint-loading">Loading maintenance status…</div>
                        {:else if orderedMaintJobs.length === 0}
                            <div class="maint-row maint-loading">No jobs reported.</div>
                        {:else}
                            <ul class="maint-list">
                                {#each orderedMaintJobs as job (job.name)}
                                    <li class="maint-row">
                                        <span class="maint-glyph maint-{jobStateClass(job)}" aria-hidden="true"
                                            >{jobGlyph(job)}</span
                                        >
                                        <span class="maint-label">{jobLabel(job.name)}</span>
                                        <span class="maint-outcome maint-{jobStateClass(job)}">{job.outcome}</span>
                                        <span class="maint-meta">
                                            {#if job.finished}
                                                <span>{rel(job.finished, maintNowMs)}</span>
                                            {/if}
                                            {#if job.duration_ms != null}
                                                <span>· {job.duration_ms} ms</span>
                                            {/if}
                                            {#if job.rows_affected != null}
                                                <span>· {job.rows_affected.toLocaleString()} rows</span>
                                            {/if}
                                            {#if job.overdue}
                                                <span class="maint-warn">· overdue</span>
                                            {/if}
                                        </span>
                                        {#if job.outcome === 'failure' && job.reason}
                                            <div class="maint-reason">{job.reason}</div>
                                        {/if}
                                    </li>
                                {/each}
                            </ul>
                        {/if}
                    {/if}
                </div>

                <!-- Actions bar -->
                <div class="settings-actions-wrap">
                    <div class="settings-actions">
                        <button class="btn-brutal btn-test" onclick={sendTest} disabled={testing}>
                            <Play size={14} />
                            {testing ? 'Sending...' : 'Send Test'}
                        </button>
                        <div style="display:flex;gap:8px">
                            <button class="btn-brutal btn-save" onclick={save} disabled={saving || !dirty}>
                                <Save size={14} />
                                {saving ? 'Saving...' : 'Save'}
                            </button>
                            <button class="btn-brutal btn-secondary" onclick={requestClose}
                                ><X size={14} /> Close</button
                            >
                        </div>
                    </div>
                </div>
            {/if}
        </div>
    </div>
</div>

{#if editTarget !== null}
    <TargetEditModal
        target={editTarget}
        isNew={editIdx < 0}
        onsave={saveTarget}
        onclose={() => {
            editTarget = null;
        }}
    />
{/if}

{#if deleteIdx >= 0 && config}
    <TargetDeleteModal
        target={config.notifications[deleteIdx]}
        onconfirm={confirmDelete}
        oncancel={() => (deleteIdx = -1)}
    />
{/if}

{#if showConfirmClose}
    <ConfirmDialog
        title="Unsaved Changes"
        message="You have unsaved changes that will be lost if you close now."
        confirmLabel="Discard"
        cancelLabel="Keep Editing"
        saveLabel="Save & Close"
        onconfirm={() => {
            showConfirmClose = false;
            animateClose();
        }}
        oncancel={() => (showConfirmClose = false)}
        onsave={async () => {
            showConfirmClose = false;
            if (await save()) animateClose();
        }}
    />
{/if}

<style>
    .settings-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.55);
        z-index: 150;
        display: flex;
        justify-content: center;
        align-items: center;
        animation: modal-fade-in var(--anim-in-duration) var(--anim-timing);
    }
    .settings-overlay.closing {
        animation: modal-fade-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .settings-overlay.closing > .modal-wrap {
        animation: modal-card-out var(--anim-out-duration) var(--anim-timing) forwards;
    }
    .settings-overlay.sub-open > .modal-wrap {
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.15s ease-out;
    }
    .modal-wrap {
        position: relative;
        width: 860px;
        max-width: 94vw;
        transition: opacity 0.15s ease-out;
        animation: modal-card-in var(--anim-in-duration) var(--anim-timing);
    }
    .modal-badge {
        position: absolute;
        top: -16px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        align-items: center;
        justify-content: center;
        width: 36px;
        height: 36px;
        background: var(--color-accent);
        color: #fff;
        border: 3px solid var(--color-border);
        border-radius: 50%;
        box-shadow: 3px 3px 0 var(--color-shadow);
        z-index: 1;
    }
    .settings-modal {
        max-height: 90vh;
        background: var(--color-card);
        border: 4px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 10px 10px 0 var(--color-shadow);
        overflow-y: auto;
        padding: 32px 36px;
        transition: background 0.12s linear;
    }
    .modal-title {
        font-family: 'Fraunces', serif;
        font-size: 1.25rem;
        font-weight: 700;
        margin: 0;
        text-align: center;
        flex: 1;
    }
    .settings-title {
        margin-bottom: 24px;
        display: flex;
        align-items: center;
    }
    .settings-close {
        background: none;
        border: none;
        font-size: 1.4rem;
        cursor: pointer;
        color: var(--color-muted);
        padding: 4px 8px;
        display: flex;
        align-items: center;
    }
    .settings-close:hover {
        color: var(--color-fg);
    }
    .settings-group {
        margin-bottom: 22px;
    }
    .section-header {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.78rem;
        font-weight: 800;
        text-transform: uppercase;
        letter-spacing: 0.1em;
        color: var(--color-accent);
        margin-bottom: 0;
        padding: 6px 0 2px;
    }
    .modal-section {
        margin-top: 26px;
    }
    .section-hint {
        font-size: 0.75rem;
        color: var(--color-muted);
        margin: 4px 0 10px;
        line-height: 1.5;
    }

    /* Display preferences — reduce-motion toggle. */
    .display-toggle {
        display: flex;
        align-items: flex-start;
        gap: 12px;
        padding: 10px 12px;
        border: 2px solid var(--color-border);
        border-radius: var(--radius-default);
        background: var(--color-surface);
        cursor: pointer;
    }
    .display-toggle input[type='checkbox'] {
        margin-top: 3px;
        cursor: pointer;
        /* Make the native checkbox honour the theme — the browser UA
           otherwise paints it light-on-light (invisible check) or
           light-on-dark (bright white square floating in dark mode). */
        accent-color: var(--color-accent);
        width: 14px;
        height: 14px;
    }
    .display-toggle-label {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .display-toggle-title {
        font-size: 0.85rem;
        font-weight: 700;
        color: var(--color-fg);
    }
    .display-toggle-desc {
        font-size: 0.75rem;
        color: var(--color-muted);
        line-height: 1.5;
    }
    .display-toggle-desc code {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        background: color-mix(in srgb, var(--color-accent) 10%, transparent);
        padding: 1px 5px;
        border-radius: 3px;
    }

    /* Maintenance section — collapsed by default. Toggle mirrors the
       "Customize settings manually" pattern so operators recognise the
       chevron + label affordance. */
    .maint-toggle {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 6px 0 2px;
        background: none;
        border: none;
        cursor: pointer;
        color: var(--color-accent);
        font-family: inherit;
        font-size: 0.78rem;
        font-weight: 800;
        text-transform: uppercase;
        letter-spacing: 0.1em;
    }
    .maint-toggle:hover {
        color: var(--color-fg);
    }

    /* Maintenance job list — same vocabulary as the old footer widget, but
       laid out as rows so there's room for timing + row counts. */
    .maint-list {
        list-style: none;
        padding: 0;
        margin: 0;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .maint-row {
        display: grid;
        grid-template-columns: 22px 1fr auto;
        grid-template-rows: auto auto;
        column-gap: 10px;
        align-items: baseline;
        padding: 8px 12px;
        border: 2px solid var(--color-border);
        border-radius: var(--radius-default);
        background: var(--color-surface);
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.78rem;
    }
    .maint-row.maint-error,
    .maint-row.maint-loading {
        grid-template-columns: 1fr;
        color: var(--color-muted);
    }
    .maint-row.maint-error {
        color: var(--color-red);
    }
    .maint-glyph {
        grid-row: 1;
        grid-column: 1;
        font-weight: 800;
        text-align: center;
    }
    .maint-glyph.maint-ok {
        color: var(--color-green);
    }
    .maint-glyph.maint-warn {
        color: var(--color-amber);
    }
    .maint-glyph.maint-fail {
        color: var(--color-red);
    }
    .maint-glyph.maint-skip {
        color: var(--color-subtle);
    }
    .maint-label {
        grid-row: 1;
        grid-column: 2;
        font-weight: 700;
        color: var(--color-fg);
    }
    .maint-outcome {
        grid-row: 1;
        grid-column: 3;
        text-transform: uppercase;
        font-size: 0.68rem;
        letter-spacing: 0.08em;
        font-weight: 700;
    }
    .maint-outcome.maint-ok {
        color: var(--color-green);
    }
    .maint-outcome.maint-warn {
        color: var(--color-amber);
    }
    .maint-outcome.maint-fail {
        color: var(--color-red);
    }
    .maint-outcome.maint-skip {
        color: var(--color-subtle);
    }
    .maint-meta {
        grid-row: 2;
        grid-column: 2 / span 2;
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
        color: var(--color-muted);
        font-size: 0.72rem;
    }
    .maint-meta .maint-warn {
        color: var(--color-amber);
    }
    .maint-reason {
        grid-row: 3;
        grid-column: 2 / span 2;
        margin-top: 6px;
        color: var(--color-red);
        font-size: 0.72rem;
    }
    .subsection {
        border-left: 3px solid color-mix(in srgb, var(--color-accent) 30%, transparent);
        padding-left: 14px;
        margin-top: 18px;
        padding-bottom: 4px;
    }
    .settings-label {
        font-size: 0.75rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--color-accent);
        margin-bottom: 0;
    }
    .settings-check {
        display: flex;
        align-items: center;
        gap: 10px;
        margin-bottom: 10px;
        cursor: pointer;
        font-size: 0.85rem;
        font-weight: 600;
    }
    .settings-check input[type='checkbox'] {
        appearance: none;
        width: 18px;
        height: 18px;
        border: 2px solid var(--color-border);
        border-radius: 3px;
        background: var(--color-surface);
        cursor: pointer;
        position: relative;
    }
    .settings-check input[type='checkbox']:checked {
        background: var(--color-accent);
        border-color: var(--color-accent);
    }
    .settings-check input[type='checkbox']:checked::after {
        content: '';
        position: absolute;
        left: 4px;
        top: 1px;
        width: 5px;
        height: 9px;
        border: solid #fff;
        border-width: 0 2px 2px 0;
        transform: rotate(45deg);
    }
    .settings-num {
        width: 80px;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.8rem;
        padding: 8px 12px;
        background: var(--color-surface);
        color: var(--color-fg);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        outline: none;
    }
    .settings-num-label {
        font-size: 0.75rem;
        color: var(--color-muted);
    }
    .settings-hint {
        font-size: 0.72rem;
        color: var(--color-muted);
        font-weight: normal;
        line-height: 1.5;
        margin-bottom: 12px;
    }
    .settings-divider {
        height: 1px;
        background: var(--color-border);
        margin: 18px 0;
        opacity: 0.4;
    }
    .settings-cfg-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 20px;
        margin-bottom: 12px;
    }
    .threshold-row {
        display: flex;
        gap: 6px;
        align-items: center;
        margin-bottom: 6px;
    }
    .threshold-lbl {
        min-width: 28px;
    }
    .threshold-unit {
        min-width: 16px;
    }

    /* Fire preset cards */
    .fire-row {
        display: grid;
        grid-template-columns: 1fr 1fr 1fr;
        gap: 10px;
        margin-bottom: 10px;
    }
    .fire-card {
        position: relative;
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 3px;
        padding: 16px 12px 12px;
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: var(--radius-default);
        cursor: pointer;
        transition: all 0.1s linear;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow);
    }
    .fire-level-1 {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-green) 14%, var(--color-surface)) 0%,
            var(--color-surface) 70%
        );
    }
    .fire-level-2 {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-amber) 20%, var(--color-surface)) 0%,
            color-mix(in srgb, var(--color-amber) 6%, var(--color-surface)) 100%
        );
    }
    .fire-level-3 {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-red) 22%, var(--color-surface)) 0%,
            color-mix(in srgb, var(--color-red) 8%, var(--color-surface)) 100%
        );
    }
    .fire-card:hover {
        border-color: var(--color-accent);
        transform: translate(-2px, -2px);
        box-shadow: calc(var(--spacing-so) + 2px) calc(var(--spacing-so) + 2px) 0 var(--color-shadow);
    }
    .fire-card:active {
        transform: translate(2px, 2px);
        box-shadow: 1px 1px 0 var(--color-shadow);
    }
    .fire-card.active {
        border-color: var(--color-accent);
        border-width: 3px;
    }
    .fire-level-1.active {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-green) 22%, var(--color-card)) 0%,
            var(--color-card) 70%
        );
    }
    .fire-level-2.active {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-amber) 28%, var(--color-card)) 0%,
            color-mix(in srgb, var(--color-amber) 10%, var(--color-card)) 100%
        );
    }
    .fire-level-3.active {
        background: linear-gradient(
            145deg,
            color-mix(in srgb, var(--color-red) 32%, var(--color-card)) 0%,
            color-mix(in srgb, var(--color-red) 12%, var(--color-card)) 100%
        );
    }
    .fire-seal {
        position: absolute;
        top: -10px;
        right: -10px;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        color: #fff;
        border: 3px solid var(--color-border);
        border-radius: 50%;
        box-shadow: 2px 2px 0 var(--color-shadow);
        animation: seal-pop 0.12s linear;
    }
    .fire-seal-1 {
        background: var(--color-green);
    }
    .fire-seal-2 {
        background: var(--color-amber);
    }
    .fire-seal-3 {
        background: var(--color-red);
    }
    @keyframes seal-pop {
        from {
            transform: scale(0);
        }
        to {
            transform: scale(1);
        }
    }
    .fire-icon-wrap {
        display: flex;
        gap: 2px;
        line-height: 1;
        margin-bottom: 4px;
    }
    .fire-level-1 .fire-icon-wrap {
        color: var(--color-green);
    }
    .fire-level-2 .fire-icon-wrap {
        color: var(--color-amber);
    }
    .fire-level-3 .fire-icon-wrap {
        color: var(--color-red);
    }
    .fire-label {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.9rem;
        font-weight: 800;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        color: var(--color-fg);
    }
    .fire-tagline {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.68rem;
        font-weight: 600;
        font-style: italic;
        color: var(--color-muted);
        margin-bottom: 4px;
    }
    .fire-detail {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.64rem;
        color: var(--color-muted);
        text-align: center;
        line-height: 1.5;
        font-weight: 500;
    }
    .fire-custom-toggle {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        background: none;
        border: none;
        cursor: pointer;
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.72rem;
        font-weight: 600;
        color: var(--color-muted);
        padding: 4px 0;
        transition: color 0.1s linear;
    }
    .fire-custom-toggle:hover {
        color: var(--color-accent);
    }

    .settings-actions-wrap {
        margin-top: 16px;
        padding-top: 16px;
        border-top: 1px solid var(--color-surface);
    }
    .settings-actions {
        display: flex;
        align-items: center;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 12px;
    }
    .repeat-pills {
        display: flex;
        gap: 7px;
        margin-bottom: 12px;
        flex-wrap: wrap;
    }
    .repeat-pill {
        font-family: 'Work Sans', sans-serif;
        font-size: 0.72rem;
        font-weight: 600;
        padding: 5px 12px;
        background: var(--color-card);
        color: var(--color-muted);
        border: var(--spacing-bw) solid var(--color-border);
        border-radius: 20px;
        cursor: pointer;
        transition: all 0.1s linear;
    }
    .repeat-pill:hover {
        border-color: var(--color-accent);
        color: var(--color-fg);
    }
    .repeat-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
    }
    .repeat-pill--dashed {
        border-style: dashed;
        font-size: 0.7rem;
    }
    .gp-pill, .pctl-pill {
        font-size: 0.72rem;
        font-weight: 600;
        padding: 5px 12px;
        color: var(--color-muted);
        background: var(--color-card);
        box-shadow: 2px 2px 0 color-mix(in srgb, var(--color-accent) 20%, transparent);
    }
    .gp-pill:hover, .pctl-pill:hover {
        box-shadow: 3px 3px 0 color-mix(in srgb, var(--color-accent) 30%, transparent);
    }
    .gp-pill:active, .pctl-pill:active {
        box-shadow: 1px 1px 0 color-mix(in srgb, var(--color-accent) 15%, transparent);
    }
    .gp-pill.active, .pctl-pill.active {
        background: var(--color-accent);
        color: #fff;
        border-color: var(--color-accent);
        box-shadow: 2px 2px 0 color-mix(in srgb, var(--color-accent) 35%, transparent);
    }
    .gp-pill--dashed {
        border-style: dashed;
        font-size: 0.7rem;
    }
    .gp-pill--off {
        font-size: 0.68rem;
        letter-spacing: 0.06em;
        text-transform: uppercase;
    }
    .gp-pill--off.active {
        background: var(--color-subtle);
        color: var(--color-bg);
        border-color: var(--color-subtle);
    }
    .btn-save {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        background: var(--color-accent);
        color: #fff;
        padding: 10px 22px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.85rem;
        font-weight: 700;
    }
    .btn-save:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none !important;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow) !important;
    }
    .btn-test {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        background: var(--color-card);
        color: var(--color-fg);
        padding: 10px 22px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.85rem;
        font-weight: 700;
    }
    .btn-test:disabled {
        opacity: 0.5;
        cursor: not-allowed;
        transform: none !important;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow) !important;
    }
    .btn-secondary {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        background: var(--color-card);
        color: var(--color-fg);
        padding: 10px 22px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.85rem;
        font-weight: 700;
    }
</style>
