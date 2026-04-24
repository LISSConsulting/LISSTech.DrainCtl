# DrainCtl — Codex Full-Codebase Remediation Plan (2026-04-24)

**Source:** `docs/reviews/codex-2026-04-24-fullcodebase-synthesis.md`
**Branch:** `008-sqlite-chart-consumers`

The work is grouped into four themed PRs, each independently reviewable and revertible. Within each PR, steps are ordered so later steps can piggyback on earlier ones without merge conflicts.

---

## Phase 1 — Security Hardening (HIGH): fail-closed fixes

PR theme: *"Close credential and transport downgrade paths."*

### Step 1 — Refuse silently-changed dashboard fingerprints

- **Addresses:** C-B6 (silent-cert-change half only — TOFU on first contact remains acceptable per product decision)
- **Scope note:** the full Step 1 from the original plan (widen `newDashClient` + system-roots-for-unpinned + `--tls-fingerprint` flag) was rejected: it trades narrow first-contact MITM protection for permanent onboarding friction on a product whose dashboards are self-signed by design. TOFU stands; we only add tamper-detection *after* the pin.
- **Files:**
  - `cmd/drainctl/register_cmd.go:68-82` — auto-pin block: add mismatch refusal branch.
  - `cmd/drainctl/register_cmd_test.go` — mismatch subtest.
  - `internal/svc/handler.go:1031-1042` — service-side `registerWithDashboard`: mirror the refusal so the service can't silently re-pin either.
  - `internal/svc/handler_test.go` (or factor a new testable helper) — service-side mismatch test.
- **Change:**
  1. In `cmd/drainctl/register_cmd.go`, before the existing auto-pin write, compare `fileCfg.Dashboard.TLSFingerprint` to `regResult.TLSFingerprint`. If both non-empty and they differ, return an error naming both fingerprints; do NOT overwrite.
  2. In `internal/svc/handler.go:1031-1042`, mirror: when `regResult.TLSFingerprint != ""` and `dashCfg.TLSFingerprint != ""` and they differ, `slog.Error("dashboard=fingerprint-mismatch", "saved", ..., "offered", ...)`, do NOT overwrite, do NOT re-init the client, return `false`.
  3. TOFU unchanged: empty saved fingerprint + non-empty offered still pins on first register as today.
- **Verification:**
  - `register_cmd_test.go` mismatch subtest: stub a Register returning fingerprint `B` while stored config has fingerprint `A`; assert `runRegister` returns an error containing `fingerprint mismatch` and the on-disk `TLSFingerprint` is unchanged.
  - Service side: factor the fingerprint-compare into a small testable helper; assert mismatch branch does not mutate `dashCfg.TLSFingerprint`.
  - Manual: register, then manually edit `config.json` to set a different fingerprint, re-register; confirm error and no overwrite.
- **Effort:** S
- **Risk:** Legitimate cert rotation is now a two-step operation (operator must clear the stored fingerprint before re-registering). Document in release notes. Rollback: revert the two branches.

### Step 2 — Require STARTTLS when SMTP auth is used; kill cleartext AUTH

- **Addresses:** C-B1
- **Files:** `email.go` (`sendSMTPStartTLS`), `email_test.go`.
- **Change:** In `sendSMTPStartTLS`, after `c.Hello`, always call `c.Extension("STARTTLS")`. If `target.Secret != ""` and either (a) STARTTLS is not advertised, or (b) `c.StartTLS` returns an error, return `fmt.Errorf("smtp: refusing cleartext AUTH on %s; use smtps:// or a server that supports STARTTLS", host)`. For secret-less relays (internal mailhog-style MTAs), keep the current opportunistic behaviour but log `slog.Warn("smtp: no STARTTLS available, sending without encryption")`.
- **Verification:** Add `email_test.go` subtest `TestSMTPStartTLS_RefusesCleartextAuth` using `net.Pipe`-driven in-process SMTP server that omits the STARTTLS extension while the target has `Secret: "x"`; assert error contains "refusing cleartext AUTH".
- **Effort:** S
- **Risk:** Misconfigured internal relays that accept AUTH PLAIN on port 25 will stop working. Mitigation: the error names the exact fix (`smtps://`). Rollback: revert the two branches.

