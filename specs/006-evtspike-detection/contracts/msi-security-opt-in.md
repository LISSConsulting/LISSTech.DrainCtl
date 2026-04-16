# Contract: MSI Security Event Log Opt-In Component

**Scope**: New WiX 5 feature inside the existing DrainCtl `.wixproj` that gates `SeSecurityPrivilege` grant on an explicit admin opt-in during install.

---

## Feature definition

```xml
<Feature Id="SecurityEventLog"
         Title="Enable Security event log monitoring"
         Description="Lets the DrainCtl service subscribe to the Windows Security channel for anomaly detection. Grants SeSecurityPrivilege to the service account. Only enable if you understand that this expands the service's ability to read the Security log, clear the Security log, alter audit policy, and set SACLs on this host."
         Level="1000"
         TypicalDefault="install"
         InstallDefault="local"
         AllowAdvertise="no">
  <ComponentRef Id="SecurityOptInMarker"/>
</Feature>
```

- `Level="1000"` — off by default; admin must explicitly select.
- `TypicalDefault="install"` — if selected, installed locally (not advertised).
- `AllowAdvertise="no"` — no "Install on first use" — either it's present or not.

## Component: `SecurityOptInMarker`

```xml
<Component Id="SecurityOptInMarker"
           Guid="{STABLE-GUID}"
           Directory="EvtSpikeDataDir">
  <File Id="SecurityEnabledMarker"
        Source="$(var.MarkerFile)"
        Name="security_enabled"
        KeyPath="yes"/>
</Component>
```

- Installs a zero-byte marker file at `%ProgramData%\LISS Technologies\LISSTech DrainCtl\evtspike\security_enabled`.
- `KeyPath="yes"` — the marker file's presence is the authoritative signal for Windows Installer that this component is installed.
- The Go service reads this file at startup to drive `ResolveChannels(securityOptIn: true)`.

## Custom actions

Two CAs wired into `InstallExecuteSequence`. Implementation: a small C# DLL compiled for .NET 4.8 (the runtime available on all targeted Windows Server versions without additional install), P/Invoking `Advapi32.LsaAddAccountRights` / `Advapi32.LsaRemoveAccountRights`.

### `GrantSeSecurityPrivilege`

```xml
<CustomAction Id="GrantSeSecurityPrivilege"
              BinaryRef="SecurityCA"
              DllEntry="GrantPrivilege"
              Execute="deferred"
              Impersonate="no"
              Return="check"/>

<InstallExecuteSequence>
  <Custom Action="GrantSeSecurityPrivilege" After="InstallFiles">
    <![CDATA[&SecurityEventLog=3 AND NOT (Installed AND !SecurityEventLog=3)]]>
  </Custom>
</InstallExecuteSequence>
```

**Condition plain-English**: run when the feature is being installed fresh, or being added on a modify (NOT already present).
**Execute deferred + Impersonate="no"**: must run with elevated (LocalSystem) context because `LsaAddAccountRights` requires `SeSecurityPrivilege` itself.

**What the DLL does**:
1. Look up the SID of the DrainCtl service account — which is `LocalSystem` by default. For admins who reconfigured the service to run as a different account, the CA reads the account from the service registration via `QueryServiceConfig`.
2. Open a local LSA handle with `POLICY_CREATE_ACCOUNT | POLICY_LOOKUP_NAMES`.
3. Call `LsaAddAccountRights(policyHandle, sid, &{ "SeSecurityPrivilege" }, 1)`.
4. Close LSA handle.
5. Return `ERROR_SUCCESS` (installer aborts on non-zero).

### `RevokeSeSecurityPrivilege`

```xml
<CustomAction Id="RevokeSeSecurityPrivilege"
              BinaryRef="SecurityCA"
              DllEntry="RevokePrivilege"
              Execute="deferred"
              Impersonate="no"
              Return="check"/>

<InstallExecuteSequence>
  <Custom Action="RevokeSeSecurityPrivilege" Before="RemoveFiles">
    <![CDATA[(!SecurityEventLog=3 AND &SecurityEventLog=2) OR (REMOVE="ALL")]]>
  </Custom>
</InstallExecuteSequence>
```

**Condition plain-English**: run when the feature is being removed on a modify, OR when the whole product is being uninstalled.
**What the DLL does**: same LSA open + `LsaRemoveAccountRights(policyHandle, sid, allRights: 0, &{ "SeSecurityPrivilege" }, 1)` + close.

## Idempotence

- Re-running install with the feature already selected → `GrantSeSecurityPrivilege` is a no-op (condition guards; LSA also no-ops on re-add).
- Uninstall with the feature already absent → `RevokeSeSecurityPrivilege` is a no-op.
- Modify install adding the feature → grants.
- Modify install removing the feature → revokes but leaves DrainCtl installed.
- Full product uninstall → revokes (then removes the marker file as part of normal component removal).

## Failure modes

| Failure | Behavior |
|---------|----------|
| `LsaOpenPolicy` returns access denied | CA fails; installer rolls back; admin sees error dialog with the HRESULT and a pointer to Event Viewer → Application → MsiInstaller. |
| Service account SID cannot be looked up (admin deleted it before uninstall) | Revoke CA logs a warning and returns success — nothing to revoke. |
| `LsaAddAccountRights` returns `STATUS_OBJECT_NAME_NOT_FOUND` (bogus privilege name) | CA fails; installer rolls back. This would be a build-time bug. |

## Installer UI

On the Features page, the feature appears under the DrainCtl product node with:
- **Title**: `Enable Security event log monitoring`
- **Description** (shown on selection): the full warning text from the XML above.
- **Default selection**: not selected (because `Level="1000"` > the default UI level of 100).

## Verification after install

Post-install, admins can verify:
- File present: `Test-Path 'C:\ProgramData\LISS Technologies\LISSTech DrainCtl\evtspike\security_enabled'`
- Privilege granted: `whoami /user /priv` run as the service account, or on a machine where the service is running: `sc.exe qprivs DrainCtl` (shows configured privileges).
- Service log at startup shows: `evtspike: security_enabled marker present; Security channel added to watched list`.

## Contract tests

These are MSI-level tests run via `msiexec /i` + `msiexec /x` in a sandboxed VM or via `Wix.Verifier`:

- `TestMSI_DefaultInstall_NoPrivilegeGranted`: install without selecting the feature; `LsaEnumerateAccountRights(LocalSystem SID)` does not include `SeSecurityPrivilege`; marker file absent.
- `TestMSI_OptInInstall_PrivilegeGranted`: install with `ADDLOCAL=SecurityEventLog`; `LsaEnumerateAccountRights` includes `SeSecurityPrivilege`; marker file present.
- `TestMSI_Uninstall_PrivilegeRevoked`: opt-in install, then uninstall; privilege gone, marker absent.
- `TestMSI_Modify_AddFeature_AfterBaseInstall`: base install without feature, then `msiexec /i` with `ADDLOCAL=SecurityEventLog`; privilege granted.
- `TestMSI_Modify_RemoveFeature`: opt-in install, then modify with `REMOVE=SecurityEventLog`; privilege revoked; DrainCtl still installed.
- `TestMSI_Reinstall_IsNoOp`: opt-in install, then reinstall with same options; no change to privileges or marker file.
- `TestMSI_UIText_ContainsWarningCopy`: scan compiled `.msi` UI tables for the exact warning string from FR-031.

These tests gate the `just release` pipeline via a post-build verification step.
