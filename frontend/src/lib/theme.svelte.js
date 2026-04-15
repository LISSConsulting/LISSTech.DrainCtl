/**
 * theme.js — Light/dark/system theme management.
 *
 * Theme preference is persisted to localStorage under 'drainctl-theme'.
 * Values: 'light', 'dark', or 'system' (follows OS preference).
 * The resolved theme is applied to document.documentElement.dataset.theme.
 */

const STORAGE_KEY = 'drainctl-theme';

// ---------------------------------------------------------------------------
// Reactive state
// ---------------------------------------------------------------------------

export const theme = $state({
    /** @type {'light'|'dark'|'system'} */
    preference: 'system',
    /** @type {'light'|'dark'} */
    resolved: 'light',
});

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
 * Resolve and apply the theme to the DOM.
 * @param {'light'|'dark'|'system'} preference
 */
function applyTheme(preference) {
    theme.preference = preference;
    theme.resolved = preference === 'system' ? systemPreference() : preference;
    if (typeof document !== 'undefined') {
        document.documentElement.dataset.theme = theme.resolved;
    }
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Initialise the theme on page load.
 * Priority: localStorage → 'system' (which reads OS preference).
 */
export function initTheme() {
    /** @type {'light'|'dark'|'system'} */
    let pref = 'system';

    if (typeof localStorage !== 'undefined') {
        const stored = localStorage.getItem(STORAGE_KEY);
        if (stored === 'light' || stored === 'dark' || stored === 'system') {
            pref = stored;
        }
    }

    applyTheme(pref);

    // Listen for OS preference changes when in system mode.
    if (typeof window !== 'undefined') {
        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
            if (theme.preference === 'system') {
                applyTheme('system');
            }
        });
    }
}

/**
 * Cycle through light → dark → system.
 */
export function toggleTheme() {
    const order = ['light', 'dark', 'system'];
    const idx = order.indexOf(theme.preference);
    const next = order[(idx + 1) % order.length];
    if (typeof localStorage !== 'undefined') {
        localStorage.setItem(STORAGE_KEY, next);
    }
    applyTheme(next);
}
