<script>
    import { toast } from '../lib/toast.svelte.js';
    import { toggleTheme, theme } from '../lib/theme.svelte.js';
    import { authState, signInWithWindows, loginWithCredentials } from '../lib/auth.svelte.js';
    import { KeyRound, ShieldCheck } from 'lucide-svelte';

    const isDark = $derived(theme.current === 'dark');

    let username = $state('');
    let password = $state('');
    let submitting = $state(false);
    let ssoLoading = $state(false);
    let ssoError = $state('');

    async function handleWindowsSignIn() {
        ssoLoading = true;
        ssoError = '';
        try {
            await signInWithWindows();
            if (authState.error === 'windows_auth_failed') {
                ssoError = 'Windows sign-in failed — you may not be on a domain-joined machine, or your account lacks access.';
            }
        } finally {
            ssoLoading = false;
        }
    }

    async function handleSubmit(e) {
        e.preventDefault();
        const u = username.trim();
        if (!u) {
            toast.err('Enter your username.');
            return;
        }
        if (!u.includes('\\') && !u.includes('@')) {
            toast.err('Include a domain — use DOMAIN\\username or username@domain.');
            return;
        }
        if (!password) {
            toast.err('Enter your password.');
            return;
        }
        submitting = true;
        try {
            await loginWithCredentials(u, password);
            if (authState.error && authState.error !== 'windows_auth_failed') {
                toast.err(authState.error);
            }
        } finally {
            password = '';
            submitting = false;
        }
    }
</script>

<!-- ── Nav — same structure as Nav.svelte, no server counters ── -->
<nav class="nav">
    <div class="nav-in">
        <div class="nav-brand">
            <img src="/logo.png" alt="" width="28" height="28" />
            DRAINCTL
        </div>
        <div class="nav-right">
            <button
                class="btn-theme btn-brutal"
                onclick={toggleTheme}
                aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}>{isDark ? '☀' : '☽'}</button
            >
        </div>
    </div>
</nav>

