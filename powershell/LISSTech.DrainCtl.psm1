#Requires -Version 5.1

Set-StrictMode -Version Latest

# Locate drainctl.dll — may be alongside the psm1 (MSI install) or in bin/ subdir (dev/PSGallery).
$script:DllPath = Join-Path $PSScriptRoot 'drainctl.dll'
if (-not (Test-Path $script:DllPath)) {
    $script:DllPath = Join-Path (Join-Path $PSScriptRoot 'bin') 'drainctl.dll'
}
if (-not (Test-Path $script:DllPath)) {
    throw "DrainCtl: drainctl.dll not found. Module installation may be corrupt."
}

# Locate drainctl.exe — add its directory to PATH if not already there.
$script:BinDir = Split-Path $script:DllPath
$script:ExePath = Join-Path $script:BinDir 'drainctl.exe'

if ($env:PATH -notlike "*$($script:BinDir)*") {
    $env:PATH = "$($script:BinDir);$env:PATH"
}

$MyInvocation.MyCommand.ScriptBlock.Module.OnRemove = {
    $env:PATH = ($env:PATH -split ';' | Where-Object { $_ -ne $script:BinDir }) -join ';'
}

# ── Native interop ──────────────────────────────────────────────────────────

# P/Invoke declarations with a UTF-8 helper that works on both
# .NET Framework 4.x (Windows PowerShell 5.1) and .NET 6+ (PowerShell 7+).
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
using System.Text;

public static class DrainCtlNative {
    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_Version();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_ReadDrainMode();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_Check(
        [MarshalAs(UnmanagedType.LPStr)] string dbPath,
        int graceMinutes,
        int retentionDays);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_History(
        [MarshalAs(UnmanagedType.LPStr)] string dbPath,
        int limit,
        int changesOnly);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_AuditSetup();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern void DrainCtl_Free(IntPtr ptr);

    /// <summary>
    /// Read a null-terminated UTF-8 string from unmanaged memory.
    /// Works on .NET Framework 4.x (PS 5.1) and .NET 6+ (PS 7+).
    /// </summary>
    public static string PtrToStringUTF8(IntPtr ptr) {
        if (ptr == IntPtr.Zero) return null;
        int len = 0;
        while (Marshal.ReadByte(ptr, len) != 0) len++;
        byte[] buf = new byte[len];
        Marshal.Copy(ptr, buf, 0, len);
        return Encoding.UTF8.GetString(buf);
    }
}
"@

function Invoke-DrainCtlNative {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [IntPtr]$Ptr
    )

    try {
        $json = [DrainCtlNative]::PtrToStringUTF8($Ptr)
    } finally {
        [DrainCtlNative]::DrainCtl_Free($Ptr)
    }

    $obj = $json | ConvertFrom-Json

    if ($null -ne ($obj.PSObject.Properties['error'])) {
        $err = $obj.PSObject.Properties['error'].Value
        $exception = [System.InvalidOperationException]::new($err)
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'DrainCtlNativeError', [System.Management.Automation.ErrorCategory]::InvalidResult, $null)
        $PSCmdlet.ThrowTerminatingError($errorRecord)
    }

    return $obj
}

function Get-SafeProperty {
    # Reads a property from a PSObject without triggering strict-mode errors
    # when the property doesn't exist (JSON omitempty).
    param($Object, [string]$Name, $Default = $null)
    $prop = $Object.PSObject.Properties[$Name]
    if ($null -ne $prop) { return $prop.Value }
    return $Default
}

function ConvertTo-NativeDateTime {
    # Converts an ISO 8601 string to [datetime]. Returns $null for absent,
    # null, empty, or Go zero-time values.
    param($Value)
    if ($null -eq $Value -or $Value -eq '') { return $null }
    $s = [string]$Value
    # Go zero time
    if ($s -like '0001-01-01*') { return $null }
    return [datetimeoffset]::Parse($s).LocalDateTime
}

