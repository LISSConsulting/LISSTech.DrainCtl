# Contract: ETW Provider

**Scope**: Windows Event Tracing manifest-based provider for DrainCtl service.

## Provider Registration

| Property | Value |
|----------|-------|
| Provider Name | `LISS Technologies-DrainCtl` |
| Provider GUID | *(generated during implementation)* |
| Resource DLL | `[InstallDir]\bin\drainctl-msg.dll` |
| Manifest | `[InstallDir]\bin\drainctl.man` |

## Channels

### Operational

```
Name:      LISS Technologies-DrainCtl/Operational
Type:      Operational
Enabled:   true (default)
Isolation: Application
```

Receives all INFO, WARN, and ERROR events. Visible in Event Viewer under **Applications and Services Logs > LISS Technologies-DrainCtl > Operational**.

### Debug

```
Name:      LISS Technologies-DrainCtl/Debug
Type:      Debug
Enabled:   false (default)
Isolation: Application
```

Receives DEBUG events. Must be enabled by administrator:
```powershell
wevtutil sl "LISS Technologies-DrainCtl/Debug" /e:true
```

Visible in Event Viewer under **Applications and Services Logs > LISS Technologies-DrainCtl > Debug** (show analytic and debug logs).

## Event Definitions

### Operational Channel Events

| ID | Symbol | Level | Description |
|----|--------|-------|-------------|
| 1000 | ServiceStarted | Informational | Service started successfully |
| 1001 | ServiceStopped | Informational | Service stopped |
| 1002 | CheckHealthy | Informational | Health check passed |
| 1003 | ConfigReloaded | Informational | Configuration reloaded from file |
| 1004 | TransitionDetected | Informational | Drain mode state transition |
| 1099 | GenericInfo | Informational | Generic informational message |
| 2000 | CheckGrace | Warning | Grace period active |
| 2099 | GenericWarning | Warning | Generic warning |
| 3000 | CheckAlert | Error | Grace period exceeded / alert condition |
| 3001 | RegistryReadFailed | Error | Failed to read registry value |
| 3002 | ServiceError | Error | Service runtime error |
| 3099 | GenericError | Error | Generic error |

### Debug Channel Events

| ID | Symbol | Level | Description |
|----|--------|-------|-------------|
| 4000 | GenericDebug | Verbose | Generic debug message |

## Event Template

All events use a single string data field (`%1`) for the formatted message, consistent with the current message compiler approach. Structured fields are embedded in the message string (e.g., `host=RDSH01 state=healthy`).

## Querying

```powershell
# View provider metadata
wevtutil gp "LISS Technologies-DrainCtl"

# Query Operational events
wevtutil qe "LISS Technologies-DrainCtl/Operational" /c:10 /f:text

# Enable Debug channel
wevtutil sl "LISS Technologies-DrainCtl/Debug" /e:true

# Query Debug events
wevtutil qe "LISS Technologies-DrainCtl/Debug" /c:10 /f:text
```

## Installer Actions

| Phase | Action |
|-------|--------|
| Install | `wevtutil im "[InstallDir]\bin\drainctl.man"` |
| Uninstall | `wevtutil um "[InstallDir]\bin\drainctl.man"` |
| Upgrade | Uninstall old → Install new |

Replaces the current registry-based event log source registration (`HKLM\SYSTEM\CurrentControlSet\Services\EventLog\DrainCtl\DrainCtl`).
