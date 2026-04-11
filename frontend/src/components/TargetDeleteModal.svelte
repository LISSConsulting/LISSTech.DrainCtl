<script>
  let { target, onconfirm, oncancel } = $props();

  let dest = $derived(target?.type === 'email' ? (target?.to || []).join(', ') : (target?.url || ''));

  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') oncancel?.(); }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="tgt-del-overlay" onclick={(e) => e.target === e.currentTarget && oncancel?.()} role="dialog" aria-modal="true" tabindex="-1">
  <div class="tgt-del-modal">
    <h3 class="tgt-del-title serif">Delete Notification Target</h3>
    <p>Are you sure you want to delete this notification target?</p>
    <p style="font-family:'JetBrains Mono',monospace;font-size:12px;color:var(--color-muted);margin-top:8px">{dest}</p>
    <div class="btn-row">
      <button class="btn-brutal btn-cancel" onclick={oncancel}>Cancel</button>
      <button class="btn-brutal btn-danger" onclick={onconfirm}>Delete</button>
    </div>
  </div>
</div>

<style>
  .tgt-del-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.55); z-index: 210; display: flex; align-items: center; justify-content: center; }
  .tgt-del-modal { background: var(--color-card); border: 4px solid var(--color-border); border-radius: var(--radius-default); box-shadow: 10px 10px 0 var(--color-shadow); max-width: 400px; width: 94vw; padding: 24px; text-align: center; }
  .tgt-del-title { font-family: 'Fraunces', serif; font-size: 18px; margin-bottom: 16px; }
  p { font-size: 14px; margin-bottom: 8px; line-height: 1.5; }
  .btn-row { display: flex; gap: 8px; justify-content: center; margin-top: 16px; }
  .btn-cancel { background: var(--color-card); color: var(--color-fg); padding: 8px 20px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .btn-danger { background: var(--color-red); color: #fff; padding: 8px 20px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; border-color: var(--color-red); }
</style>