### Step 3 — Drop SERVICE from config.json ACL

- **Addresses:** C-B3 (ACL half)
- **Files:** `config.go` (`restrictConfigACL`), `config_test.go`.
- **Change:** Remove the `icacls /grant *S-1-5-6:(M)` command. Add an explicit `icacls /remove *S-1-5-6` before the grants so repeat installations on older ACLs clean up. Keep SYSTEM and Administrators.
- **Verification:** On an elevated dev box, delete `config.json`, run the service once to recreate, then `icacls "%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json"` and confirm no SERVICE ACE. Add a comment-documented manual checklist entry to the release checklist.
- **Effort:** S
- **Risk:** Any non-SYSTEM service running in SERVICE group that expected read access breaks. drainctld runs under LocalSystem, so the intended consumer is unaffected. Rollback: re-add the line.

### Step 4 — Add fixed-entropy DPAPI scope (greenfield, no migration)

- **Addresses:** C-B3 (DPAPI half)
- **Scope note:** greenfield product — zero deployed agents hold legacy zero-entropy ciphertext. No migration, no fallback, no deep-copy writeback gymnastics. Just flip the scheme.
- **Files:** `dpapi.go`, `dpapi_test.go`.
- **Change:**
  1. Declare `var dpapiEntropy = []byte("LISSTech.DrainCtl/v1/notify-secret")` in `dpapi.go`.
  2. In `DPAPIEncrypt` (line 40-48 region), pass `dpapiEntropy` as the `pOptionalEntropy` arg to `CryptProtectData` (currently `0` / `nil`).
  3. In `DPAPIDecrypt` (line 71-79 region), pass the same `dpapiEntropy` to `CryptUnprotectData`.
  4. No changes to `EncryptSecrets`/`DecryptSecrets` in `config.go` — they go through the two DPAPI helpers and pick up the new behaviour transparently.
- **Verification:**
  - `dpapi_test.go`: round-trip a plaintext through `DPAPIEncrypt`/`DPAPIDecrypt`; assert it matches.
  - Negative test: craft ciphertext with a different entropy constant and assert `DPAPIDecrypt` returns error.
  - Manual: register a webhook target, restart the service, confirm notifications still fire.
- **Effort:** S
- **Risk:** If this ships after agents are deployed with the old scheme, those secrets get blanked on next load. **Must land before first external release.** Rollback: remove the entropy arg from both calls.

---

## Phase 2 — Correctness HIGH: subtle bugs with production impact

PR theme: *"Tighten the long-lived RPC/event pipelines."*

### Step 5 — Plumb `loss` through evtspike and supervisor

- **Addresses:** C-A1
- **Files:** `internal/evtspike/subscriber_windows.go`, `internal/evtspike/subsystem.go` (or wherever `Subscribe` is called), `internal/evtspike/*_test.go`.
- **Change:** In the `EvtNext` error branch (`r == 0` path inside `Subscribe`'s goroutine), inspect `e` (the final return) via a pre-bound `windows.Errno` capture. If the errno is non-zero and not `ERROR_NO_MORE_ITEMS` / `ERROR_TIMEOUT`, call `loss(fmt.Errorf("EvtNext %s: %w", channel, e))` then `return` (don't just `break`). The supervisor already owns re-subscription; verify by auditing the one caller. Add a `counter` comment documenting the return condition.
- **Verification:** Add `subscriber_test.go` case that invokes `Subscribe` against a deliberately closed subscription handle (inject via a test-only unexported setter or by calling `procEvtClose` on `sub` after Subscribe returns) and asserts `loss` fires within 2 s with a non-nil error.
- **Effort:** S
- **Risk:** A too-aggressive loss classification could cause the supervisor to loop-resubscribe on recoverable conditions. Mitigation: we treat only hard errors as terminal; zero-count returns continue to `break` the inner loop without invoking loss. Rollback: revert the function body; signatures untouched.

