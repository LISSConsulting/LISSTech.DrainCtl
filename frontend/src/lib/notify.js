/**
 * notify.js — Shared constants for notification targets and triggers.
 *
 * Centralised here so NotificationTargets.svelte and TargetEditModal.svelte
 * stay in sync without duplicating the same objects in both files.
 */

/** @type {string[]} All trigger identifiers in display order. */
export const ALL_TRIGGERS = [
  'drain_on', 'drain_off', 'grace_entered', 'alert', 'healthy',
  'session_warning',
  'cpu_warning', 'cpu_critical',
  'memory_warning', 'memory_critical',
  'input_delay_warning', 'input_delay_critical',
];

/** Human-readable labels for each trigger key. */
export const TRIGGER_LABELS = {
  drain_on:              'Drain On',
  drain_off:             'Drain Off',
  grace_entered:         'Grace',
  alert:                 'Alert',
  healthy:               'Healthy',
  session_warning:       'Sessions',
  cpu_warning:           'CPU Warn',
  cpu_critical:          'CPU Crit',
  memory_warning:        'Mem Warn',
  memory_critical:       'Mem Crit',
  input_delay_warning:   'Delay Warn',
  input_delay_critical:  'Delay Crit',
};

/** Repeat interval options (minutes). */
export const REPEAT_OPTIONS = [0, 15, 60, 240, 480];

/** Human-readable labels for repeat intervals (minutes → label). */
export const REPEAT_MAP = { 0: 'Once', 15: '15m', 60: '1h', 240: '4h', 480: '8h' };

/**
 * Returns the display label for a repeat interval in minutes.
 * @param {number} m
 * @returns {string}
 */
export function repeatLabel(m) {
  return REPEAT_MAP[m] != null ? REPEAT_MAP[m] : m + 'm';
}
