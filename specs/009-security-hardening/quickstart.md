# Quickstart: Security and Correctness Hardening (009)

## Goal

Validate that each of the 5 user stories from `spec.md` ships correctly end-to-end.
Each section below is independently testable: you can run Story 1's checks without
Stories 2-5 being complete, and vice versa, because they ship as separate PRs.

## Prerequisites

- A dev Windows box with drainctl built from branch `009-security-hardening`
- `just release` completed so the MSI + binaries are current
- A dashboard build running locally (or on a reachable host) for register flows
- An SMTP test harness (mailhog, Papercut, or a small in-process Go server in `email_test.go`)
- Admin and non-admin Windows user accounts for pipe-access tests

---

## User Story 1 Validation — Fail-closed credential paths (P1)

### Dashboard fingerprint mismatch refusal

1. Install drainctl fresh. Run `drainctl register https://dash.example`. Confirm
   success and that `config.json` now has a non-empty `Dashboard.TLSFingerprint`.
2. Rotate the dashboard's TLS certificate (replace with a freshly generated
   self-signed cert on the dashboard host).
3. Run `drainctl register https://dash.example` again without touching `config.json`.
4. Confirm:
   - Exit code is non-zero.
   - Error output contains `fingerprint mismatch` and both hex fingerprints.
   - `config.json`'s `TLSFingerprint` is the **original** (pre-rotation) value.
5. Check the service log (`drainctl.log`): for the background re-register path,
   confirm `level=ERROR msg="dashboard=fingerprint-mismatch" saved=<…> offered=<…>`.
6. Edit `config.json` to clear `TLSFingerprint`. Re-run register. Confirm success
   and that the field is now the rotated fingerprint.

### SMTP STARTTLS refusal

1. Configure a notification target of type `smtp` with a non-empty `Secret` (any
   password) pointing at a test SMTP server that **does not advertise STARTTLS** in
   its EHLO response (mailhog on port 1025 is a quick way to reproduce; a stubbed
   in-process server is better for automation).
2. Trigger a notification via the dashboard's **Test** button, or fire the send path
   directly via unit test.
