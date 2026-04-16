# Quickstart: evtspike

Stand up the detector on a test RDSH, verify detection and notifications, then (optionally) enable the Security channel opt-in. Assumes you have a build of the DrainCtl MSI that includes this feature.

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

Do **not** select the Security event log monitoring feature. The detector ships disabled.

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

## Path B — Standalone CLI (sidecar / no full-service deployment)

### 1. Drop the CLI on the host

Copy `evtspike.exe` to e.g. `C:\Tools\evtspike\` on a host that either (a) has DrainCtl installed or (b) doesn't but you still want local visibility.

### 2. Interactive smoke

```powershell
cd C:\Tools\evtspike
.\evtspike.exe
```

Watch stdout. Every 10 seconds you'll see one line per subscribed channel showing counts. Induce a spike with the `eventcreate` loop from Path A step 4 and verify `[ALERT]` lines appear on stdout.

If DrainCtl is running on the same host, the CLI also forwards the spike over `\\.\pipe\drainctl` — check DrainCtl's logs / webhook for the same spike arriving via the service. If DrainCtl is not running, the CLI just logs locally (no crash, no retry spin).

### 3. Install as a Windows service

```powershell
.\evtspike.exe install-service
Start-Service EvtSpike
Get-Service EvtSpike
```

The service is now running under `LocalSystem`. Its config and baseline file locations mirror DrainCtl's, but scoped to this service.

### 4. Uninstall

```powershell
Stop-Service EvtSpike
.\evtspike.exe uninstall-service
```

---

## Path C — Enable Security channel monitoring (elevated privilege)

**Read the Security implications in the spec's Clarifications section before doing this.** You are granting `SeSecurityPrivilege` to the service account, which expands its capability to read and clear the Security log and alter audit policy.

### 1. Install (or modify install) with the feature selected

Fresh install:

```powershell
msiexec /i drainctl-v26.106.x-x64.msi ADDLOCAL=SecurityEventLog
```

Modify an existing install:

```powershell
msiexec /i drainctl-v26.106.x-x64.msi ADDLOCAL=SecurityEventLog REINSTALL=ALL REINSTALLMODE=omus
```

Or: run the installer interactively, click Modify on the Features page, and check "Enable Security event log monitoring".

### 2. Verify the grant

```powershell
# Marker file placed by the MSI:
Test-Path 'C:\ProgramData\LISS Technologies\LISSTech DrainCtl\evtspike\security_enabled'

# Privilege granted to the DrainCtl service account (LocalSystem by default):
whoami /user /priv   # run interactively as LocalSystem via PsExec if needed
```

### 3. Restart the DrainCtl service

```powershell
Restart-Service DrainCtl
```

Log should show: `evtspike: security_enabled marker present; Security channel added to watched list` followed by the normal subscription count, now `54/54`.

### 4. Revoke

Remove the feature:

```powershell
msiexec /i drainctl-v26.106.x-x64.msi REMOVE=SecurityEventLog REINSTALL=ALL REINSTALLMODE=omus
```

Verify: marker file gone, `whoami /priv` no longer shows `SeSecurityPrivilege`, service log shows `Security` missing from the subscribed list on next restart.

---

## Troubleshooting

### "evtspike: subscribed to 0/54 channels"

The service account can't read any event log. Verify the service is running as `LocalSystem` (default) or an account in the `Event Log Readers` group. Check `sc.exe qc DrainCtl`.

### "evtspike: baseline file corrupt; renaming to .corrupt-YYYYMMDD-HHMMSS.bak"

Expected after a hard crash mid-write (very rare). The detector re-enters warm-up; the `.bak` file can be deleted once you've confirmed the replacement is good.

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
- [ ] Path B standalone → service: install CLI, induce spike, service-side webhook fires.
- [ ] Path B no-service fallback: stop DrainCtl, induce spike, CLI logs locally without crashing.
- [ ] Path C install-time opt-in: feature selected in UI grants `SeSecurityPrivilege` and adds marker file; service subscribes to Security.
- [ ] Path C uninstall: feature removal revokes the privilege and removes the marker file.
- [ ] Steady-state resource usage matches SC-007 (small single-digit % CPU, <50 MB memory).
