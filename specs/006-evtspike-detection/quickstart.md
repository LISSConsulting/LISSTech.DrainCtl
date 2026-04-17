# Quickstart: evtspike

Stand up the detector on a test RDSH, verify detection and notifications, then (optionally) enable the Security channel opt-in. Assumes you have a build of the DrainCtl MSI that includes this feature. The feature ships only as an in-service subsystem; there is no standalone CLI in MVP (scope-reduced 2026-04-17).

---

## Prerequisites

- Windows Server 2019+ with the RDSH role installed.
- DrainCtl service account has its default privileges.
- At least one webhook target you can watch (e.g., a `webhook.site` URL, an ntfy topic you control, or a local SMTP catcher).
- Admin shell (elevated PowerShell).

---

## Path A — Built-in subsystem (primary path)

### 1. Install DrainCtl normally

```powershell
msiexec /i drainctl-v26.106.x-x64.msi
```

The detector ships disabled. No installer-time Security opt-in — that is now a config flag (Path C).

### 2. Turn the feature on

Edit `C:\ProgramData\LISS Technologies\LISSTech DrainCtl\config.json`, add the `evtspike` block:

```json
"evtspike": {
  "enabled": true
}
```

Save. The service picks this up on its next config reload (within seconds; no restart required).

Confirm in the service log:

```powershell
Get-Content 'C:\ProgramData\LISS Technologies\LISSTech DrainCtl\drainctl.log' -Tail 50 -Wait
```