# ── Public cmdlets ──────────────────────────────────────────────────────────

function Get-RDSHDrainMode {
    <#
    .SYNOPSIS
    Returns detailed drain mode state with grace period evaluation and audit trail.

    .DESCRIPTION
    Reads the TSServerDrainMode registry value, detects state transitions,
    records an audit entry, and evaluates whether drain mode has exceeded
    the grace period.

    Returns a rich object with Status, Host, DrainMode, StateSince,
    StateDurationSeconds, transition info, and who changed it.

    Status values:
      healthy - connections allowed
      grace   - drain mode active but within grace period
      alert   - drain mode active beyond grace period
      error   - could not read registry

    Sets $LASTEXITCODE (0 = healthy/grace, 1 = alert, 2 = error).

    .PARAMETER DBPath
    Path to the JSONL audit trail file.
    Default: %ProgramData%\drainctl\audit.jsonl

    .PARAMETER GraceMinutes
    Minutes drain mode must persist before alerting. Default: 60.

    .PARAMETER RetentionDays
    Days to retain audit records. Default: 90.

    .EXAMPLE
    PS> Get-RDSHDrainMode

    Host                 : RDSH01
    DrainMode            : ALLOW_ALL_CONNECTIONS
    Status               : healthy
    StateDurationSeconds : 3600

    .EXAMPLE
    PS> Get-RDSHDrainMode -GraceMinutes 120 | Select-Object Host, Status, DrainMode

    .EXAMPLE
    PS> Get-RDSHDrainMode | Where-Object Status -ne 'healthy'
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject])]
    param(
        [Parameter()]
        [string]$DBPath = '',

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$GraceMinutes = 60,

        [Parameter()]
        [ValidateRange(1, 365)]
        [int]$RetentionDays = 90
    )

    $ptr = [DrainCtlNative]::DrainCtl_Check($DBPath, $GraceMinutes, $RetentionDays)
    $raw = Invoke-DrainCtlNative -Ptr $ptr

    $global:LASTEXITCODE = $raw.exit_code

    [PSCustomObject]@{
        PSTypeName           = 'DrainCtl.CheckResult'
        Timestamp            = ConvertTo-NativeDateTime $raw.timestamp
        Host                 = $raw.host
        DrainMode            = $raw.drain_mode
        DrainModeValue       = $raw.drain_mode_value
        StateSince           = ConvertTo-NativeDateTime $raw.state_since
        StateDurationSeconds = if ($null -ne $raw.state_duration_seconds) { [int]$raw.state_duration_seconds } else { $null }
        GracePeriodSeconds   = $raw.grace_period_seconds
        Status               = $raw.status
        ConnectionsAllowed   = $raw.connections_allowed
        Transition           = $raw.transition
        TransitionFrom       = Get-SafeProperty $raw 'transition_from'
        ChangedBy            = Get-SafeProperty $raw 'changed_by'
        Message              = Get-SafeProperty $raw 'message'
        ExitCode             = $raw.exit_code
    }
}

function Test-RDSHDrainMode {
    <#
    .SYNOPSIS
    Tests whether RDSH connections are allowed. Returns $true or $false.

    .DESCRIPTION
    Returns $true if the server is accepting connections (drain mode is off
    or within the grace period). Returns $false if drain mode is active
    beyond the grace period or the registry cannot be read.

    Also records an audit entry and sets $LASTEXITCODE
    (0 = pass, 1 = alert, 2 = error).

    .PARAMETER DBPath
    Path to the JSONL audit trail file.
    Default: %ProgramData%\drainctl\audit.jsonl

    .PARAMETER GraceMinutes
    Minutes drain mode must persist before alerting. Default: 60.

    .PARAMETER RetentionDays
    Days to retain audit records. Default: 90.

    .EXAMPLE
    PS> if (Test-RDSHDrainMode) { 'OK' } else { 'ALERT' }

    .EXAMPLE
    PS> Test-RDSHDrainMode -GraceMinutes 120
    True
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter()]
        [string]$DBPath = '',

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$GraceMinutes = 60,

        [Parameter()]
        [ValidateRange(1, 365)]
        [int]$RetentionDays = 90
    )

    $ptr = [DrainCtlNative]::DrainCtl_Check($DBPath, $GraceMinutes, $RetentionDays)
    $result = Invoke-DrainCtlNative -Ptr $ptr

    $global:LASTEXITCODE = $result.exit_code

    $result.exit_code -eq 0
}