### Step 6 — Make config scoped updates atomic RMW

- **Addresses:** C-A2
- **Files:** `config.go`, `config_test.go`.
- **Change:** Introduce unexported `readModifyWrite(f func(*Config) error) error` that:
  1. Acquires the existing named mutex (hoist the acquisition currently in `saveConfigToFile`).
  2. Calls `LoadConfig()` (now an unlocked variant — split `LoadConfig` into exported lock-taking wrapper and unexported `loadConfigLocked`).
  3. Invokes `f(cfg)`.
  4. Calls an unlocked `saveConfigFileLocked(cfg)`.
  Rewrite `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig`, `UpdateNotifySettings`, `UpdateEvtSpikeEnabled`, and `InstallCertificate` in terms of `readModifyWrite`.
- **Verification:** Add a `TestConfigRMW_NoInterleave` test spawning 20 goroutines hammering `UpdateSessionThreshold` with distinct values, then reading the final file content; assert the last written value matches one of the values and no corrupt JSON. Also add a regression test that two concurrent updaters (notifications vs grace) both land.
- **Effort:** M
- **Risk:** The rework threads through every setter; a miss leaves one path unlocked. Mitigation: grep for `saveConfigToFile(` and ensure every non-migration caller has moved under `readModifyWrite`. Rollback: revert.

### Step 7 — Shared pipe message reader

- **Addresses:** C-A3
- **Files:** `internal/pipe/pipe.go`, `internal/pipe/pipe_test.go`.
- **Change:** Extract `readPipeMessage(r io.Reader, initialBufSize int) ([]byte, error)` mirroring the existing `readPipeResponse` loop. On `errors.Is(err, windows.ERROR_MORE_DATA)`, grow with repeated reads capped at 1 MiB; return concatenated bytes. Use in both `handlePipeConn` (replacing the single `conn.Read` at pipe.go:100-103) and refactor `readPipeResponse` to delegate to it.
- **Verification:** Reuse the existing `scriptedReader` pattern from `pipe_test.go:330 TestReadPipeResponse_ContinuesOnMoreData` — it already precisely models `windows.ERROR_MORE_DATA` behaviour, which `net.Pipe` **cannot** reproduce. Add `TestReadPipeMessage_HandlesServerSideMoreData` exercising the new helper with the same `scriptedReader`, asserting correct concatenation across multiple ERROR_MORE_DATA chunks. Add a cap test: inject a reader that keeps returning ERROR_MORE_DATA past 1 MiB and assert the specific cap error. Delete obsolete duplication in `TestReadPipeResponse_*` once helper is shared.
- **Effort:** M
- **Risk:** The `errors.Is(err, windows.ERROR_MORE_DATA)` check depends on the concrete error returned by `os.File.Read` on a message-mode pipe; the existing response-side loop already relies on it and works in production, so the failure mode is known-good. Rollback: revert the refactor; response-side loop keeps working.

---

## Phase 3 — Security MEDIUM + Correctness MEDIUM

PR theme: *"Lock down outbound URLs, pipe callers, email rendering, and timestamp attribution."*

### Step 8 — Block cloud metadata IP in outbound notification URLs

- **Addresses:** C-B2 (minimal nod-to-cloud version — full SSRF guard rejected)
- **Scope note:** the general RFC1918 / link-local / ULA guard was dropped: drainctl is an on-prem Windows product, LAN webhooks (internal Slack bridges, internal ntfy, internal Mattermost) are a primary legitimate use case, and blocking them by default would break more operators than it protects. The only surviving piece is a literal reject of `169.254.169.254` (cloud-metadata service IP) as a cheap hedge against a future cloud-deployed build accidentally turning an authenticated dashboard bug into an IAM-credential exfiltration.
- **Files:** `notify.go`, `notify_test.go`, `internal/dashboard/server.go` (target-persist + test paths).
- **Change:**
  1. Add a small unexported helper in `notify.go`:
     ```go
     func rejectCloudMetadata(rawURL string) error {
         u, err := url.Parse(rawURL)
         if err != nil { return nil } // parse errors handled elsewhere
         if u.Hostname() == "169.254.169.254" {
             return fmt.Errorf("notify: cloud metadata IP 169.254.169.254 is not a valid notification target")
         }
         return nil
     }
     ```
  2. Call it at the top of `sendWebhook` (notify.go:390) and `sendNtfy` (notify.go:580) — return the error before dialling.
  3. Call it in `internal/dashboard/server.go:748` (`handlePutSettings` — before persisting targets) and `:930` (`handleNotifyTest` — before dispatching) so a bad URL is rejected at write-time, not just at send-time.
  4. Do **not** add an allowlist, a new config field, or a shared `internal/neturl` package. Single-purpose helper, colocated with the callers.