Look for: `evtspike: subscribed to 53/54 channels` (Security is skipped because you didn't opt in — expected). A typical install will show 53 of 54; channels that the host's Windows image didn't install are also skipped with a warning.

### 3. Wire up a notification target

Add a webhook (or other target) subscribed to the `event_spike` trigger:

```json
"notifications": [
  {
    "type": "webhook",
    "url": "https://webhook.site/<your-bin>",
    "triggers": ["event_spike"],
    "repeat_minutes": 30
  }
]
```

`event_spike` is **not** in `DefaultTriggers`, so you must list it explicitly. Save. Service reloads.

### 4. Inject a spike

Pick a channel from the default list that's normally quiet — `Microsoft-Windows-Winlogon/Operational` is a good choice because the channel exists on every RDSH and typically sits near zero outside logon storms.

Write 50 events in a burst, then another 50 one minute later:

```powershell
for ($i = 1; $i -le 50; $i++) {
  eventcreate /T ERROR /ID 999 /L Application /SO 'evtspike-smoke' /D "burst 1 iteration $i" | Out-Null
}
Start-Sleep -Seconds 60
for ($i = 1; $i -le 50; $i++) {
  eventcreate /T ERROR /ID 999 /L Application /SO 'evtspike-smoke' /D "burst 2 iteration $i" | Out-Null
}
```

*(`eventcreate` targets the Application channel, not Winlogon. For a Winlogon test, `New-WinEvent -ProviderName 'Microsoft-Windows-Winlogon' ...` requires the provider to be registered — you'd need to register a test provider. For smoke-test purposes, use Application; it's in the default watched list.)*

### 5. Verify

Check three places:

- **Webhook**: the target receives one POST with `"event": "event_spike"` and a `spike` sub-object whose `channel` is `"Application"` and `observed` is ~50.
- **Dashboard**: the server card shows `Training` or `Healthy` status pill. Open the server detail — the recent-spikes list shows the Application spike.
- **Log**: `drainctl.log` has an entry like `evtspike: confirmed spike channel=Application observed=50 expected=~0.3 tail=1.2e-23`.

### 6. Verify non-spikes are quiet

Wait 10 minutes. Without further bursts, no additional notifications fire (cooldown). The baseline's robust cap means the spike doesn't poison the Application baseline — a follow-up 30-event burst tomorrow will still fire.

### 7. Turn it off

Flip `enabled: false` in config or remove the block entirely. Baseline file is retained — if you turn it back on later, warm-restart kicks in and you don't re-enter warm-up.

---

## Path B (DROPPED — standalone CLI scope-reduced 2026-04-17)

---

## Path C — Enable Security channel monitoring (config flag)

**Read the Security implications in the spec's Clarifications section before doing this.** This enables the DrainCtl service to read the Security log, clear the Security log, manage audit policy, and set SACLs on this host. Only enable if you understand and accept this expanded capability.

### 1. Flip the flag in config.json

Edit `C:\ProgramData\LISS Technologies\LISSTech DrainCtl\config.json`, inside the `evtspike` block:

```json
"evtspike": {
  "enabled": true,
  "security_channel_enabled": true
}
```

Save. The service detects the change and restarts the evtspike subsystem within a few seconds (logged as `evtspike: restarting for config change`). No service restart is required.

### 2. Verify

Look for these lines in `drainctl.log`:

```
evtspike: enabling SeSecurityPrivilege on service token
evtspike: subscribed to 54/54 channels (Security included)
```

Confirm the subscribed-channel count moved from 53 to 54, and that `Security` appears in the list.

### 3. If you're running under a dedicated service account

LocalSystem (DrainCtl's default account) has `SeSecurityPrivilege` present in its token (Disabled state). If you have reconfigured DrainCtl to run under a dedicated account, that account will not have this privilege by default and enabling the flag will produce:

```
evtspike: AdjustTokenPrivileges returned ERROR_NOT_ALL_ASSIGNED; skipping Security subscription
```

Grant the privilege manually:

```powershell
# Option 1: secedit
secedit /configure /cfg <policy.inf> /db <temp.sdb>   # where policy.inf assigns SeSecurityPrivilege to your account

# Option 2: group policy (Local Security Policy → Local Policies → User Rights Assignment → Manage auditing and security log)

# Then restart the DrainCtl service
Restart-Service DrainCtl
```

Other channels continue to operate regardless — the failure is scoped to Security only.

### 4. Disable

Set `security_channel_enabled: false` in config.json (or remove the field). On next reload the subsystem restarts and `Security` disappears from the subscribed list. No LSA operation is performed — LocalSystem's built-in `SeSecurityPrivilege` is untouched.

---

## Troubleshooting

### "evtspike: subscribed to 0/54 channels"

The service account can't read any event log. Verify the service is running as `LocalSystem` (default) or an account in the `Event Log Readers` group. Check `sc.exe qc DrainCtl`.

### "evtspike: baseline file corrupt; renaming to .corrupt-YYYYMMDD-HHMMSS.bak"

Expected after a hard crash mid-write (very rare). The detector re-enters warm-up; the `.bak` file can be deleted once you've confirmed the replacement is good.

If the log instead says `baseline file unreadable; starting fresh` with NO rename, the file was temporarily locked (antivirus, backup agent, indexer) and the detector deliberately left it in place — on the next write the detector will overwrite it normally. If you see this repeatedly, check for an AV exclusion path for `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike-baseline.json`.

### Webhook gets no POST

Check that your notification target lists `"event_spike"` in its `triggers` array. It is NOT in `DefaultTriggers` — an upgrade to a version that includes this feature does not silently start firing new events against targets that didn't ask for them.

### Dashboard pill stuck on `Training`

That's the default for the first ≈1 week after install (per-slot baselines haven't matured). If you need faster maturity on a test host, lower `slot_maturity_observations` in config.json (e.g., to `1`). Do not do this in production.

### "evtspike: confirmed spike ... but no notification fired"

Likely explanations, in order of probability:
1. No notification target lists `event_spike` in its triggers.
2. Cooldown: you fired another spike on the same (host, channel) within `cooldown_minutes`.
3. Target's `repeat_minutes` hasn't elapsed since the last spike notification for this pair.
4. Target is `enabled: false`.

---

## What to check before calling it "done"

- [ ] Path A end-to-end: enable, inject, notification received, dashboard pill transitions.
- [ ] Path A warm restart: restart service, spikes fire again within one scoring window, no alert storm.
- [ ] Path A robust cap: inject a 30-minute flood, wait, then inject a small burst — the small burst still alerts.
- [ ] Path C LocalSystem opt-in: set `security_channel_enabled: true`, verify subscription count goes from 53 to 54, verify no privilege error in logs.
- [ ] Path C disable: set back to `false`, verify Security is dropped on next reload.
- [ ] Steady-state resource usage matches SC-007 (<5% of one core, <50 MB memory, ≤13 MB/day baseline write volume).