function Get-RDSHDrainHistory {
    <#
    .SYNOPSIS
    Retrieves the drain mode audit trail.

    .DESCRIPTION
    Returns audit records from the JSONL trail, newest first.
    Each record includes the timestamp, host, drain mode, whether
    a state transition occurred, and who made the change.

    .PARAMETER DBPath
    Path to the JSONL audit trail file.
    Default: %ProgramData%\drainctl\audit.jsonl

    .PARAMETER Limit
    Maximum number of records to return. Default: 50.

    .PARAMETER ChangesOnly
    If set, returns only records where a state transition occurred.

    .EXAMPLE
    PS> Get-RDSHDrainHistory | Format-Table

    .EXAMPLE
    PS> Get-RDSHDrainHistory -ChangesOnly -Limit 10

    .EXAMPLE
    PS> Get-RDSHDrainHistory | Where-Object Changed -eq $true
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject[]])]
    param(
        [Parameter()]
        [string]$DBPath = '',

        [Parameter()]
        [ValidateRange(1, [int]::MaxValue)]
        [int]$Limit = 50,

        [Parameter()]
        [switch]$ChangesOnly
    )

    $co = if ($ChangesOnly) { 1 } else { 0 }
    $ptr = [DrainCtlNative]::DrainCtl_History($DBPath, $Limit, $co)
    $records = Invoke-DrainCtlNative -Ptr $ptr

    if ($null -eq $records -or $records.Count -eq 0) {
        return
    }

    foreach ($r in $records) {
        [PSCustomObject]@{
            PSTypeName           = 'DrainCtl.AuditRecord'
            Timestamp            = ConvertTo-NativeDateTime $r.timestamp
            Host                 = $r.host
            DrainMode            = $r.drain_mode
            DrainModeValue       = $r.drain_mode_value
            StateDurationSeconds = Get-SafeProperty $r 'state_duration_seconds'
            Changed              = $r.changed
            ChangedBy            = Get-SafeProperty $r 'changed_by'
            ExitCode             = $r.exit_code
        }
    }
}

function Install-RDSHDrainAudit {
    <#
    .SYNOPSIS
    Configures registry auditing for drain mode change attribution.

    .DESCRIPTION
    One-time setup that enables Object Access auditing and sets a SACL on
    the Terminal Server registry key so that Windows records Event ID 4657
    whenever TSServerDrainMode is modified. This allows Get-RDSHDrainMode
    and drainctl to attribute changes to specific users.

    Must be run elevated (as Administrator or SYSTEM).

    On domain-joined machines, Group Policy may override the local auditpol
    settings on the next GP refresh (~90 minutes). The command emits warnings
    with the exact GPO path to configure for persistence.

    .EXAMPLE
    PS> Install-RDSHDrainAudit
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High')]
    [OutputType([void])]
    param()

    if (-not $PSCmdlet.ShouldProcess('Registry auditing for TSServerDrainMode', 'Configure')) {
        return
    }

    $ptr = [DrainCtlNative]::DrainCtl_AuditSetup()
    $result = Invoke-DrainCtlNative -Ptr $ptr

    Write-Verbose 'Registry auditing configured. Event ID 4657 will now record TSServerDrainMode changes.'
}

Export-ModuleMember -Function @(
    'Get-RDSHDrainMode'
    'Test-RDSHDrainMode'
    'Get-RDSHDrainHistory'
    'Install-RDSHDrainAudit'
)
