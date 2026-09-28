/**
 * auth.svelte.js — Authentication state and actions for the DrainCtl dashboard.
 *
 * authState is a Svelte 5 reactive object shared across all components.
 * Null username means the user is not authenticated.
 */

import { clearSessionState } from './state.svelte.js';
import { SESSION_ACTION_STORAGE_KEY, stopSessionActionPolling } from './session-actions.svelte.js';

function applyAuthenticatedUser(data) {
    const wasAdmin = authState.isAdmin;
    authState.username = data.user ?? data.username ?? null;
    authState.isAdmin = data.is_admin === true;
    if (wasAdmin && !authState.isAdmin) clearSessionClientState();
    authState.error = null;
}

export function clearSessionClientState() {
    clearSessionState();
    stopSessionActionPolling();
    try {
        if (typeof window !== 'undefined') window.sessionStorage.removeItem(SESSION_ACTION_STORAGE_KEY);
    } catch {
        // Storage may be unavailable in a privacy-restricted browser context.
    }
}

function clearAuthenticatedUser() {
    authState.username = null;
    authState.isAdmin = false;
    clearSessionClientState();
}

/**
 * Shared authentication state. Null username = not logged in.
 * @type {{ username: string|null, isAdmin: boolean, loading: boolean, error: string|null }}
 */
export const authState = $state({
    username: null,
    isAdmin: false,
    /** True while a session check or auth operation is in flight. Initialised
     *  true so the loading indicator shows on app load while checkSession runs. */
    loading: true,
    error: null,
});

/**
 * Check for an existing valid session on page load.
 * Calls GET /api/v1/me — returns username if the session cookie is still valid,
 * plain 401 otherwise. No Negotiate handshake, no Windows popup.
 * On success sets authState.username; on failure leaves username null so the
 * login page is shown immediately.
 */
export async function checkSession() {
    authState.loading = true;
    try {
        const res = await fetch('/api/v1/me', { credentials: 'include' });
        if (res.ok) {
            const data = await res.json();
            applyAuthenticatedUser(data);
        } else {
            clearAuthenticatedUser();
        }
        // 401 = no valid session → show login, no error state needed
    } catch {
        clearAuthenticatedUser();
    } finally {
        authState.loading = false;
    }
}

/**
 * Initiate Windows Negotiate (SSPI/Kerberos) sign-in.
 * Only called when the user explicitly clicks "Sign in with Windows".
 * On success sets authState.username; on failure sets authState.error.
 */
export async function signInWithWindows() {
    authState.loading = true;
    authState.error = null;
    try {
        const res = await fetch('/api/v1/auth/negotiate', {
            method: 'POST',
            credentials: 'include',
        });
        if (res.ok) {
            const data = await res.json();
            applyAuthenticatedUser(data);
        } else {
            authState.error = 'windows_auth_failed';
        }
    } catch {
        authState.error = 'windows_auth_failed';
    } finally {
        authState.loading = false;
    }
}

/**
 * Submit explicit username/password credentials.
 * Sets authState.username on success; sets authState.error on failure.
 * @param {string} username
 * @param {string} password
 */
export async function loginWithCredentials(username, password) {
    try {
        const res = await fetch('/api/v1/auth/login', {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password }),
        });
        if (res.ok) {
            const data = await res.json();
            applyAuthenticatedUser(data);
        } else if (res.status === 401 || res.status === 403) {
            try {
                const body = await res.json();
                authState.error = body.error ?? 'Wrong username or password.';
            } catch {
                authState.error = 'Wrong username or password.';
            }
        } else {
            authState.error = 'Cannot reach the server — check your connection.';
        }
    } catch {
        authState.error = 'Cannot reach the server — check your connection.';
    }
}

/**
 * Log out the current user.
 */
export async function logout() {
    try {
        await fetch('/api/v1/auth/logout', {
            method: 'POST',
            credentials: 'include',
        });
    } catch {
        // Ignore network errors — always complete the client-side logout.
    } finally {
        clearAuthenticatedUser();
        authState.error = null;
        authState.loading = false;
    }
}
