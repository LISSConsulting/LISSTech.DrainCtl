# Operator Quickstart: Runtime Resilience and Diagnostics (012)

This runbook validates the release on an isolated Windows test host and explains how to handle a local `drainctld.exe` crash dump. It does **not** authorize automatic upload or external sharing of dumps.

## Safety first

- Use a test host or an approved maintenance window. Deliberately terminating a production service can interrupt monitoring and dashboard reporting.
- Crash dumps can contain credentials and user/process memory. Treat them as sensitive incident evidence.
- Do not put dumps in email, chat, tickets, telemetry, source control, dashboard uploads, or unapproved file shares. Copy them only after incident approval to the organization-approved restricted evidence store.
- Never change WER to create a larger dump merely to troubleshoot without explicit incident approval. This feature's supported default is a mini dump.

## 1. Confirm installation and recovery policy

From an elevated PowerShell prompt:

```powershell
Get-Service -Name DrainCtl
sc.exe qfailure DrainCtl
```

Expected policy:

| Failure count | Action | Delay |
|---:|---|---:|
| 1 | Restart | 5 seconds |
| 2 | Restart | 5 seconds |
| 3 | None | — |
| Reset period | — | 1 day |

The service runs as LocalSystem. Installer source of truth is `installer/LISSTech.DrainCtl.wxs`; the displayed value must match the table. If it does not, repair/reinstall the approved package and retain the `sc.exe` output with the installation record.

A normal `Stop-Service DrainCtl` is not a recovery test and should not create a crash dump.

## 2. Confirm WER local-dump setup and access controls

From the same elevated prompt:

```powershell
$dumpRoot = Join-Path $env:ProgramData 'LISS Technologies\LISSTech DrainCtl\dumps'
$werKey = 'HKLM:\Software\Microsoft\Windows\Windows Error Reporting\LocalDumps\drainctld.exe'
Get-ItemProperty -Path $werKey | Select-Object DumpFolder, DumpType, DumpCount
Get-Acl -Path $dumpRoot | Format-List Owner, AccessToString
Get-ChildItem -Force $dumpRoot | Select-Object Name, Length, CreationTimeUtc
```

Expected results:

- `DumpType` is `1` (mini dump) and `DumpCount` is `3`.
- `DumpFolder` points to the product's ProgramData `dumps` directory.
- Only LocalSystem and local Administrators have broad/full access. Investigate inherited Users, Everyone, Authenticated Users, or service-group access before allowing any dump collection.
- An empty directory before the first fault is normal.

If the registry key, directory, or safe ACL is absent, record the setup error and repair the approved installation. Do **not** point WER at a temporary, user-profile, network, or broadly writable fallback path.

## 3. Scheduled diagnostic collection

The disabled `\LISS Technologies\DrainCtl-Diags` task collects service, WER, event,
and **metadata-only** dump inventory into `diags`. The inventory contains only the
dump name, size, and UTC timestamp; by default it does not read dump bytes or
calculate hashes.

Set `DRAINCTL_INCLUDE_CRASH_DUMPS=1` only for an approved local investigation. The
task then hashes and gzip-copies only the newest `.dmp` **inside** `$dumpRoot`, writing
the hash sidecar there as well. Both artifacts inherit the protected SYSTEM and
Administrators-only dump ACL; no dump archive or hash is placed in `diags`, uploaded,
or otherwise shared. Seven-day task retention deletes only its `crash-dump-*.dmp.gz`
artifacts and hash sidecars; it never deletes WER-managed `.dmp` files.

Missing dump resources or an unreadable protected dump directory are reported in
diagnostics without making the scheduled collector fail the rest of its work. Repair
the approved MSI if the protected directory ACL is absent rather than relaxing it.

## 4. Controlled recovery smoke test (isolated host only)

> This exercise intentionally interrupts the monitoring service. Do not run it on a production host without explicit change approval.


1. Start with a healthy service and note its process ID:

   ```powershell
   Start-Service DrainCtl
   $before = Get-CimInstance Win32_Service -Filter "Name='DrainCtl'"
   $before | Select-Object Name, State, ProcessId
   ```

2. On an isolated test host, terminate only the displayed service process. Wait at least 10 seconds, then query the service again:

   ```powershell
   Stop-Process -Id $before.ProcessId -Force
   Start-Sleep -Seconds 10
   Get-CimInstance Win32_Service -Filter "Name='DrainCtl'" | Select-Object Name, State, ProcessId
   ```

   Expected: SCM has restarted the first failure after roughly five seconds. The new PID should differ.

3. Repeat once against the new PID and wait again. Expected: second restart.
4. Repeat a third time against the resulting PID. Expected: the service remains stopped; do not start it repeatedly as part of this test.
5. Inspect SCM events and local dump inventory:

   ```powershell
   Get-WinEvent -FilterHashtable @{ LogName = 'System'; Id = 7034,1067; StartTime = (Get-Date).AddMinutes(-15) } |
     Select-Object TimeCreated, Id, ProviderName, Message
   Get-ChildItem -Force $dumpRoot | Sort-Object CreationTimeUtc -Descending |
     Select-Object Name, Length, CreationTimeUtc
   ```

6. Restore service availability after recording the result:

   ```powershell
   Start-Service DrainCtl
   ```

Interpretation:

- Two restarts followed by stopped state validates SCM action ordering.
- Up to three dumps validates bounded WER retention. More than three requires investigation; do not add an application-side delete job.
- No dump can result from WER service/policy, disk, or access constraints. Preserve SCM evidence and investigate protected setup; do not improvise exfiltration.

## 5. Crash-dump incident workflow