- **Verification:**
  - `notify_test.go`: `rejectCloudMetadata("http://169.254.169.254/latest/meta-data/")` returns error; `rejectCloudMetadata("http://192.168.1.10/hook")` returns nil (LAN still allowed); `rejectCloudMetadata("https://slack.com/hook")` returns nil.
  - Manual: via dashboard UI, add a webhook at `http://169.254.169.254/x`; confirm save fails. Add `http://192.168.1.10/x`; confirm save succeeds.
- **Effort:** S
- **Risk:** None realistic — `169.254.169.254` is never a legitimate notification target. Rollback: delete the helper and four call sites.

### Step 9 — Pipe caller-SID check for privileged verbs (keep default DACL)

- **Addresses:** C-B4
- **Files:** `internal/pipe/conn_windows.go`, `internal/pipe/pipe.go`, new `internal/pipe/sid_windows.go`, new `internal/pipe/sid_test.go`.
- **Key correction from prior plan revision:** do **not** narrow the DACL to SYSTEM+Admins+SERVICE — doing so breaks existing non-admin CLI usage of read-only verbs (`status`, `history`, `servers`) because the connection is blocked before any handler code runs. Instead, keep the default DACL (which on a SYSTEM-owned pipe already restricts by default DACL inheritance) and enforce **per-verb authorization** after accept.
- **Change:**
  1. In `handlePipeConn`, after accepting the connection and reading the request, for the privileged verbs `register`, `remove-server`, `baseline-reset`, call a new helper `callerIsPrivileged(conn net.Conn) (bool, string, error)` that:
     - Calls `GetNamedPipeClientProcessId` for the server-side handle.
     - Opens the client process token (`OpenProcessToken`).
     - Retrieves the user SID (`GetTokenInformation(TokenUser)`).
     - Returns `true` if the SID is LocalSystem (`S-1-5-18`) or a member of Administrators (`S-1-5-32-544`).
  2. On `false`, return `PipeResponse{OK: false, Error: "access denied"}` and `slog.Warn("pipe=access_denied", "cmd", req.Cmd, "sid", sidStr)`. Include the event in the ETW audit channel (`EvtAccessDenied`, id 5004 — already defined).
  3. Read-only verbs (`status`, `history`, `servers`) are unchanged — any caller the OS permitted through the pipe DACL keeps working.
  4. Optional hardening (follow-up, not this step): evaluate whether the default DACL actually restricts enough in production; if a future audit shows interactive non-admin users can connect, consider a tight SDDL that explicitly grants `IU` (S-1-5-4) read-only, SYSTEM+Admins full — but stage that separately so it can be reverted without rolling back the caller-SID check.
- **Verification:**
  - Unit: `sid_test.go` exercises the SID-check logic via a stub token-reader interface — inject SIDs for non-admin, admin, LocalSystem and assert verdicts.
  - Integration: `pipe_caller_test.go` opens a pipe, dials as the current process (admin on dev), calls `register` against a stub `HandleRegister` and asserts success; then run against a crafted scripted reader that simulates the `access denied` branch and asserts the response shape.
  - Manual: from a non-admin shell, run `drainctl register https://dash/` — confirm `access denied`. Run `drainctl status` as the same user — confirm still works.