<!-- ── Login page body ─────────────────────────────────────── -->
<div class="login-page">
    <div class="modal-wrap">
        <!-- Floating badge icon — mirrors ConfigModal pattern exactly -->
        <span class="modal-badge" aria-hidden="true">
            <KeyRound size={20} />
        </span>

        <!-- Login card — modal treatment: 4px border, 10px brutal shadow -->
        <div class="login-modal">
            <div class="modal-header">
                <h1 class="modal-title serif">Sign In</h1>
            </div>

            <p class="modal-sub">Access the DrainCtl dashboard with your network account.</p>

            <!-- ── Windows SSO ─────────────────────────────────────── -->
            <div class="sso-section">
                <button
                    type="button"
                    class="btn-brutal btn-sso"
                    onclick={handleWindowsSignIn}
                    disabled={ssoLoading || submitting}
                >
                    <ShieldCheck size={16} />
                    {ssoLoading ? 'Signing in…' : 'Sign in with Windows'}
                </button>

                {#if ssoError}
                    <p class="sso-error">{ssoError}</p>
                {/if}
            </div>

            <!-- ── Divider ─────────────────────────────────────────── -->
            <div class="or-divider">
                <span class="or-line"></span>
                <span class="or-label">or</span>
                <span class="or-line"></span>
            </div>

            <!-- ── Credentials form ───────────────────────────────── -->
            <form onsubmit={handleSubmit} novalidate>
                <div class="field">
                    <label class="settings-label" for="username">Username</label>
                    <input
                        class="settings-input"
                        type="text"
                        id="username"
                        name="username"
                        placeholder="DOMAIN\username"
                        autocomplete="username"
                        spellcheck="false"
                        autocapitalize="none"
                        bind:value={username}
                    />
                    <span class="field-hint">Format: DOMAIN\username or username@domain</span>
                </div>

                <div class="field">
                    <label class="settings-label" for="password">Password</label>
                    <input
                        class="settings-input"
                        type="password"
                        id="password"
                        name="password"
                        placeholder="••••••••"
                        autocomplete="current-password"
                        bind:value={password}
                    />
                </div>

                <button type="submit" class="btn-brutal btn-signin" disabled={submitting || ssoLoading}>
                    {submitting ? 'Signing in…' : 'Sign In'}
                </button>
            </form>

            <div class="modal-divider"></div>

            <p class="modal-note">
                Credentials verified against <span class="mono">Active Directory</span>.<br />
                Contact your administrator if you cannot sign in.
            </p>
        </div>
    </div>
</div>

<style>
    /* ── Nav — mirrors Nav.svelte exactly ─────────────────────── */
    .nav {
        position: sticky;
        top: 0;
        z-index: 100;
        background: var(--color-bg);
        border-bottom: var(--spacing-bw) solid var(--color-border);
        height: 52px;
    }

    .nav-in {
        max-width: 1400px;
        margin: 0 auto;
        padding: 0 24px;
        display: flex;
        align-items: center;
        justify-content: space-between;
        height: 100%;
    }

    .nav-brand {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.95rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        display: flex;
        align-items: center;
        gap: 10px;
        color: var(--color-fg);
    }

    .nav-right {
        display: flex;
        align-items: center;
        gap: 16px;
    }

    .btn-theme {
        width: 40px;
        padding: 6px;
        font-size: 1.05rem;
        line-height: 1;
        background: var(--color-card);
        color: var(--color-fg);
        display: flex;
        align-items: center;
        justify-content: center;
    }

    /* ── Page body ─────────────────────────────────────────────── */
    .login-page {
        min-height: calc(100vh - 52px);
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 48px 16px;
    }

    /* ── Modal wrap — mirrors ConfigModal .modal-wrap ──────────── */
    .modal-wrap {
        position: relative;
        width: 440px;
        max-width: 94vw;
        animation: modal-card-in var(--anim-in-duration) var(--anim-timing);
    }

    /* ── Floating badge — mirrors ConfigModal .modal-badge exactly */
    .modal-badge {
        position: absolute;
        top: -18px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        align-items: center;
        justify-content: center;
        width: 36px;
        height: 36px;
        background: var(--color-accent);
        color: #fff;
        border: 3px solid var(--color-border);
        border-radius: 50%;
        box-shadow: 3px 3px 0 var(--color-shadow);
        z-index: 1;
    }

    /* ── Login card — 4px border + 10px shadow per ConfigModal ─── */
    .login-modal {
        background: var(--color-card);
        border: 4px solid var(--color-border);
        border-radius: var(--radius-default);
        box-shadow: 10px 10px 0 var(--color-shadow);
        padding: 40px 36px 32px;
    }

    .modal-header {
        margin-bottom: 8px;
        text-align: center;
    }

    .modal-title {
        font-family: 'Fraunces', serif;
        font-size: 1.35rem;
        font-weight: 700;
        margin: 0;
    }

    .modal-sub {
        font-size: 0.82rem;
        color: var(--color-muted);
        text-align: center;
        margin-bottom: 24px;
        line-height: 1.5;
    }

    /* ── Windows SSO ───────────────────────────────────────────── */
    .sso-section {
        display: flex;
        flex-direction: column;
        gap: 10px;
    }

    .btn-sso {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        background: var(--color-card);
        color: var(--color-fg);
        border: 3px solid var(--color-border);
        padding: 11px 22px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.88rem;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        width: 100%;
    }

    .btn-sso:hover:not(:disabled) {
        background: var(--color-bg);
    }

    .btn-sso:disabled {
        opacity: 0.6;
        cursor: not-allowed;
        transform: none !important;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow) !important;
    }

    .sso-error {
        font-size: 0.78rem;
        color: var(--color-alert, #c0392b);
        line-height: 1.45;
        margin: 0;
        padding: 8px 10px;
        border: 2px solid var(--color-alert, #c0392b);
        border-radius: var(--radius-default);
        background: color-mix(in srgb, var(--color-alert, #c0392b) 8%, transparent);
    }

    /* ── Or divider ───────────────────────────────────────────── */
    .or-divider {
        display: flex;
        align-items: center;
        gap: 10px;
        margin: 20px 0;
    }

    .or-line {
        flex: 1;
        height: 1px;
        background: var(--color-border);
        opacity: 0.4;
    }

    .or-label {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.68rem;
        color: var(--color-subtle);
        text-transform: uppercase;
        letter-spacing: 0.1em;
    }

    /* ── Fields ────────────────────────────────────────────────── */
    form {
        display: flex;
        flex-direction: column;
        gap: 16px;
    }

    .field {
        display: flex;
        flex-direction: column;
        gap: 5px;
    }

    /* .settings-label and .settings-input come from app.css globals */

    .field-hint {
        font-family: 'JetBrains Mono', monospace;
        font-size: 0.62rem;
        color: var(--color-subtle);
    }

    /* ── Sign In button ────────────────────────────────────────── */
    .btn-signin {
        display: flex;
        align-items: center;
        justify-content: center;
        background: var(--color-accent);
        color: #fff;
        padding: 12px 22px;
        font-family: 'Work Sans', sans-serif;
        font-size: 0.9rem;
        font-weight: 800;
        text-transform: uppercase;
        letter-spacing: 0.06em;
        margin-top: 6px;
        width: 100%;
    }

    .btn-signin:disabled {
        opacity: 0.6;
        cursor: not-allowed;
        transform: none !important;
        box-shadow: var(--spacing-so) var(--spacing-so) 0 var(--color-shadow) !important;
    }

    /* ── Divider ───────────────────────────────────────────────── */
    .modal-divider {
        height: 1px;
        background: var(--color-border);
        margin: 24px 0 20px;
        opacity: 0.3;
    }

    /* ── Footer note ───────────────────────────────────────────── */
    .modal-note {
        font-size: 0.75rem;
        color: var(--color-subtle);
        text-align: center;
        line-height: 1.6;
    }

    /* ── Responsive ────────────────────────────────────────────── */
    @media (max-width: 480px) {
        .login-modal {
            padding: 36px 20px 28px;
        }
        .nav-in {
            padding: 0 16px;
        }
    }
</style>
