# CHRONICLE — Gotchas, Quirks & Lessons Learned

## Runtime correctness hardening — 2026-08-06

- Named-pipe accepts now treat `ERROR_PIPE_CONNECTED` as success, closing the client-before-accept race reproduced by the race suite.
- `SaveConfig` encrypts a private deep copy; live notification targets retain plaintext credentials. Pipe handlers now consume immutable atomic snapshots of service config and dashboard state.
- Dashboard config reloads stop as well as start the listener. Memory-threshold REST/SSE conversion preserves the `-1` disabled sentinel, and the backend rejects invalid ranges and threshold ordering.
- Performance sustain windows use the service's actual evaluation cadence rather than the collector's internal sampling cadence.
- Config, dashboard TLS key, and SQLite DACLs are applied through `x/sys/windows` in-process APIs instead of spawning `icacls.exe`.

## 009 security + correctness hardening — shipped (2026-04-24)

Remediates 16 confirmed items from the 2026-04-24 codex full-codebase review (scope-pruned; plan at `docs/reviews/codex-2026-04-24-fullcodebase-remediation-plan.md`). Five user stories landed; one task deferred.

**Credential paths (US1/US2)**:
- Dashboard fingerprint mismatch refusal: once pinned, `Dashboard.TLSFingerprint` is immutable via the automatic register path. Legitimate cert rotation now requires manually clearing the field in `config.json` before re-registering. CLI returns a non-zero exit with `fingerprint mismatch` in the error; service logs `slog.Error("dashboard=fingerprint-mismatch", ...)` on every retry.
- Authenticated SMTP relays now require STARTTLS (port 587 flow) or `smtps://` (port 465). Drainctl refuses to send `AUTH` on a cleartext session. Plain-port-25 AUTH is rejected with a host-naming error; unauthenticated relays keep opportunistic behavior with a `slog.Warn`.
- `config.json` ACL: SERVICE (S-1-5-6) ACE dropped. Only SYSTEM and Administrators. Post-install `icacls` check confirms no stray SERVICE grant.
- DPAPI entropy constant (`LISSTech.DrainCtl/v1/notify-secret`) added. Greenfield — no migration path; any secret encrypted under the zero-entropy scheme will blank on load.

**Pipeline correctness (US3)**:
- Evtspike `Subscribe` now fires its `loss` callback on terminal `EvtNext` errors (previously discarded). Function-pointer test seams (`createEvent`, `evtNextCall`, etc.) let tests inject a closed-handle scenario.
- Atomic config read-modify-write via `readModifyWrite(f)` under the existing named mutex. Every scoped updater (`UpdateNotifySettings`, `UpdateEvtSpikeEnabled`, `InstallCertificate`) brackets Load → mutate → Save under one lock. 20-goroutine stress test verifies no lost updates.
- Named-pipe server shares `readPipeMessage` with the response path; 1 MiB cap; `ERROR_MORE_DATA` loop. Requests >4 KB no longer silently truncate.

**Defense in depth (US4)**:
- Pipe caller-SID check for privileged verbs (`register`, `remove-server`, `baseline-reset`): `GetNamedPipeClientProcessId` + `OpenProcessToken` + `Token.IsMember(adminSID)`. Denied calls get `access denied`, a `slog.Warn("pipe=access_denied", ...)`, and an `EvtAccessDenied` audit event. Read-only verbs (`status`, `history`, `servers`) still accessible to any OS-permitted caller.
- Email rendering swapped from `text/template` to `html/template`. Crafted event-log fields can no longer inject HTML into admin inboxes.
- Registry-change attribution uses the event's own `SystemTime` (RFC3339Nano), not wall-clock `time.Now()`. Buffered audit-log deliveries no longer stamp the wrong transition.
- ~~Cloud-metadata IP rejection~~ **withdrawn 2026-04-24**: an earlier 009 revision rejected `http://169.254.169.254` notification targets as a "cheap cloud-metadata hedge." Removed during 009 codex post-review after the literal-string check was shown to be bypassable via IPv6-mapped (`[::ffff:169.254.169.254]`), decimal/hex IPv4, trailing-dot, and 302 redirect (default Go HTTP client follows redirects). Hedge value was zero while the spec implied a guarantee we couldn't deliver. RFC1918 LAN webhooks were always allowed; that's unchanged. See `docs/reviews/codex-2026-04-24-009-branch-remediation.md` Step 1.

