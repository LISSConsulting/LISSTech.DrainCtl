/**
 * auth.svelte.js — Authentication state and actions for the DrainCtl dashboard.
 *
 * authState is a Svelte 5 reactive object shared across all components.
 * Null username means the user is not authenticated.
 */

/**
 * Shared authentication state. Null username = not logged in.
 * @type {{ username: string|null, loading: boolean, error: string|null, skipProbe: boolean }}
 */
export const authState = $state({
    username: null,
    /** True while an auth operation is in flight. Initialised true so the
     *  loading indicator shows immediately on app load before the probe runs. */
    loading: true,
    error: null,
    /** Set after explicit logout to prevent App.svelte from re-probing Negotiate. */
    skipProbe: false,
});

/**
 * Attempt silent Windows Negotiate (SSPI) auto-login.
 * Called on app load. On success sets authState.username; on failure sets
 * authState.error = 'auto_login_failed'.
 */
export async function probeNegotiate() {
    authState.loading = true;
    try {
        const res = await fetch('/api/v1/auth/negotiate', {
            method: 'POST',
            credentials: 'include',
        });
        if (res.ok) {
            const data = await res.json();
            authState.username = data.username;
            authState.error = null;
        } else {
            authState.error = 'auto_login_failed';
        }
    } catch {
        authState.error = 'auto_login_failed';
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
            authState.username = data.username;
            authState.error = null;
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
 * Sets skipProbe = true so App.svelte shows the login form without re-probing.
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
        authState.username = null;
        authState.error = null;
        authState.loading = false;
        authState.skipProbe = true;
    }
}
