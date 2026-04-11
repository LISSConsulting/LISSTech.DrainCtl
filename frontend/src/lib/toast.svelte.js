/**
 * toast.svelte.js — Site-wide toast notification store using Svelte 5 runes.
 *
 * Usage:
 *   import { toast } from '../lib/toast.svelte.js';
 *   toast.ok('Settings saved');
 *   toast.err('Save failed: network error');
 *   toast.info('Test notification sent');
 */

const DURATION = { ok: 4000, err: 6000, info: 4000 };

let id = 0;

/** @type {{ id: number, msg: string, type: 'ok'|'err'|'info', dismissing: boolean }[]} */
let items = $state([]);

/**
 * Show a toast notification.
 * @param {string} msg
 * @param {'ok'|'err'|'info'} type
 */
function show(msg, type = 'info') {
  const tid = ++id;
  items = [...items, { id: tid, msg, type, dismissing: false }];
  setTimeout(() => dismiss(tid), DURATION[type] ?? 4000);
}

/**
 * Begin dismiss animation, then remove after animation completes.
 * @param {number} tid
 */
function dismiss(tid) {
  items = items.map(t => t.id === tid ? { ...t, dismissing: true } : t);
  setTimeout(() => { items = items.filter(t => t.id !== tid); }, 300);
}

export const toast = {
  get items() { return items; },
  ok:   (/** @type {string} */ msg) => show(msg, 'ok'),
  err:  (/** @type {string} */ msg) => show(msg, 'err'),
  info: (/** @type {string} */ msg) => show(msg, 'info'),
  dismiss,
};
