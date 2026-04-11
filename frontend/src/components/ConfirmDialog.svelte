<script>
  import { TriangleAlert } from 'lucide-svelte';

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
    <div class="confirm-icon"><TriangleAlert size={28} /></div>
    <h3 class="confirm-title serif">{title}</h3>
    <p class="confirm-msg">{message}</p>
    <div class="confirm-actions">
      <button class="btn-brutal confirm-cancel" onclick={oncancel}>{cancelLabel}</button>
      <button class="btn-brutal confirm-yes" onclick={onconfirm}>{confirmLabel}</button>
    </div>
  </div>
</div>

<style>
  .confirm-overlay { position: fixed; inset: 0; background: rgba(45,26,26,0.5); z-index: 300; display: flex; align-items: center; justify-content: center; }
  .confirm-modal { background: var(--color-card); border: var(--spacing-bw) solid var(--color-border); border-radius: var(--radius-default); box-shadow: 8px 8px 0 var(--color-shadow); max-width: 400px; width: 94vw; padding: 28px; text-align: center; }
  .confirm-icon { color: var(--color-amber); margin-bottom: 12px; }
  .confirm-title { font-family: 'Fraunces', serif; font-size: 1.1rem; margin-bottom: 8px; }
  .confirm-msg { font-size: 0.85rem; color: var(--color-muted); margin-bottom: 20px; line-height: 1.5; }
  .confirm-actions { display: flex; gap: 10px; justify-content: center; }
  .confirm-cancel { background: var(--color-card); color: var(--color-fg); padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .confirm-yes { background: var(--color-red); color: #fff; padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; border-color: var(--color-red); }
</style>
