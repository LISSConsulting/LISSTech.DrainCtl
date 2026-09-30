<script>
    import { fetchInvestigationDetail, retryInvestigation, ApiError } from '../lib/api.js';
    import { appState, setInvestigationDetail, upsertInvestigationAttempt } from '../lib/state.svelte.js';

    let { attemptId, onclose = undefined, onretry = undefined } = $props();
    let activeAttemptId = $state(null);
    let lastPropAttemptId = $state(null);
    let loading = $state(true);
    let error = $state('');
    let retrying = $state(false);
    let detail = $derived(activeAttemptId ? appState.investigationDetails.get(activeAttemptId) ?? null : null);
    let attempt = $derived(activeAttemptId ? detail?.attempt ?? appState.investigationAttempts.get(activeAttemptId) ?? null : null);
    let canRetry = $derived(['failed', 'insufficient_evidence'].includes(attempt?.state));

    $effect(() => {
        if (attemptId !== lastPropAttemptId) {
            activeAttemptId = attemptId;
            lastPropAttemptId = attemptId;
        }
    });
    $effect(() => {
        if (!activeAttemptId) return;
        loading = true;
        error = '';
        fetchInvestigationDetail(activeAttemptId).then((value) => { setInvestigationDetail(value); error = ''; }).catch((e) => error = e.message).finally(() => loading = false);
    });

    // SSE summaries intentionally contain no report/provenance. Once an open
    // queued/running attempt becomes terminal, resolve its immutable REST detail.
    $effect(() => {
        const live = appState.investigationAttempts.get(activeAttemptId);
        if (!live || !['completed', 'insufficient_evidence', 'failed'].includes(live.state) || detail?.attempt?.state === live.state) return;
        fetchInvestigationDetail(activeAttemptId).then((value) => { setInvestigationDetail(value); error = ''; }).catch((e) => error = e.message);
    });

    async function retry() {
        if (!canRetry || retrying) return;
        retrying = true;
        error = '';
        try {
            const retry = await retryInvestigation(activeAttemptId, attempt.source);
            upsertInvestigationAttempt(retry);
            activeAttemptId = retry.attempt_id;
            await onretry?.(retry);
        }
        catch (e) { error = e instanceof ApiError && ['queue_full', 'attempt_limit_reached'].includes(e.code) ? e.code.replace('_', ' ') : e.message; }
        finally { retrying = false; }
    }
    const list = (values) => Array.isArray(values) ? values.join(', ') || 'none' : 'none';
</script>

<section class="panel" aria-label="Investigation">
    <header><h3>Investigation</h3>{#if onclose}<button type="button" onclick={onclose}>Close</button>{/if}</header>
    {#if loading}<p>Loading investigation…</p>
    {:else if error}<p class="error">{error}</p>
    {:else if attempt}
        <div class="facts">
            <div><b>State</b> {attempt.state}</div><div><b>Terminal reason</b> {attempt.terminal_reason || 'none'}</div>
            <div><b>Created</b> {attempt.created_at}</div><div><b>Started</b> {attempt.started_at ?? '—'}</div>
            <div><b>Send authorized</b> {attempt.send_authorized_at ?? '—'}</div><div><b>Send completed</b> {attempt.send_completed_at ?? '—'}</div>
            <div><b>Completed</b> {attempt.completed_at ?? '—'}</div><div><b>Omissions</b> {list(attempt.omission_codes)}</div>
        </div>
        {#if detail?.attempt?.evidence}
            {@const evidence = detail.attempt.evidence}
            <section><h4>Deterministic evidence</h4><p>Version {evidence.version}; snapshot {evidence.snapshot_kind}; captured {evidence.snapshot_at}.</p><p>Window: {evidence.window_start} to {evidence.window_end}.</p><p>Fact IDs: {list(evidence.fact_ids)}. Omission codes: {list(evidence.omission_codes)}.</p></section>
        {/if}
        {#if detail?.attempt?.provenance}
            {@const provenance = detail.attempt.provenance}
            <section><h4>Closed local provenance</h4><div class="facts"><div><b>Profile</b> {provenance.provider_profile}</div><div><b>Endpoint</b> {provenance.provider_endpoint}</div><div><b>Requested model</b> {provenance.requested_model}</div><div><b>Response format</b> {provenance.response_format}</div><div><b>Store</b> {String(provenance.store)}</div><div><b>Validation</b> {provenance.validation_outcome}</div><div><b>Request headers</b> {provenance.request_header_bytes} bytes</div><div><b>Request body</b> {provenance.request_body_bytes} bytes</div><div><b>Response headers</b> {provenance.response_header_bytes} bytes</div><div><b>Response body</b> {provenance.response_body_bytes} bytes</div></div><p>Send authorization ({provenance.send_authorized_at}) means transmission may have begun; it does not prove provider receipt. Send completion ({provenance.send_completed_at}) means only the local HTTP exchange returned.</p></section>
        {/if}
        {#if detail?.attempt?.result}
            {@const report = detail.attempt.result}
            <section><h4>Untrusted model guidance</h4><p><b>Assessment</b> {report.overall_assessment}; <b>Sufficiency</b> {report.evidence_sufficiency}; <b>Human review</b> {report.human_review_required ? 'required' : 'not required'}</p><div class="untrusted"><b>Untrusted model summary</b><p>{report.summary.text}</p><small>Facts: {list(report.summary.fact_ids)}</small></div>
            {#each report.hypotheses as hypothesis (hypothesis.rank)}<div class="untrusted"><b>Untrusted model hypothesis</b><p>{hypothesis.text}</p><small>Rank {hypothesis.rank}; {hypothesis.confidence}; supporting facts: {list(hypothesis.supporting_fact_ids)}; contradicting facts: {list(hypothesis.contradicting_fact_ids)}</small></div>{/each}
            {#each report.missing_evidence as missing, index}<div class="untrusted"><b>Untrusted model missing evidence {index + 1}</b><p>{missing.text}</p><small>Category: {missing.category}; related facts: {list(missing.related_fact_ids)}</small></div>{/each}
            {#each report.recommended_diagnostic_checks as check (check.rank)}<div class="untrusted"><b>Untrusted model-suggested diagnostic check</b><p>{check.text}</p><small>Rank {check.rank}; {check.check_type}; facts: {list(check.fact_ids)}; related hypotheses: {list(check.hypothesis_ranks)}</small></div>{/each}
            </section>
        {/if}
        {#if canRetry}<button type="button" onclick={retry} disabled={retrying}>{retrying ? 'Retrying…' : 'Create retry'}</button>{/if}
    {:else}<p>No retained attempt is available.</p>{/if}
</section>

<style>
.panel{border:2px solid var(--color-border);background:var(--color-card);padding:14px;margin:12px 0;font:12px 'JetBrains Mono',monospace}.panel header{display:flex;justify-content:space-between;align-items:center}.panel h3,.panel h4{margin:12px 0 8px}.facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:6px}.untrusted{border-left:3px solid var(--color-amber);padding-left:8px;margin:10px 0}.error{color:var(--color-red)}button{font:inherit}
</style>