1. **Stabilize**: determine whether SCM has exhausted its restart limit: it restarts only the first two unexpected exits in a one-day reset period, then leaves the service stopped after the third. If stopped, restore it only under the incident/change process.
2. **Locate and preserve metadata**: from an elevated PowerShell session, list the protected local directory without copying it:

   ```powershell
   Get-ChildItem -Force $dumpRoot | Sort-Object CreationTimeUtc -Descending |
     Select-Object Name, Length, CreationTimeUtc
   ```

   Record service state, SCM 7034/1067 events, `sc.exe qfailure` output, selected dump filename/size/creation time, and product/service version. Do not paste dump binary contents into the incident record.
3. **Confirm authorization**: an authorized local administrator confirms the incident identifier, retention decision, analysis workstation, and restricted evidence destination before a dump is copied.
4. **Copy, do not move**: copy only the authorized dump manually to the approved restricted evidence location. Keep the protected local original until the incident retention decision is made; do not relax ACLs to make it easier to inspect.
5. **Analyze offline**: use an approved debugger on the secured analysis workstation. Keep memory contents out of tickets, chat, telemetry, and dashboard data; record only approved, redacted findings in the incident record.
6. **Delete deliberately**: after incident approval and required retention, an authorized administrator deletes the exact local WER dump from `$dumpRoot`:

   ```powershell
   Remove-Item -LiteralPath (Join-Path $dumpRoot '<approved-dump-name>.dmp') -Force
   ```

   Verify the selected filename before running the command. The product never deletes WER-managed `.dmp` files automatically; only task-created opt-in gzip artifacts and hash sidecars have seven-day retention.

## 6. Freshness and offline-transition walkthrough

1. Configure a short safe test poll interval (for example 60 seconds) through the normal supported configuration path and allow it to take effect.
2. Confirm a registered host has a recent accepted report and is shown as its reported health state.
3. Prevent reports from that **test** host only, then wait at least three effective heartbeat intervals.
4. Observe one `host_offline` event in the authenticated dashboard SSE diagnostic stream or structured server log. Existing UI/server state should show offline.
5. Keep the host silent and repeat health queries. Expected: it remains offline without another event for the same last-report epoch, including after a dashboard restart.
6. Restore reporting. Expected: one `host_recovered` event followed by the compatible normal `server_update`; the reported health state is visible again.
7. Change the configured interval during a controlled test. Expected: the freshness worker wakes immediately, replaces its prior wait, and evaluates the new `3 × effective interval` boundary without restarting the dashboard listener.

Do not use agent-provided timestamps to judge this test. The dashboard acceptance time is authoritative.

## 7. RemoteFX data-quality walkthrough

1. Enable optional RemoteFX collection only on a test host that has the relevant role/provider available.
2. In the dashboard, distinguish these states:
   - **No stream / zero FPS or quality**: chart gap; not a 0-quality failure.
   - **Provider unavailable or missing counter**: absent RemoteFX data; core host telemetry continues.
   - **Valid stream**: FPS and quality show service-floor P95 (numeric P5) and P50 median; latency/loss/skips show conventional P95 and P50.
3. Inspect structured diagnostic signals for invalid RemoteFX values. A sentinel-like encoding value, NaN, infinity, negative value, or out-of-range value must be dropped, not capped or graphed.
4. Confirm other valid RemoteFX fields and core metrics in the same report remain visible. A malformed optional field must not make the host offline or discard the entire performance record.

## 8. Rolling upgrade and rollback

### Upgrade

1. Back up normal configuration/audit data under the established product procedure; do not include dump contents in routine backups unless the incident policy explicitly requires it.
2. Upgrade with the approved MSI. MSI owns stop/install/start sequencing.
3. Re-run sections 1 and 2 to confirm recovery policy and protected WER configuration.
4. Confirm both an older compatible agent and a new agent can report core state to the upgraded dashboard. New optional diagnostic fields may be absent on old agents.

### Rollback

1. Stop only through the approved installer/change process; do not induce crashes to roll back.
2. Install the approved prior package. Preserve `config.json`, the telemetry database, and the protected dumps directory unless an incident owner directs otherwise.
3. Confirm core host reporting after rollback. Earlier software may ignore additive freshness records or newer optional fields; it must not interpret their absence as a zero or a deletion.
4. Keep existing dumps under the incident retention decision. Rollback does not authorize deletion or remote collection.

## Troubleshooting table

| Observation | Likely interpretation | Operator action |
|---|---|---|
| Service did not restart after first/second forced exit | Recovery policy missing, failure budget already exhausted, or SCM could not start it | Inspect `sc.exe qfailure`, System event logs, and installation logs; repair approved package. |
| Service stopped after third exit | Expected recovery-budget behavior | Start only through incident/change process; inspect local evidence. |
| No dump after fault | WER policy/service/storage/ACL issue, or fault did not reach WER | Inspect key, ACL, WER operational logs, free space, and SCM events; do not add upload tooling. |
| More than three dumps | Retention/configuration mismatch | Preserve evidence and investigate WER configuration; do not add concurrent cleanup code. |
| Multiple offline events without a new report | Freshness deduplication defect | Preserve SSE/log timestamps and report to engineering with metadata only. |
| Offline sooner/later than expected | Effective heartbeat interval mismatch | Inspect the active dashboard interval and use exactly three times that interval. |
| RemoteFX zero plotted as a line | Inactive-stream gap rule violated | Capture counter name/timestamp/version metadata; do not treat as a service-quality alert. |
| Very large RemoteFX time value plotted | Outlier validation missing/bypassed | Capture metric name/version metadata; field should be dropped rather than clamped. |
