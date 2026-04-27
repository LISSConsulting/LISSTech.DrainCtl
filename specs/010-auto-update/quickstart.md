# Quickstart: Agent Self-Poll Auto-Update (010)

Manual walkthrough for the operator-facing surface and the developer-facing smoke test. Kept as a checklist so it doubles as the pre-merge gate (see [tasks.md §"Commit 13"](./tasks.md)).

## Operator quickstart

After installing a release that includes this feature (010 or later), auto-update is **off by default**. The agent does not contact GitHub until you opt in. To enable:

1. Open `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json` in an editor as Administrator.
2. Add or edit the `update` object. Defaults are `{enabled: false, channel: "stable", poll_interval: "24h"}`.
3. **Today (while every release ships as prerelease)**, both flags are required for any auto-update activity:
   ```json
   "update": {
     "enabled": true,
     "channel": "prerelease",
     "poll_interval": "24h"
   }
   ```
   Save and the service reloads within seconds — no restart needed.
4. **Once the project graduates a non-prerelease release**, the operationally useful default flips to:
   ```json
   "update": {
     "enabled": true,
     "channel": "stable"
   }
   ```
   which tracks only non-prerelease releases. Operators who want the early channel keep `"channel": "prerelease"`.
5. Recognized values for `channel` are `"stable"` and `"prerelease"`. Any other value (including typos like `"beta"` or `"latest"`) is silently clamped to `"stable"` with a `slog.Warn` in the daily log.
6. Confirm the updater is alive by tailing the daily log (set `log_file_level=debug` to see poll lines):
   ```
   %ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl-YYYY-MM-DD.log
   ```
   Look for `update=poll_start` lines at the configured interval (after the 5–15 min initial delay).
7. To change the cadence: set `"poll_interval"` to any Go duration string ≥ `"1h"` (e.g., `"6h"`, `"12h"`, `"72h"` for 3 days). Values below `"1h"` are clamped to `"1h"` with a `slog.Warn`.
8. To opt back out at any time: set `"enabled": false`. The poll loop sees the change on its next tick and skips the GitHub call.
9. Verify auto-update has fired by grepping the daily file log (durable audit-store row deferred to a follow-up — see spec.md FR-011):
   ```
   Get-Content '$env:ProgramData\LISS Technologies\LISSTech DrainCtl\drainctl-*.log' | Select-String 'update=installing'
   ```
   One `update=installing old=… new=…` line appears per version transition. Lines persist for 7 days (the file-log rotation window).

## What the operator does NOT need to do

- **Nothing** if you don't want auto-update. The default is off; install 010, do nothing, behavior is identical to pre-010 hosts (manual `irm install.ps1 | iex` for every fix).
- No registry edits, no scheduled tasks, no group policy. The single `update` object in `config.json` is the entire surface.

## Developer smoke test (pre-merge)

Before merging the 010 PR, run this on one fresh Windows VM:

### Setup

1. VM running Windows 10 or Server 2019+, joined to whatever domain you usually test against (or workgroup is fine).
2. Latest release MSI (the one before this PR — call it `Vbase`) installed via the standard `irm install.ps1 | iex` path.
3. Confirm baseline: `drainctl --version` reports `Vbase`.

### Step 1: install the 010 build

1. From the dev machine, run `just release` against the 010 branch.
2. Copy `dist\LISSTech.DrainCtl\msi\LISSTech.DrainCtl.msi` to the VM.
3. On the VM (elevated PowerShell): `msiexec /i path\to\msi /quiet /norestart`.
4. Confirm `drainctl --version` reports the 010 build's version (call it `V010`).

### Step 2: confirm the updater is alive but idle

1. **First**: edit `config.json` and add `"update": {"enabled": true, "channel": "prerelease", "poll_interval": "24h"}`. (Default is off — without this step the rest of the smoke test will silently no-op.)
2. Wait 16 minutes (covers the longest-jitter initial-delay).
3. `Get-Content '%ProgramData%\LISS Technologies\LISSTech DrainCtl\drainctl-{date}.log' | Select-String 'update='`.
4. Expected lines (in this order):
   - `update=poll_start url=https://api.github.com/... current=V010`
   - One of: `update=up_to_date current=V010` (if no newer release exists), or `update=installing remote=…` (if a newer release exists upstream), or `update=no_stable_release` (if `channel="stable"` and only prereleases exist — should NOT see this if you set `channel="prerelease"`).

