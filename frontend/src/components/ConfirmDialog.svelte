<script>
  import { OctagonAlert } from 'lucide-svelte';

  let { title = 'Are you sure?', message, confirmLabel = 'Discard', cancelLabel = 'Cancel', onconfirm, oncancel } = $props();

  let closing = $state(false);
  const CLOSE_MS = 150;

  function animateClose(cb) {
    closing = true;
    setTimeout(() => cb?.(), CLOSE_MS);
  }

  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') animateClose(oncancel); }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
<div class="confirm-overlay {closing ? 'closing' : ''}" onclick={(e) => e.target === e.currentTarget && animateClose(oncancel)} role="dialog" aria-modal="true" tabindex="-1">
  <div class="modal-wrap">
    <span class="confirm-badge"><OctagonAlert size={20} strokeWidth={2.5} /></span>
    <div class="confirm-modal">
    <h3 class="confirm-title serif">{title}</h3>
    <p class="confirm-msg">{message}</p>
    <div class="confirm-actions">
      <button class="btn-brutal confirm-cancel" onclick={() => animateClose(oncancel)}>{cancelLabel}</button>
      <button class="btn-brutal confirm-yes" onclick={() => animateClose(onconfirm)}>{confirmLabel}</button>
    </div>
    </div>
  </div>
</div>

<style>
  .confirm-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.55); z-index: 300; display: flex; align-items: center; justify-content: center; animation: modal-fade-in 0.15s ease-out; }
  .confirm-overlay.closing { animation: modal-fade-out 0.15s ease-in forwards; }
  .confirm-overlay.closing > .modal-wrap { animation: modal-card-out 0.15s ease-in forwards; }
  .modal-wrap { position: relative; max-width: 420px; width: 94vw; }
  .confirm-modal { background: var(--color-card); border: 4px solid var(--color-border); border-radius: var(--radius-default); box-shadow: 10px 10px 0 var(--color-shadow); padding: 28px 28px 24px; text-align: center; animation: modal-card-in 0.15s ease-out; }
  .confirm-badge {
    position: absolute;
    top: -16px; left: 50%; transform: translateX(-50%);
    display: flex; align-items: center; justify-content: center;
    width: 36px; height: 36px;
    background: var(--color-red); color: #fff;
    border: 3px solid var(--color-border);
    border-radius: 50%;
    box-shadow: 3px 3px 0 var(--color-shadow);
  }
  .confirm-title { font-family: 'Fraunces', serif; font-size: 1.25rem; font-weight: 700; margin: 8px 0 6px; letter-spacing: -0.01em; }
  .confirm-msg { font-size: 0.85rem; color: var(--color-muted); margin-bottom: 22px; line-height: 1.5; }
  .confirm-actions { display: flex; gap: 10px; justify-content: center; }
  .confirm-cancel { background: var(--color-card); color: var(--color-fg); padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; }
  .confirm-yes { background: var(--color-red); color: #fff; padding: 9px 22px; font-family: 'Work Sans', sans-serif; font-size: 0.85rem; font-weight: 700; border-color: var(--color-red); }
</style>