3. Confirm:
   - The send fails with an error containing `refusing cleartext AUTH` and the host.
   - No `AUTH` command was sent on the wire (verify with `tcpdump`/Wireshark on
     loopback, or by inspecting the mock server's recorded commands).
4. Change the SMTP server to advertise STARTTLS and accept it. Retry. Confirm the
   send succeeds.
5. Change `Secret` to empty on the notification target. Retry against a no-STARTTLS
   server. Confirm:
   - The send succeeds (opportunistic cleartext).
   - The service log contains `level=WARN msg="smtp: no STARTTLS available, sending without encryption" host=<…>`.

---

## User Story 2 Validation — Narrowed credential blast radius (P1)

### Config.json ACL

1. Uninstall drainctl, then reinstall the 009 MSI on a fresh box (or delete
   `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` and restart the
   service to recreate it).
2. Run `icacls "%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json"`.
3. Confirm:
   - SYSTEM has `(F)` or `(M)`.
   - Administrators has `(F)`.
   - **`NT AUTHORITY\SERVICE` (`S-1-5-6`) is absent**.
   - No other groups beyond the above.

### DPAPI entropy

1. With drainctl running, configure a webhook target and enter a secret. Save.
2. Stop the service. Inspect `config.json`'s `Secret` field — confirm it's DPAPI-
   encrypted (starts with the `dpapiPrefix`).
3. Start the service. Trigger a notification. Confirm the secret decrypts and the
   webhook fires.
4. **Negative test** (via Go test, not manual): craft a ciphertext by calling
   `CryptProtectData` directly with a *different* entropy constant (e.g. empty or
   `"wrong"`). Attempt to decrypt via `DPAPIDecrypt`. Confirm it returns an error.

---

## User Story 3 Validation — Long-running pipelines don't silently break (P1)

### Evtspike loss callback

1. Enable evtspike in `config.json` with at least one channel configured.
2. Start the service. Confirm spike detection is live (inject a synthetic event
   storm, watch for the spike notification).
3. In a Go test (`subscriber_test.go`), invoke `Subscribe` against a deliberately
   closed subscription handle (inject via a test-only setter or by calling
   `procEvtClose` on `sub` after `Subscribe` returns). Assert that the `loss`
   callback fires within 2 seconds with a non-nil error.
4. Confirm the supervisor then re-subscribes (observable via log signals already
   present in `internal/evtspike/subsystem.go`).

### Concurrent config RMW

1. In a Go test (`config_test.go`), spawn 20 goroutines that each call
   `UpdateSessionThreshold(rand.Intn(100))` concurrently. Wait.
2. Read the final `config.json` and assert:
   - It parses as valid JSON.
   - The `SessionThreshold` value equals exactly one of the values any goroutine wrote.
3. Additionally, run a test with two goroutines — one calling `UpdateSessionThreshold`
   and the other calling `UpdateGracePeriod` — and assert both changes are present
   in the final file.

### Pipe message size handling

1. In a Go test (`pipe_test.go`), use the existing `scriptedReader` pattern to
   simulate a pipe request that delivers data via multiple `ERROR_MORE_DATA` chunks
   totaling >4 KB.
2. Assert the server-side handler receives the full concatenated message.
3. Run a cap test with a reader that keeps returning `ERROR_MORE_DATA` past 1 MiB.
   Assert the read returns a specific cap error.

---

## User Story 4 Validation — Medium security paper cuts (P2)

### Cloud metadata IP rejection

1. Open the dashboard, navigate to notification targets.
2. Attempt to add a webhook target with URL `http://169.254.169.254/latest/meta-data/`.
3. Confirm the save fails with a clear error message naming the metadata IP.
4. Attempt again with `http://192.168.1.10/x`. Confirm save succeeds (LAN allowed).
5. Click **Test** on the `192.168.1.10` target; expect the test to succeed
   (or fail cleanly with a network error if no server is listening — not the
   metadata-IP rejection error).

### Pipe caller-SID check

1. From an elevated (admin) shell, run `drainctl register https://dash.example`.
   Confirm the register proceeds (or fails for an unrelated reason like bad URL —
   not `access denied`).
2. From a non-admin shell (run as a standard user without UAC elevation), run
   `drainctl register https://dash.example`. Confirm the pipe call returns
   `access denied`.
3. From the same non-admin shell, run `drainctl status`. Confirm it works.
4. Check the service log for `level=WARN msg="pipe=access_denied" cmd=register sid=S-1-5-…`.
5. Check the ETW audit channel for an `EvtAccessDenied` event with matching payload.

### Email HTML escaping

1. In a Go test (`email_test.go`), render the notification template with
   `Message: "<script>alert(1)</script>"` and `ChangedBy: "<b>admin</b>"`.
2. Assert the rendered body contains `&lt;script&gt;` and `&lt;b&gt;`, not the
   raw tags.
3. Trigger a real email via the dashboard Test button with a similarly crafted
   message. View the resulting email in the mailbox and confirm the tags appear
   as text, not as rendered HTML.

### Registry attribution SystemTime

1. In a Go test (`evtsubscribe_test.go`), feed `processEvent` a synthetic XML
   payload with `<TimeCreated SystemTime="2026-04-24T10:00:00Z"/>`.
2. Assert `LatestAttribution().Timestamp` equals the parsed time (not `time.Now()`).
3. Call `WaitAttribution(after: parsedTime + 5 * time.Second, timeout: 100 * time.Millisecond)`.
4. Assert the wait returns `""` (attribution ignored because it's older than `after`).
5. Negative test: feed a malformed `SystemTime`. Assert `Timestamp` falls back to
   `time.Now()` and the service log contains
   `level=WARN msg="evtspike: unparseable SystemTime" raw=<…>`.

---

## User Story 5 Validation — Surface drift cleanup (P3)

### `Update*` helpers gone

1. Run `grep -r "UpdateNotifications\|UpdateSessionThreshold\|UpdateGracePeriod\|UpdatePerformanceConfig"` on the repo.
2. Confirm zero hits (or only hits inside the commit message / release notes).
3. Run `go build ./...` and `go test ./...`. Both pass.

### ETW IDs moved

1. `grep -n EvtDashboardAccess drainctl.go` → empty.
2. `grep -rn EvtDashboardAccess internal/etwids` → one hit (the declaration).
3. `go build ./cmd/cshared/...` succeeds (DLL does not reference the IDs).

### `DefaultDBPath` + history widening

1. `drainctl history --limit 1` against an existing telemetry database → returns a row.
2. `drainctl history --db %ProgramData%\LISS Technologies\LISSTech DrainCtl` (a dir
   path) → same result.
3. `drainctl history --db %ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl.db`
   (a file path) → same result.
4. `go doc drainctl.DefaultAuditPath` shows the `// Deprecated:` notice.

### `handler.go` split

1. `wc -l internal/svc/handler*.go` shows no single file exceeds 400 lines.
2. `go build ./... && go test ./internal/svc/...` passes.
3. `git diff --stat HEAD~1 HEAD -- internal/svc/handler*.go` on the split commit
   shows near-zero net line delta (pure moves).

### Svelte helpers + orphan deletion

1. `grep -n "deriveP95\|deriveP50" frontend/src/lib/state.svelte.js` → empty.
2. `pnpm -C frontend build` succeeds.
3. Open the dashboard. Trigger a drain-mode transition on the test host.
4. Confirm the transition appears in the transitions list exactly once (not twice —
   the old duplicated blocks would have logged twice on SSE + poll overlap).

---

## Cross-cutting verification (before PR merge)

For every phase PR:

```powershell
go build ./...
go test ./...
just lint
just vulncheck
pnpm -C frontend build
```

All must exit 0 with no warnings.

Pre-commit (`prek run --all-files`) must pass before every commit.

For the final release that includes all of 009, bump via `just release` and spot-check
the installed MSI on a fresh VM using the Story 2 ACL check and Story 1 SMTP negative
test.
