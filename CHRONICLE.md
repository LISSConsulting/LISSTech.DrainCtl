# CHRONICLE — Gotchas, Quirks & Lessons Learned

## 007 SQLite Telemetry Store — planning review (2026-04-17)

Commits `4c13128` / `e25482e` / `c021f4e` / (this commit) resolve 32 deduplicated findings from an adversarial codex review of the spec/plan/tasks for feature 007. Full artifacts at `docs/reviews/codex-2026-04-17-{synthesis,remediation-plan}.md`. Key lessons worth remembering once implementation starts:

- **Boot-sequence order matters**: `telemetry.Open() → MigrateJSONL (chunked) → drift reconciliation → live ingest`. Reconciliation compares against `LatestByHost`, which is wrong if JSONL hasn't been imported yet.
- **PRAGMAs are connection-local in SQLite, not database-level**. Using `*sql.DB` (a pool) means PRAGMAs only hit whichever physical connection runs them. Always apply via `driver.Connector`'s `Connect()` so every pooled connection gets them. Don't let a `journal_mode=wal` test fool you — that one IS persistent and will pass even when the others don't.
- **Aggregator `ON CONFLICT DO NOTHING` freezes buckets on late raw samples**. Use `DO UPDATE` keyed on the full recompute + a watermark (5 min) + source-tier retention as the freeze boundary. Don't gate on `minute = 0` — scheduler jitter will miss hours.
- **Drift reconciliation on state alone can't see A→B→A oscillations**. Compare registry `LastWriteTime` against last-audit `ts` too.
- **SQLite row-value comparison is lexicographic-ascending**. A pagination predicate `(ts, host, new_state) < (cursor…)` only works under an **all-DESC** (or all-ASC) ORDER BY, NOT mixed DESC/ASC. If you need mixed ordering, use an explicit `OR`-cascade predicate.
- **`incremental_vacuum` does not return OS disk space** — only to SQLite's internal free-list. File size stays at high-water mark. Document this for operators; don't promise "file shrinks after retention."
- **`wal_checkpoint(TRUNCATE)` can block on long readers**. Run it on a dedicated `*sql.Conn` with `busy_timeout=0`, wrap in a 5s context deadline, do `PASSIVE` first and only escalate to `TRUNCATE` when WAL > 16 MB. On `SQLITE_BUSY` log and skip — never retry synchronously.
- **Audit rows with `principal=""` depend on a partial index** `audit_principal WHERE principal <> ''`. Any future change to the reconciliation principal value silently breaks the index. Guard with a `reconciliationPrincipal` constant referenced by both writer and test.
- **Codex CLI on Windows** (v0.121.0) — read-only sandbox fails with `CreateProcessWithLogonW 1326` on every `pwsh` spawn. Workaround: pipe file contents inline via bash heredoc + `codex exec --sandbox read-only -`. Relying on codex's own file reads is a non-starter on this host.

## Svelte 5

- **No `structuredClone()` on `$state` objects.** Svelte 5 wraps reactive state in Proxies that `structuredClone()` can't handle — throws at runtime. Use `JSON.parse(JSON.stringify(...))` instead. Bit us in `ConfigModal` save path.
- **`$derived(() => fn)` stores the function, not the result.** Must use `$derived.by(() => fn())` for computed values. Caused `totalSessions` to be an arrow function instead of a number.
- **`export let x = $state(...)` doesn't work cross-module.** Consumers get a snapshot, not a live reference. Use `export const x = $state({...})` and access properties on the stable object.

## Memory Thresholds (% free vs % used)

- **Go stores memory thresholds as % free; UI works in % used.** `api.js` `fetchSettings()`/`saveSettings()` handles the inversion once. Do NOT invert again in components — double-inversion caused ConfigModal presets to set warn=20% used (extremely aggressive) when Chill intended warn=80% used.
- **`thresholds.js` defaults must mirror Go's defaults after inversion.** Go `mem_warn_pct=20` (% free) → frontend default should be `80` (% used). Getting this wrong made every memory ring gauge glow amber/red.

## Go Zero Values & JS Falsy Traps

- **Go's zero value for `int` is `0`, not `null`.** Frontend code using `??` (nullish coalescing) won't catch it — `0 ?? 80` returns `0`. Use `> 0 ? val : default` to match Go's `resolveThreshold(0, defVal) = defVal` semantics.
- **`parseInt("0") || 80` overrides a valid zero.** JS falsy-zero trap broke disabled threshold configs.

## Wire Format Mismatches (Mock vs Production)