**Surface drift cleanup (US5)**:
- Deleted four dead `Update*` exports (`UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`). `UpdateNotifySettings` is the sole write path now.
- ETW event-ID constants moved from root `drainctl` to `internal/etwids/`.
- `DefaultAuditPath` deprecated in favor of `DefaultDBPath`; `GetHistory` accepts either a file or directory path (`os.Stat` branches).
- Shared Svelte helpers `logStatusTransition` and `appendPerfToRingBuffer` replace four duplicated blocks in `App.svelte`. Orphan `deriveP95`/`deriveP50` removed from `state.svelte.js`.

**Deferred**: T084-T086, splitting `internal/svc/handler.go` (1138 lines) into subsystem-named siblings (`service.go`, `piperpc.go`, `dashsync.go`, `perfsupervisor.go`, `spikesupervisor.go`). First pass produced tangled duplicate declarations. Split needs its own follow-up branch with function-by-function surgery — non-blocking since handler.go remains behavior-correct. Subpackage promotion (`internal/svc/piperpc/` etc.) was explicitly scoped out regardless; revisit once the in-package split lands.

## 008 extension — event_spikes + servers SQLite migration + swimlane (2026-04-21)

Folded the last two in-memory stores into `drainctl.db`. `internal/dashboard/spikestore.go` (the bounded per-host ring buffer for confirmed event-log spikes) and the `servers.json` atomic-rename roster are both retired; replaced by `telemetry.EventSpikeStore` (event_spikes table, `UNIQUE(host,channel,window_start_ms)` so dedup is now forever rather than only-vs-newest) and `telemetry.ServerStore` (servers table, CheckResult serialized as a JSON blob in `last_result_json`). Retention for event_spikes piggybacks on `RetentionSettings.AuditDays` — same TTL as the drain-mode audit trail, per the operator's request. A one-shot boot helper `MigrateLegacyServersJSON` imports any pre-009 `servers.json` and renames it to `servers.json.migrated.<ts>`; corrupt files are moved aside (`.migration-failed.<ts>`) so a malformed leftover never blocks bringup.

`GET /api/evtspike/spikes` gains an optional `from`/`to` range mode (clamped to 500 rows, RFC3339Nano) alongside the existing `limit` recent-list mode — backward-compatible, no new endpoint. The frontend's `RECENT SPIKES` table is replaced by `SpikeSwimlane.svelte`: one horizontal lane per channel, dot per confirmed spike, dot size = `sqrt(observed)` (so Application-scale and Security-scale spikes both read cleanly on the same chart), dot color bucketed by `observed/expected` ratio (≤2× accent, 2–5× amber, >5× red). Shares the same 5M/1H/1D/3D/5D preset vocabulary as 008's Overview charts but owns its own pill + localStorage persistence per-dashboard (not per-host). SSE `recent_spike` events merge into the current window buffer via `appState.recentSpikes` → swimlane `$derived`, so a spike confirmed while a detail row is open animates in without a manual refresh.

Design choice worth remembering: swimlane over line chart. Confirmed spikes are **sparse point events**, not continuous signals. A line chart would imply continuity that isn't there and its linear Y-axis would get dominated by whichever channel is busiest. Lane-per-channel + dot-per-event reads correctly and answers "which channels have been noisy, and when?" at a glance.

Cycle gotcha: `internal/telemetry` cannot import root `drainctl` or `internal/evtspike` (both already depend on telemetry). `EventSpikeStore.Insert` takes a plain-data `telemetry.EventSpike` struct; the dashboard layer owns the mapping to/from `dc.SpikePayload` at the boundary. Same pattern as `telemetry.Sample` vs `dc.CheckResult` — kept telemetry as a pure leaf package.

## 008 SQLite Chart Consumers — shipped (2026-04-20)