- **Effort:** L
- **Handle-lifetime notes:** the SID-check helper opens three native handles — the client process, its primary token, and the SID buffer. All must close on every exit path:
  - `procHandle, _ := windows.OpenProcess(...)` → `defer windows.CloseHandle(procHandle)` immediately after success.
  - `var tokenHandle windows.Token; windows.OpenProcessToken(procHandle, TOKEN_QUERY, &tokenHandle)` → `defer tokenHandle.Close()`.
  - SIDs returned from `GetTokenInformation` live in a Go byte slice so no extra free is needed, but the returned `*windows.SID` aliases into that slice — do not return the pointer past the helper.
- **Risk:** `GetNamedPipeClientProcessId` can fail on disconnected clients; handle the error by denying. The SID membership check must honour effective-token groups (run-as-admin but token not elevated), which can be subtle — use `windows.Token(tokenHandle).IsMember(adminSID)` which handles this correctly (the existing `isElevated` in config.go:781 already does this pattern). Rollback: revert; no DACL change to reverse.

### Step 10 — Switch email template to html/template

- **Addresses:** C-B5
- **Files:** `email.go`, `email_template.html`, `email_test.go`.
- **Change:** Replace `text/template` import with `html/template`; update `emailTmpl` initializer. Audit `email_template.html` for any intentional raw-HTML insertions — if found, wrap those specific fields as `template.HTML` only after confirming source is trusted (none of `.Subject/.Message/.Host/.ChangedBy/.SpikeChannel` qualify, so default auto-escaping suffices).
- **Verification:** Add `TestEmailTemplate_EscapesHTML` in `email_test.go` that renders with `Message: "<script>alert(1)</script>"` and asserts the rendered body contains `&lt;script&gt;` and not the raw tag.
- **Effort:** S
- **Risk:** Subtle visual regressions if any literal template strings relied on text/template quirks. Mitigation: diff the rendered output before/after on a representative payload. Rollback: one-line import revert.

### Step 11 — Use event SystemTime for registry-change attribution

- **Addresses:** C-A4
- **Files:** `internal/watcher/evtsubscribe.go`, `internal/watcher/evtsubscribe_test.go`.
- **Correction from prior plan revision:** the `eventRec` struct at `evtsubscribe.go:37-46` **already** has `System.TimeCreated.SystemTime string`. The only missing change is to parse it and use it.
- **Change:** In `processEvent` (line 230-272), after `xml.Unmarshal`, parse `evt.System.TimeCreated.SystemTime` via `time.Parse(time.RFC3339Nano, …)`. On success, use the parsed time for `RegistryChangeAttribution.Timestamp` (line 263). On parse error, fall back to `time.Now()` and `slog.Warn("evtspike: unparseable SystemTime", "raw", raw)`.
- **Verification:** Factor `processEvent` so the XML string is a parameter (currently it renders via `renderEventXML`). Add `TestProcessEvent_UsesSystemTime` feeding a synthetic 4657 XML with `TimeCreated SystemTime="2026-04-24T10:00:00Z"`; assert `LatestAttribution().Timestamp.Equal(parsed)`. Regression: `TestWaitAttribution_IgnoresOlderEvents` — push an attribution with SystemTime=T0, call `WaitAttribution(after=T0+5s, timeout=…)`, assert return is `""`.
- **Effort:** S
- **Risk:** Parse-format drift. Mitigation: fallback + warn. Rollback: revert.

---

## Phase 4 — Design cleanup (refactors)

PR theme: *"Tidy public surface and split god-files."*

### Step 12 — Delete dead `Update*` helpers

- **Addresses:** C-C2
- **Files:** `config.go`, `config_test.go`.
- **Change:** Remove `UpdateNotifications`, `UpdateSessionThreshold`, `UpdateGracePeriod`, `UpdatePerformanceConfig` and their dedicated tests. Keep `UpdateNotifySettings`, `UpdateEvtSpikeEnabled`, and `InstallCertificate`. Must come *after* Step 6 (which moves everything onto `readModifyWrite`) to avoid merge churn.
- **Verification:** `go build ./... && go test ./...` — compilation flushes out any forgotten caller. `go vet` must be clean.
- **Effort:** S
- **Risk:** A downstream cgo/DLL consumer might import one of them. Mitigation: grep `grep -r "UpdateNotifications\|UpdateSessionThreshold\|UpdateGracePeriod\|UpdatePerformanceConfig"`. Rollback: revert.