- **Mock API field names diverged from Go JSON tags.** `rfx_quality` vs `rfx_quality_pct`, `session_cpu_p95` vs `session_cpu_p95_pct`, etc. Charts worked in dev, showed all zeros in prod. Always derive mock field names from Go struct tags.
- **Frontend field names must match Go JSON tags exactly.** `grace_period_minutes` vs `grace_period`, `perf_monitoring` vs `performance`, `targets` vs `notifications` — five mismatches made ConfigModal completely non-functional in production.
- **`ServerView` projection matters.** Raw `ServerInfo` has nested `last_result` and capitalized status; frontend expects flat structure with lowercase tokens. Always project through a view type.

## SSE

- **SSE handlers must disable `WriteTimeout`.** Go's default HTTP write timeout kills long-lived SSE connections. Set `WriteTimeout: 0` on the SSE handler's route.
- **SSE needs keepalive comments.** Proxies/firewalls with 30–60s idle timeouts silently drop SSE connections. Send `: keepalive\n\n` every 25s.
- **SSE sessions must be re-validated.** Without periodic checks, a logged-out user's SSE stream continues delivering events indefinitely.
- **SSE broadcasts must redact secrets.** `broadcastSettingsUpdate` must strip `Secret` fields before marshaling — SSE events go to all connected browsers.
- **`OnUpdate` callback under write lock = deadlock.** `Broadcast` tried to acquire a lock already held by the caller. SSE callbacks must not hold the state lock.

## NTLM / Negotiate Auth

- **`NegotiateMiddleware` must be instantiated once at setup.** Per-request instantiation destroys NTLM multi-leg state → `SEC_E_INVALID_TOKEN`. The middleware maintains connection-level auth state across the 3-leg handshake.

## Config & DPAPI

- **DPAPI `CRYPTPROTECT_LOCAL_MACHINE` scope** — any process on the same machine can decrypt. Appropriate when service runs as SYSTEM and dashboard runs as admin. Stolen config files are useless on other machines (feature, not bug).
- **Config secret sentinel pattern:** API sends `••••••••` for existing secrets; if browser sends it back unchanged, backend preserves the existing encrypted value. Empty string clears the secret. Any other value is a new plaintext to encrypt.
- **`Validate()` encrypts, `DecryptSecrets()` decrypts.** Save path: plaintext → `Validate()` encrypts → write `dpapi:base64` to disk. Load path: read `dpapi:base64` → `Validate()` (skips already-encrypted) → `DecryptSecrets()` → plaintext in memory.

## Notifications

- **`repeat_minutes: 0` meant "once per process lifetime"** — useless for perf triggers that fire repeatedly. Changed to 1440 (once/day).

## Build & Versioning

- **CalVer `YY.DOY.patch` must update in 7 places.** `drainctl.go`, `drainctl.rc`, `.psd1`, `.wixproj`, `README.md`, `CLAUDE.md`, `docs/index.html`. `just bump` handles this.
- **After `.rc` changes, run `just resource`** to recompile `.syso`. Forgetting this ships stale version info in the binary.
- **WiX custom actions must match CLI flags.** Broke v26.100.0 when MSI install action used old flag names.
- **Signing order: binaries → MSI → sign MSI.** Can't sign the MSI before the binaries inside it are signed.

## Frontend Patterns

- **Neobrutal focus uses lift transform, not rings/outlines.** Design convention across all `btn-brutal` elements.
- **Always verify UI changes visually.** Never commit frontend changes without screenshotting. Too many invisible regressions (wrong colors, missing styles, broken dark mode).
- **`display:none` on `<img>` gets overridden by `img { display: block }`.** Use CSS class swaps or `visibility` instead for screenshot toggle.
- **`toLocaleTimeString()` without explicit locale produces inconsistent 12/24h output.** Always pass `'en-US', { hour12: false }` for stable `HH:MM:SS`.

## Testing

- **`e.message` is `undefined` when thrown value isn't an `Error`.** Network failures can throw strings. Use `e?.message ?? String(e)`.
- **`if (loading) return` guard in fetch functions can silently drop requests.** If a filter changes during a fetch, the new request is swallowed. Use a sequence counter (`fetchSeq`) to discard stale results instead.
- **`localStorage` corruption: `parseInt("NaN") || default`** — `parseInt` returns `NaN`, `||` doesn't catch it because `NaN` is falsy... wait, it does. But `parseInt(null)` returns `NaN` and `NaN || 1` = `1`. The actual bug was `parseInt(stored) || 1` where stored was a valid `"0"` — same falsy-zero trap.