If you see no `update=` lines at all, the updater isn't running. Likely causes:
- `update.enabled=false` (default) — confirm step 1.
- Log level is `info`; poll lines are `slog.Debug`. Set `log_file_level=debug` and wait one more cycle.
- Network egress is blocked for the SYSTEM account.
- The Subsystem failed to Start. Check for an `slog.Error` line near service start.

### Step 3: simulate a newer release

Two options.

**Option A: synthetic via fork**. Fork the repo, push a release with a bumped tag (e.g., `v99.99.99`) and a copy of the V010 MSI as the asset. Point the updater at your fork temporarily by editing the URL constant in `internal/updater/github_windows.go` and rebuilding for this test only. Restart the service. Wait one poll tick.

**Option B: synthetic via constant override**. Add a build-tag-gated `var apiURL = "..."` to `github_windows.go` so a `dev` build uses a localhost httptest URL. Run a small Go program on the VM that serves the GitHub-shaped JSON pointing at a local MSI. Restart the service.

Either way, the expected sequence in the log is:

```
update=poll_start url=...
update=installing remote=v99.99.99
update=verified subject=LISS Consulting, Corp.
update=installed_pending_restart remote=v99.99.99
```

Then the service stops (its own ctx cancel), msiexec runs, and within ~30-60s the service is restarted by the MSI custom action at the new version.

### Step 4: confirm the version transition is durable

1. `drainctl --version` reports the new version.
2. `drainctl history --since 1h --filter event=auto_update_install` shows the row with `old=V010 new=...`.
3. Selfmetrics log lines resume normally; goroutines flat at the expected baseline.

### Step 5: stress the refusal path

1. Build a copy of the MSI signed with a self-signed test cert (`signtool sign /f testcert.pfx /p testpass dist/.../LISSTech.DrainCtl.msi`).
2. Point the test-fork release at this MSI instead of the real one.
3. Restart the service, wait for the next poll.
4. Expected log: `update=refused reason=subject_mismatch subject=...`. The temp file is gone (`Get-ChildItem $env:TEMP\drainctl-update-*.msi` returns nothing). The service is still running at V010. No msiexec spawn occurred (`Get-Process msiexec` shows nothing).

### Step 6: stress the network-failure path

1. On the VM: `New-NetFirewallRule -DisplayName "Block GitHub for drainctl test" -Direction Outbound -RemoteAddress 140.82.0.0/16 -Action Block` (or DNS-block `api.github.com` via hosts file).
2. Restart the service. Wait through three poll attempts (the first at 5–15 min, then backoff: +5m, +15m).
3. Expected: each attempt logs an error with the network-error string. Service stays up. No crash, no msiexec, no temp file leak.
4. Remove the firewall rule. Within the next backoff interval, a poll succeeds.

## Troubleshooting

- **No `update=` lines at all in the daily log**: most likely `update.enabled=false` (the default — auto-update is opt-in). If `enabled=true`, the next likeliest cause is log level: poll lines are `slog.Debug`, so set `log_file_level=debug` in `config.json` to see them.
- **`update=no_stable_release` every poll, even though there ARE releases on GitHub**: the operator has `channel="stable"` but every release upstream is flagged prerelease. Either flip `channel="prerelease"` to track the prerelease channel, or wait for the project to graduate a non-prerelease release.
- **`update=refused reason=signature_missing` on every poll**: the upstream release's MSI is unsigned (someone shipped without `just release`). Fix on the release side, not the agent side.
- **`update=refused reason=subject_mismatch subject=<unexpected>`**: someone re-signed the MSI with a different cert. Verify the release was built and signed by the project's normal release flow. If the cert was rotated legitimately, the agent side needs a code update (the Subject CN constant in `verify_windows.go`).
- **Service installs the new version but immediately crashes back to old**: the new version has a startup bug; SCM Recovery is restarting it but the new binary keeps crashing. Manual rollback: install the old MSI, set `update.enabled=false` until the upstream regression is fixed.
