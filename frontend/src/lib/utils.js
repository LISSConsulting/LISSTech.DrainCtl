/**
 * utils.js — Shared display-formatting helpers.
 *
 * Centralised here so ServerTable, ServerDetail, HistoryModal, and EventLog
 * all produce identical output without duplicating the same functions.
 */

/**
 * Human-readable relative timestamp (e.g. "3m ago").
 *
 * Accepts an optional `_now` argument so that callers with a reactive clock
 * can pass it explicitly and have Svelte track the time dependency:
 *
 *   rel(server.last_seen, now)   // re-evaluates every time `now` changes
 *   rel(server.registered_at)    // snapshot at call time (fine for non-live uses)
 *
 * @param {string|null|undefined} iso
 * @param {number} [_now]
 * @returns {string}
 */
export function rel(iso, _now = Date.now()) {
  if (!iso) return 'never';
  const d = new Date(iso);
  if (isNaN(d)) return 'never';
  const s = Math.floor((_now - d.getTime()) / 1000);
  if (s < 0) return 'now';
  if (s < 60) return s + 's ago';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm ago';
  const h = Math.floor(m / 60);
  if (h < 24) return h + 'h ago';
  return Math.floor(h / 24) + 'd ago';
}

/**
 * Format an ISO timestamp for compact display in history and event log panels.
 * Output: "Apr 11, 14:32:01" (always en-US 24-hour locale).
 * @param {string} iso
 * @returns {string}
 */
export function formatTs(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return d.toLocaleString('en-US', {
    month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
    hour12: false,
  });
}

/**
 * Format a duration in seconds to a human-readable string.
 * Examples: "45s", "3m 12s", "2h 5m"
 * @param {number|null|undefined} sec
 * @returns {string}
 */
export function dur(sec) {
  if (sec == null) return '—';
  const s = Math.floor(sec);
  if (s < 60) return s + 's';
  const m = Math.floor(s / 60);
  if (m < 60) return m + 'm ' + (s % 60) + 's';
  const h = Math.floor(m / 60);
  return h + 'h ' + (m % 60) + 'm';
}
