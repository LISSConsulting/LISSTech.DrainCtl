<script>
    import { createInvestigation, fetchInvestigationHistory, fetchSessionDrops, fetchSessionDropDetail } from '../lib/api.js';
    import { appState, setSessionDropSummaries, setSessionDropDetail, upsertInvestigationAttempt } from '../lib/state.svelte.js';
    import InvestigationPanel from './InvestigationPanel.svelte';
    let loading = $state(true);
    let error = $state('');
    let selectedId = $state(null);
    let selectedAttemptId = $state(null);
    let detail = $derived(selectedId ? appState.sessionDropDetails.get(selectedId) : null);
    $effect(() => { fetchSessionDrops().then(v => setSessionDropSummaries(v.items)).catch(e => error = e.message).finally(() => loading = false); });
    async function refreshSelected(id) {
        const [sourceDetail, history] = await Promise.all([fetchSessionDropDetail(id), fetchInvestigationHistory('session_drop', id)]);
        setSessionDropDetail({ ...sourceDetail, attempts: history.attempts });
        history.attempts.forEach(upsertInvestigationAttempt);
    }
    async function select(id) {
        selectedId = id;
        selectedAttemptId = null;
        error = '';
        try { await refreshSelected(id); } catch (e) { error = e.message; }
    }
    async function create() {
        if (!detail?.source?.investigation_eligible) return;
        error = '';
        try {
            const attempt = await createInvestigation('session_drop', detail.source.id);
            upsertInvestigationAttempt(attempt);
            await select(detail.source.id);
            selectedAttemptId = attempt.attempt_id;
        } catch (e) {
            error = e?.code === 'queue_full' ? 'queue full' : e?.code === 'attempt_limit_reached' ? 'attempt limit reached' : e.message;
        }
    }
</script>
<section class="panel" aria-label="Session drop anomalies">
    <h3>Session-drop anomalies</h3>
    {#if loading}<p>Loading session drops…</p>{:else if error}<p class="error">{error}</p>{:else if appState.sessionDropSummaries.length === 0}<p>No retained session-drop anomalies.</p>{:else}<ul>{#each appState.sessionDropSummaries as source (source.id)}<li><button type="button" onclick={() => select(source.id)}>{source.registered_host}</button> · {source.classification} · {source.confirmation_count}/{source.confirmation_window_size} confirmation · {source.confirmed_at}</li>{/each}</ul>{/if}
    {#if detail}<section class="detail"><h4>{detail.source.registered_host}</h4><p>Classification: {detail.source.classification}; investigation eligible: {detail.source.investigation_eligible ? 'yes' : 'no'}.</p><p>Observed {detail.source.observed_total_sessions}; reference {detail.source.reference_total_sessions}; expected {detail.source.expected_total_sessions}; loss {detail.source.absolute_loss_sessions} ({detail.source.relative_loss}); tail probability {detail.source.tail_probability}.</p><p>Baseline: {detail.source.baseline_model_version}, {detail.source.baseline_scope}, slot {detail.source.slot_index ?? 'not applicable'}, mature days {detail.source.slot_mature_days}.</p><p>Confirmation: {detail.source.confirmation_flags.map(flag => flag ? 'candidate' : 'normal').join(', ')} ({detail.source.confirmation_count}/{detail.source.confirmation_window_size}); fresh context {detail.source.freshness_context}; drain context {detail.source.drain_context}.</p><p>Confirmation started: {detail.source.confirmation_started_at}; ended: {detail.source.confirmation_ended_at}.</p><p>Retained attempt lineage: {detail.attempts.length ? '' : 'none'}{#each detail.attempts as attempt (attempt.attempt_id)}<button type="button" onclick={() => selectedAttemptId = attempt.attempt_id}>#{attempt.attempt_number} {attempt.state} ({attempt.terminal_reason || 'no terminal reason'})</button>{/each}</p>{#if detail.source.investigation_eligible && detail.attempts.length === 0}<button type="button" onclick={create}>Create investigation</button>{/if}</section>{/if}
    {#if selectedAttemptId}<InvestigationPanel attemptId={selectedAttemptId} onclose={() => selectedAttemptId = null} onretry={() => refreshSelected(selectedId).catch((e) => error = e.message)} />{/if}
</section>
<style>.panel{border:2px solid var(--color-border);background:var(--color-card);padding:14px;margin:12px 0;font:12px 'JetBrains Mono',monospace}.detail{border-top:1px solid var(--color-border);margin-top:10px;padding-top:10px}.error{color:var(--color-red)}button{font:inherit;margin-right:6px}</style>
