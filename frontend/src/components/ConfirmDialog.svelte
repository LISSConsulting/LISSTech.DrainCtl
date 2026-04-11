<script>
  import { OctagonAlert } from 'lucide-svelte';

  let { title = 'Are you sure?', message, confirmLabel = 'Discard', cancelLabel = 'Cancel', onconfirm, oncancel } = $props();

  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') oncancel?.(); }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="confirm-overlay" onclick={(e) => e.target === e.currentTarget && oncancel?.()} role="dialog" aria-modal="true" tabindex="-1">
  <div class="confirm-modal">
    <div class="confirm-header">
      <span class="confirm-icon"><OctagonAlert size={22} strokeWidth={2.5} /></span>
      <h3 class="confirm-title serif">{title}</h3>
    </div>
    <p class="confirm-msg">{message}</p>
    <div class="confirm-actions">
      <button class="btn-brutal confirm-cancel" onclick={oncancel}>{cancelLabel}</button>
      <button class="btn-brutal confirm-yes" onclick={onconfirm}>{confirmLabel}</button>
    </div>
  </div>
</div>

<style>
  .confirm-overlay { position: fixed; inset: 0; background: rgba(45,26,26,0.5); z-index: 300; display: flex; align-items: center; justify-content: center; }
  .confirm-modal { background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); max-width: 420px; width: 94vw; padding: 24px 28px; }
  .confirm-header { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
  .confirm-icon { color: var(--color-red); line-height: 1; flex-shrink: 0; display: flex; }
  .confirm-title { font-family: 'Fraunces', serif; font-size: 1.1rem; margin: 0; }
  .confirm-msg { font-size: 0.85rem; color: var(--color-muted); margin-bottom: 20px; line-height: 1.5; padding-left: 32px; }
  .confirm-actions { display: flex; gap: 10px; justify-content: flex-end; }
  .confirm-cancel { background: var(--color-card); color: var(--color-fg); padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .confirm-yes { background: var(--color-red); color: #fff; padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; border-color: var(--color-red); }
</style>
