/**
 * theme.js — Light/dark theme management.
 *
 * Theme is persisted to localStorage under the key 'drainctl-theme'.
 * The active theme is reflected on document.documentElement.dataset.theme,
 * which Tailwind CSS v4 can target via the [data-theme] attribute selector.
 */

const STORAGE_KEY = 'drainctl-theme';

// ---------------------------------------------------------------------------
// Reactive state (Svelte 5 rune — valid in .js modules compiled by Vite/Svelte)
// ---------------------------------------------------------------------------

/** @type {'light'|'dark'} */
export let currentTheme = $state('light');

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Read the OS preference.
 * @returns {'light'|'dark'}
 */
function systemPreference() {
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return 'dark';
  }
  return 'light';
}

/**
 * Apply a theme to the DOM and update the reactive variable.
 * @param {'light'|'dark'} theme
 */
function applyTheme(theme) {
  currentTheme = theme;
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.theme = theme;
  }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Initialise the theme on page load.
 *
 * Priority order:
 *   1. localStorage value (user's explicit previous choice)
 *   2. OS prefers-color-scheme
 *   3. 'light' fallback
 *
 * Call once from your root component's onMount or main entry point.
 */
export function initTheme() {
  /** @type {'light'|'dark'} */
  let resolved = 'light';

  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === 'light' || stored === 'dark') {
      resolved = stored;
    } else {
      resolved = systemPreference();
    }
  } else {
    resolved = systemPreference();
  }

  applyTheme(resolved);
}

/**
 * Toggle between light and dark, persist to localStorage, and update the DOM.
 */
export function toggleTheme() {
  const next = currentTheme === 'dark' ? 'light' : 'dark';
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, next);
  }
  applyTheme(next);
}