### Step 13 — Move ETW IDs to `internal/etwids`

- **Addresses:** C-C1
- **Files:** new `internal/etwids/etwids.go`, `drainctl.go`, all callers in `internal/svc` and `internal/dashboard`.
- **Change:** Create `internal/etwids/etwids.go` exporting `EvtDashboardAccess`, `EvtDashboardConfigChange`, `EvtServerRegistered`, `EvtServerRemoved`, `EvtAccessDenied`, `EvtGenericAudit`. Delete the constants from `drainctl.go`. Fix imports. Keep `MigrateFromRegistry` and `WriteDefaultParameters` in root but add `// Installer-only; do not call from runtime code.` doc comments.
- **Verification:** `go build ./...`; confirm the DLL (`cmd/cshared/`) still builds because it never referenced these IDs.
- **Effort:** S
- **Risk:** Public-API break if any external tool imports `github.com/LISSConsulting/LISSTech.DrainCtl.EvtDashboardAccess`. Risk is low — these are service-internal event IDs. Rollback: move back.

### Step 14 — Retire the "JSONL audit path" API surface safely

- **Addresses:** C-C5
- **Files:** `drainctl.go:37-40`, `history.go:35`, `cmd/drainctl/main.go:45`, `cmd/cshared/exports.go:81`, `config.go:283, 389, 1073` (default-setting call sites), `config_test.go:836`, `README.md` (if mentioned).
- **Critical correction from prior plan revision:** this is **not** a pure rename. `GetHistory` at `history.go:35-39` deliberately treats `HistoryOptions.DBPath` as a file path whose **parent directory** is the telemetry store root: `dataDir := filepath.Dir(opts.DBPath)`. The CLI (`cmd/drainctl/main.go:45`) and the DLL (`cmd/cshared/exports.go:81`) both pass `DefaultAuditPath()` → `filepath.Dir` yields `%ProgramData%\LISS Technologies\LISSTech DrainCtl`. If we simply rename to `DefaultDBPath()` returning `drainctl.db` AND delete the `filepath.Dir` branch, `telemetry.OpenReadOnly(dataDir)` receives a file path and fails. The plan must redesign the storage-open contract, not just rename.
- **Change (two-phase, sequenced):**
  1. **Phase 14a — introduce new API.**
     - Add `DefaultDBPath() string` returning `DefaultDataDir() + "\\drainctl.db"` (new function).
     - Add `DefaultDBDir() string` returning `DefaultDataDir()` (explicit directory accessor).
     - Rename `HistoryOptions.DBPath` semantic from "file path whose parent is the data dir" to "direct path to the SQLite DB file or a directory containing it". Implement `GetHistory` to call `os.Stat` — if dir, use as-is; if file, use parent; if empty, use `DefaultDBDir()`.
     - Update `cmd/drainctl/main.go:45` default from `dc.DefaultAuditPath()` to `dc.DefaultDBPath()` and the flag help text from "audit trail file" to "SQLite audit DB".
     - Update `cmd/cshared/exports.go:81` similarly.
     - Leave `DefaultAuditPath` in place, reimplemented as `DefaultDBPath()` (so any embedder or registry-migration code keeps working), but mark `// Deprecated: use DefaultDBPath.`
     - Update the default-setting call sites in `config.go:283, 389, 1073` to `DefaultDBPath()` — the `Config.AuditPath` field is persisted, but the migration path (`config.go:1073 WriteDefaultParameters`) writes to registry for legacy installers; flip its value over.
  2. **Phase 14b (later release) — delete `DefaultAuditPath` and rename `Config.AuditPath` → `Config.DBPath` with a one-shot migration in `LoadConfig` (read old key if present, write new key on next save).** Out of scope for this PR; land as a separate commit once one full release cycle has passed.