Feature 008 replaces all browser-local history accumulation in the Overview dashboard with SQLite-backed retained queries. The fleet sentinel route `GET /api/v1/metrics/_fleet` aggregates across all registered hosts using the existing tiered `metrics_raw` → `metrics_5min` → `metrics_hourly` cascade; the `_fleet` host receives its own rows in each tier written during the normal aggregator run. The frontend drops the mock `fleetHistory` cold-start synthesis block in `App.svelte` (the ~80-line `appendMetricsSample`/`appendSessionSample`/`appendRfxSample` accumulation loop) and replaces it with a single `fetchFleetMetrics()` call inside a Svelte 5 `$effect`. Overview charts gain a shared 5M/1H/1D/3D/5D time-window model, wheel-zoom between presets, and drag-pan with an `↺ LIVE` reset button; all four chart families (LOAD, HIC, SESSIONS, REMOTE FX) move together. A bounded production seed route `GET /api/v1/metrics` (no host) seeds per-host sparkline ring buffers from retained SQLite history on first dashboard load so `ServerTable` sparklines render immediately on a cold browser session. `seedServerMetrics()` overwrites any stale browser-local data; `localStorage` persistence for `serverMetrics` and `rfxAvailable` is removed entirely. `ServerDetail.svelte`'s fleet-aggregate fallback (`?? appState.metricsHistory`) is replaced with an explicit empty array. `metricsHistory`, `sessionHistory`, `remoteFxHistory`, `rfxAvailable`, `appendMetricsSample`, `appendSessionSample`, and `appendRfxSample` are all deleted from `state.svelte.js`. F1 in FEATURES.md was the origin of this feature; it is now retired.

## 007 — Post-migration downgrade loses new audit events (2026-04-18)