- **Verification:** `go build ./... && go test ./...`. Add `TestGetHistory_AcceptsFilePath` and `TestGetHistory_AcceptsDirPath` to `history_test.go`. Manual: `drainctl history --limit 1` works against an existing `drainctl.db`.
- **Effort:** M (was S — scope wider than rename)
- **Risk:** Operators with explicit `--db <path>` flags that point at `audit.jsonl` break immediately — but that file doesn't exist post-feature-007 anyway, so the breakage is cosmetic. Rollback: revert Phase 14a.

### Step 15 — Split `internal/svc/handler.go`

- **Addresses:** C-C3
- **Files:** `internal/svc/handler.go` split into `handler.go` (core Run loop), `handler_pipe.go`, `handler_dashboard.go`, `handler_perf.go`, `handler_evtspike.go`, `handler_lifecycle.go`. Test file may split into matching `*_test.go` files or stay single.
- **Change:** Pure cut-and-paste: move related functions together without changing signatures or types. Each new file keeps the `//go:build windows` tag and `package svc` declaration. Any unexported helpers used across files stay in their origin file.
- **Verification:** `go build ./... && go test ./internal/svc/...` must pass unchanged. `git diff --stat` should show near-zero net line delta (pure moves).
- **Effort:** L
- **Risk:** Subtle compile breaks if a helper is referenced across files but the new file misses an import. Mitigation: each split is one file, compile after each. Rollback: `git reset` to pre-split.

### Step 16 — Extract shared Svelte helpers

- **Addresses:** C-C4
- **Files:** `frontend/src/lib/state.svelte.js`, `frontend/src/App.svelte`.
- **Change:** In `state.svelte.js`, export two pure functions:
  - `logStatusTransition(prev, next, host, timestamp)` — returns a `{from, to, host, timestamp}` tuple plus side-effect of pushing to the shared transitions ring buffer.
  - `appendPerfToRingBuffer(sample)` — pushes to the shared sparkline ring buffer with the existing cap.
  Replace the four duplicated blocks in `App.svelte` (lines 192-216, 284-302, 386-411, 414-431) with calls to these helpers.
- **Verification:** Manual dashboard check:
  1. `just dashboard-dev` (or equivalent), open dashboard in browser.
  2. Force a status transition by toggling drain mode on the test host; confirm the transition list updates once (not twice) and the timestamp matches the SSE payload.
  3. Kill SSE by blocking the endpoint in devtools; confirm polling fallback still logs transitions and sparkline keeps accumulating samples.
  4. Verify sparkline ring length caps at the same old max value by watching `$state.snapshot(perfRing).length`.
- **Effort:** M
- **Risk:** Extraction can reorder effects. Mitigation: keep helpers side-effect-only on the shared buffers, leave all `$state` subscriptions in `App.svelte`. Rollback: revert.

### Step 17 — Delete orphan `deriveP95` / `deriveP50`

- **Addresses:** C-C7
- **Files:** `frontend/src/lib/state.svelte.js`.
- **Change:** Remove both exported functions. Run `pnpm build` to confirm no consumer. Grep the rest of `frontend/src` for the names.
- **Verification:** `pnpm -C frontend build` succeeds; `pnpm -C frontend test` (if present) passes.
- **Effort:** S
- **Risk:** None expected; callers already verified absent per synthesis. Rollback: restore.

---

## Issues NOT addressed

- **C-C6 (`store.go` cohesion)** — Synthesis marked it optional and LOW. Bundling it here widens the refactor PR without proportional benefit; defer to the next organic touch on `store.go`.

---

## Sequencing notes

- Phase 1 ships first because B1/B3/B6 directly protect the product before first external release. All four steps are now S-effort; they can batch into one PR.
- Phase 2 must precede Step 12 (Phase 4) — Step 12 removes helpers that Step 6 refactors.
- Phase 3 can ship in parallel with Phase 2 since they touch disjoint files.
- Phase 4 is a trailing cleanup PR.
- Step 4 no longer depends on Step 6 (migration writeback dropped — greenfield).