If v26.107+ runs for days/weeks after a successful JSONL→SQLite migration, every audit event recorded into `drainctl.db` during that window is **invisible to any pre-007 binary**. The pre-007 binary reads `audit.jsonl` only; rolling back via MSI downgrade restores `audit.jsonl.bak.<ts>` (the migration's original backup) but no post-migration events were ever written to JSONL — they exist only in SQLite.

The events are not lost — `drainctl.db` remains on disk and can be read by a 007+ build or opened offline by any `sqlite3` tool — but the old binary shows a hole between the `.bak`'s final row and the restart time. Operators who anticipate potentially long-lived downgrades should export audit data to JSONL before downgrading (a dedicated export command is deliberately out of scope for 007; see spec Clarifications Q4 and `research.md §15`).

Flagged here so future loops considering a downgrade path don't rediscover the gap from a cold start. If an export tool becomes a real operational need, it's a small follow-up: `sqlite3 drainctl.db` → `SELECT ... FROM audit` → emit the legacy JSONL shape.

## 007 SQLite Telemetry Store — shipped (2026-04-18)

Feature 007 replaces the in-memory history ring + JSONL audit fallback with a single durable `drainctl.db` (WAL-mode SQLite via `modernc.org/sqlite`, no cgo). Metrics flow through a three-tier cascade — `metrics_raw` (≤25 h) → `metrics_5min` (≤7 d) → `metrics_hourly` (≤30 d) — driven by a watermarked aggregator that idempotently recomputes every eligible bucket per tick. Audit writes land through a dedicated `*sql.Conn` pinned to `PRAGMA synchronous=FULL` (metrics stay at NORMAL) and a cursor-paginated `QueryRange` over all-DESC ordering. The service boot path is now `Open → MigrateJSONL → drift reconciliation → live ingest`; `LatestByHost` + registry `LastWriteTime` together catch both net-state drift and A→B→A oscillations. Retention runs on its own dedicated connection with `busy_timeout=0`, chunks deletes in 10k-row batches, does WAL checkpoints PASSIVE-then-TRUNCATE, and reports through the new `/api/v1/maintenance/status` endpoint backed by the `maintenance_jobs` table. Dashboard chart zoom now debounces (150 ms) and asks the server for `resolution=auto` — tier-selection truth lives server-side, not in the browser. `MemAuditStore` and the file-only root-package `AuditStore` are deleted; `/api/v1/history/{host}` returns 410 Gone; the CLI reads the same DB with `?mode=ro`. FR coverage and success-criteria instrumentation is in the task list — this paragraph is the map, not the territory.

## BUILD.md for Ralph loops + per-commit codex review (2026-04-17)

Rewrote `BUILD.md` from a 9-line generic agent prompt into a staged Ralph-loop runbook: Orient → Implement → Verify → Codex review → Commit → Stop. Captures the project rules (Windows build tag, CalVer in 7 places, `just lint` zero-tolerance, named mutex ≠ SQLite WAL locking) so each fresh Opus iteration doesn't have to re-derive them from `CLAUDE.md`.

Added a **light-weight codex pre-commit step** (BUILD.md §4): pipe the staged diff through `codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -` under a single correctness-and-side-effects lens. Crucially — one cycle max per task, verify findings against the actual code (codex hallucinates), fix-or-note-and-proceed. Never loop past one codex fix round; if codex is still red, stop and escalate.

Why YOLO flag: codex 0.121.0's Windows sandbox fails with `CreateProcessWithLogonW 1326` on every PowerShell spawn (see `reference_codex_yolo_windows` memory). `--dangerously-bypass-approvals-and-sandbox` is the only reliable invocation on this host; safe because `codex exec` is `approval: never` non-interactive.

Key Ralph-friendly discipline encoded in the runbook:
- Stop conditions are explicit: all-tasks-done, design drift, test-fail-after-3-tries, codex-confirmed-and-unresolved, lint-loop, diff > 1000 lines.
- Pre-commit hook failure **aborts the commit** (nothing is created); fix, re-stage, retry. Never `--amend` (rewrites the prior commit), never `--no-verify`.
- Tick `- [ ]` → `- [x]` in `tasks.md` **in the same commit** as the implementation, never separately.
- Do NOT push. Ralph decides when to push.

**Dogfood**: the codex review step caught two HIGH-confidence bugs in the first draft of this very file — (1) a shell-pipe-vs-heredoc bug where `git diff --cached | codex exec - <<'PROMPT'` silently sends the prompt but not the diff (the heredoc overrides the pipe), and (2) misleading "create a new commit" wording for pre-commit hook failure (hook failure aborts — there's nothing to compare against). Both were fixed before this commit landed. First iteration paid its own keep.

## 007 SQLite Telemetry Store — implementation notes (2026-04-17)

- **T019 — aggregator maintenance_jobs bleed from T048**. The US1 test list (`TestHourlyAggregator_RecordsMaintenanceRow`, `TestAggregator_EmitsLogsAndMaintenanceRowPerRun`) requires the aggregator to already upsert a `maintenance_jobs` row per run. T048 (US5) covers the broader instrumentation via `MaintenanceStore`, but the minimal per-run upsert had to land in T019 so its tests pass. Future T048 replaces the inline SQL in `aggregator.recordMaintenance` with a call through `MaintenanceStore.UpsertJob`; the log+row pairing (FR-032) is already wired.
- **T019 — hourlyWatermark semantics**. `hourlyWatermark = 1h + 5min` is subtracted from `now` and then **truncated to the hour boundary**, producing `maxBucketMs`. A bucket is eligible iff `bucket_ts ≤ maxBucketMs`. This is not "bucket_end + 5min ≤ now" — the truncation makes the effective freeze window range from 5 min (near a boundary) to ~1h5min (just after a boundary). A test that seeds a bucket that ended just a few minutes before `now` will non-deterministically get aggregated, so `TestHourlyAggregator_SkipsIncompleteHours` sticks to the current in-progress hour (bucket_ts > maxBucketMs under every clock phase).
- **T024 — `ROW_NUMBER() OVER (PARTITION BY host ...)` is O(rows), not O(hosts)**. SQLite must rank every audit row before the outer `WHERE rn = 1` can filter, so a window-function "latest per group" query scans the whole table regardless of `audit_host_ts (host, ts DESC)`. The O(hosts) pattern is `WITH latest AS (SELECT host, MAX(ts) FROM audit GROUP BY host)` — SQLite's MIN/MAX-per-group optimization walks distinct host values from the index and picks each group's max via the index ordering, then a PK join pulls the full rows. Tie-break on `new_state` lives in a correlated `SELECT MAX(new_state) WHERE host=? AND ts=?` subquery (PK seek). Codex caught the window-function version in review — "keep it O(hosts)" was load-bearing, not decorative.

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

- **CalVer `YY.MM.BUILD` is git-derived, nothing to bump by hand.** `scripts/version.ps1` computes it from the commit month + monthly commit count; `just all`/`just release` inject it into Go (ldflags), the Windows resource (`just resource` renders `assets/drainctl.rc` from its template and recompiles `drainctl.syso`), the MSI (`-p:ProductVersion=`), and the PowerShell module (`.psd1.tmpl` rendering). The only version string still hand-maintained is `docs/index.html` release-notes content, updated per release refresh — not per commit.
- **`just resource` is wired into the build.** `just all`/`just release` call it automatically; re-run it manually only if you are poking at `drainctl.rc.tmpl` or `assets/drainctl.man` directly.
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
