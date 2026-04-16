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
    public static extern IntPtr DrainCtl_GetSettings();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_SetSettings(
        [MarshalAs(UnmanagedType.LPStr)] string jsonStr);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_TestNotify();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_EnableDashboard(int port, [MarshalAs(UnmanagedType.LPStr)] string group);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_DisableDashboard();

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_InstallCertificate(
        [MarshalAs(UnmanagedType.LPStr)] string certPath,
        [MarshalAs(UnmanagedType.LPStr)] string keyPath);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_NotifySetTarget(
        [MarshalAs(UnmanagedType.LPStr)] string jsonStr);

    [DllImport("$($script:DllPath.Replace('\','\\'))", CallingConvention = CallingConvention.Cdecl)]
    public static extern IntPtr DrainCtl_NotifyRemoveTarget(
        [MarshalAs(UnmanagedType.LPStr)] string typ,
        int index);

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

function ConvertFrom-SecureStringToPlainText {
    # Decrypts a SecureString into a plaintext string. The plaintext lives in
    # managed memory only as long as the caller holds the result; we marshal
    # it to drainctl.dll immediately so DPAPI encryption happens before any
    # garbage-collected copy can persist.
    param([System.Security.SecureString]$Secure)
    if ($null -eq $Secure) { return $null }
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($Secure)
    try {
        return [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    } finally {
        [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
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
        Performance          = $(
            $perf = Get-SafeProperty $raw 'performance'
            if ($null -ne $perf) {
                [PSCustomObject]@{
                    CPUPct        = Get-SafeProperty $perf 'cpu_pct' 0
                    MemAvailMB    = Get-SafeProperty $perf 'mem_avail_mb' 0
                    MemTotalMB    = Get-SafeProperty $perf 'mem_total_mb' 0
                    PagesSec      = Get-SafeProperty $perf 'pages_sec' 0
                    DiskQueue     = Get-SafeProperty $perf 'disk_queue' 0
                    TCPRetrans    = Get-SafeProperty $perf 'tcp_retrans_sec' 0
                    InputDelayP50 = Get-SafeProperty $perf 'input_delay_p50_ms' 0
                    InputDelayP95 = Get-SafeProperty $perf 'input_delay_p95_ms' 0
                    InputDelayMax = Get-SafeProperty $perf 'input_delay_max_ms' 0
                    SessionCPUP95 = Get-SafeProperty $perf 'session_cpu_p95_pct' 0
                    SessionMemP95 = Get-SafeProperty $perf 'session_mem_p95_bytes' 0
                    RFXAvailable  = Get-SafeProperty $perf 'rfx_available' $false
                    RFXFPSOut     = Get-SafeProperty $perf 'rfx_fps_out' 0
                    RFXSkipServer = Get-SafeProperty $perf 'rfx_skip_server_sec' 0
                    RFXSkipNet    = Get-SafeProperty $perf 'rfx_skip_net_sec' 0
                    RFXEncodeMS   = Get-SafeProperty $perf 'rfx_encode_ms' 0
                    RFXQuality    = Get-SafeProperty $perf 'rfx_quality_pct' 0
                    RFXRTT        = Get-SafeProperty $perf 'rfx_rtt_ms' 0
                    RFXLoss       = Get-SafeProperty $perf 'rfx_loss_pct' 0
                }
            } else { $null }
        )
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

    if ($null -eq $records -or @($records).Count -eq 0) {
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
            ActiveSessions       = Get-SafeProperty $r 'active_sessions' 0
            DisconnectedSessions = Get-SafeProperty $r 'disconnected_sessions' 0
            TotalSessions        = Get-SafeProperty $r 'total_sessions' 0
            MaxSessions          = Get-SafeProperty $r 'max_sessions' 0
            CPUPct               = Get-SafeProperty $r 'cpu_pct' $null
            InputDelayMax        = Get-SafeProperty $r 'input_delay_max_ms' $null
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
    $null = Invoke-DrainCtlNative -Ptr $ptr

    Write-Verbose 'Registry auditing configured. Event ID 4657 will now record TSServerDrainMode changes.'
}

function Get-RDSHDrainNotification {
    <#
    .SYNOPSIS
    Shows the current notification configuration for DrainCtl.

    .DESCRIPTION
    Reads notification settings from config.json and returns them as a
    structured object. Shows webhook URL, ntfy URL, transition/grace
    notification toggles, and repeat interval.

    .EXAMPLE
    PS> Get-RDSHDrainNotification

    WebhookURL      : https://hooks.example.com/drainctl
    NtfyURL         :
    OnTransition    : True
    OnGraceExceeded : True
    RepeatMinutes   : 0
    Enabled         : True
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject])]
    param()

    $ptr = [DrainCtlNative]::DrainCtl_GetSettings()
    $raw = Invoke-DrainCtlNative -Ptr $ptr

    [PSCustomObject]@{
        PSTypeName      = 'DrainCtl.Settings'
        WebhookURL      = Get-SafeProperty $raw 'webhook_url' ''
        NtfyURL         = Get-SafeProperty $raw 'ntfy_url' ''
        OnTransition    = [bool](Get-SafeProperty $raw 'on_transition' $true)
        OnGraceExceeded = [bool](Get-SafeProperty $raw 'on_grace_exceeded' $true)
        RepeatMinutes   = [int](Get-SafeProperty $raw 'repeat_minutes' 0)
        Enabled         = [bool](Get-SafeProperty $raw 'enabled' $false)
    }
}

function Set-RDSHDrainNotification {
    <#
    .SYNOPSIS
    Updates notification settings for DrainCtl (legacy — use Add/Remove-RDSHDrainNotificationTarget instead).

    .DESCRIPTION
    Writes notification configuration to config.json using the legacy flat
    format (single webhook + single ntfy). Only specified parameters are
    changed; unspecified parameters retain their current values.

    DEPRECATED: This cmdlet manages at most one webhook and one ntfy target.
    For multi-target management use Get-RDSHDrainNotificationTarget,
    Add-RDSHDrainNotificationTarget, and Remove-RDSHDrainNotificationTarget.

    .PARAMETER WebhookURL
    Webhook URL for HTTP POST JSON notifications. Set to empty string to disable.

    .PARAMETER NtfyURL
    ntfy.sh topic URL (e.g. "https://ntfy.sh/drainctl-alerts"). Set to empty string to disable.

    .PARAMETER OnTransition
    Enable or disable notifications on state transitions.

    .PARAMETER OnGraceExceeded
    Enable or disable notifications when drain exceeds grace period.

    .PARAMETER RepeatMinutes
    Minutes between repeated alert notifications (0 = notify once only).

    .EXAMPLE
    PS> Set-RDSHDrainNotification -WebhookURL 'https://hooks.example.com/drainctl'

    .EXAMPLE
    PS> Set-RDSHDrainNotification -NtfyURL 'https://ntfy.sh/my-alerts' -RepeatMinutes 30

    .EXAMPLE
    PS> Set-RDSHDrainNotification -WebhookURL '' -NtfyURL ''
    #>
    [CmdletBinding(SupportsShouldProcess)]
    [OutputType([void])]
    param(
        [Parameter()]
        [AllowEmptyString()]
        [string]$WebhookURL,

        [Parameter()]
        [AllowEmptyString()]
        [string]$NtfyURL,

        [Parameter()]
        [bool]$OnTransition,

        [Parameter()]
        [bool]$OnGraceExceeded,

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$RepeatMinutes
    )

    Write-Warning 'Set-RDSHDrainNotification is deprecated. Use Add-RDSHDrainNotificationTarget and Remove-RDSHDrainNotificationTarget for multi-target management.'

    if (-not $PSCmdlet.ShouldProcess('DrainCtl notification configuration', 'Update')) {
        return
    }

    $payload = @{}
    if ($PSBoundParameters.ContainsKey('WebhookURL'))      { $payload['webhook_url']       = $WebhookURL }
    if ($PSBoundParameters.ContainsKey('NtfyURL'))          { $payload['ntfy_url']          = $NtfyURL }
    if ($PSBoundParameters.ContainsKey('OnTransition'))     { $payload['on_transition']     = $OnTransition }
    if ($PSBoundParameters.ContainsKey('OnGraceExceeded'))  { $payload['on_grace_exceeded'] = $OnGraceExceeded }
    if ($PSBoundParameters.ContainsKey('RepeatMinutes'))    { $payload['repeat_minutes']    = $RepeatMinutes }

    $jsonStr = $payload | ConvertTo-Json -Compress
    $ptr = [DrainCtlNative]::DrainCtl_SetSettings($jsonStr)
    $null = Invoke-DrainCtlNative -Ptr $ptr

    Write-Verbose 'Notification configuration updated.'
}

function Get-RDSHDrainNotificationTarget {
    <#
    .SYNOPSIS
    Lists all configured notification targets.

    .DESCRIPTION
    Returns all notification targets from config.json as structured objects.
    Each target has a Type (webhook or ntfy), URL, Triggers array, and
    RepeatMinutes setting.

    .EXAMPLE
    PS> Get-RDSHDrainNotificationTarget

    Type    URL                                   Triggers                        RepeatMinutes
    ----    ---                                   --------                        -------------
    webhook https://hooks.example.com/drain       {drain_on, drain_off, alert}               15
    ntfy    https://ntfy.sh/drainctl-alerts       {alert}                                     0

    .EXAMPLE
    PS> Get-RDSHDrainNotificationTarget | Where-Object Type -eq 'webhook'
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject[]])]
    param()

    $ptr = [DrainCtlNative]::DrainCtl_GetSettings()
    $raw = Invoke-DrainCtlNative -Ptr $ptr

    $targets = Get-SafeProperty $raw 'notifications' @()
    if ($null -eq $targets -or @($targets).Count -eq 0) {
        return
    }

    foreach ($t in $targets) {
        [PSCustomObject]@{
            PSTypeName    = 'DrainCtl.NotificationTarget'
            Type          = $t.type
            URL           = $t.url
            Triggers      = @($t.triggers)
            RepeatMinutes = [int](Get-SafeProperty $t 'repeat_minutes' 0)
        }
    }
}

function Add-RDSHDrainNotificationTarget {
    <#
    .SYNOPSIS
    Adds a notification target to DrainCtl.

    .DESCRIPTION
    Appends a new notification target (webhook, ntfy, or email) to the
    notifications array in config.json. The mutation is atomic — drainctl.dll
    walks the same code path as the CLI, so the dashboard cannot race with
    this call.

    Pass -Secret as a [SecureString] to set the webhook HMAC key or SMTP
    password. The plaintext is DPAPI-encrypted by drainctl before it lands
    on disk.

    .PARAMETER Type
    Target type: 'webhook', 'ntfy', or 'email'.

    .PARAMETER URL
    Target URL. For email use smtp:// or smtps:// (e.g. smtp://mail.example.com:587).

    .PARAMETER Triggers
    Array of trigger names. Default: drain_on, drain_off, alert, healthy.

    .PARAMETER RepeatMinutes
    Minutes between repeated alert notifications. 0 = notify once only. Default: 0.

    .PARAMETER Secret
    SecureString containing the HMAC signing secret (webhook) or SMTP password
    (email). Stored DPAPI-encrypted. Use:
      $sec = Read-Host -AsSecureString
      $sec = $env:DRAINCTL_SMTP_PASSWORD | ConvertTo-SecureString -AsPlainText -Force

    .PARAMETER From
    (email only) Sender address.

    .PARAMETER To
    (email only) One or more recipient addresses.

    .EXAMPLE
    PS> Add-RDSHDrainNotificationTarget -Type webhook -URL 'https://hooks.example.com/drain' -Secret (Read-Host -AsSecureString)

    .EXAMPLE
    PS> $pw = $env:SMTP_PASSWORD | ConvertTo-SecureString -AsPlainText -Force
    PS> Add-RDSHDrainNotificationTarget -Type email -URL 'smtp://mail.example.com:587' `
            -From 'alerts@example.com' -To 'ops@example.com','oncall@example.com' -Secret $pw
    #>
    [CmdletBinding(SupportsShouldProcess)]
    [OutputType([void])]
    param(
        [Parameter(Mandatory)]
        [ValidateSet('webhook', 'ntfy', 'email')]
        [string]$Type,

        [Parameter(Mandatory)]
        [ValidateNotNullOrEmpty()]
        [string]$URL,

        [Parameter()]
        [ValidateSet('drain_on', 'drain_off', 'grace_entered', 'alert', 'healthy',
            'session_warning', 'cpu_warning', 'cpu_critical',
            'input_delay_warning', 'input_delay_critical',
            'memory_warning', 'memory_critical')]
        [string[]]$Triggers = @('drain_on', 'drain_off', 'alert', 'healthy'),

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$RepeatMinutes = 0,

        [Parameter()]
        [System.Security.SecureString]$Secret,

        [Parameter()]
        [string]$From,

        [Parameter()]
        [string[]]$To
    )

    if (-not $PSCmdlet.ShouldProcess("$Type target $URL", 'Add notification target')) {
        return
    }

    # TargetIndex past-end (huge value) tells the shared API to append.
    $payload = [ordered]@{
        type         = $Type
        url          = $URL
        target_index = [int]::MaxValue
        triggers     = @($Triggers)
        repeat_minutes = $RepeatMinutes
    }
    if ($PSBoundParameters.ContainsKey('Secret')) {
        $payload['secret'] = ConvertFrom-SecureStringToPlainText -Secure $Secret
    }
    if ($PSBoundParameters.ContainsKey('From')) {
        $payload['from'] = $From
    }
    if ($PSBoundParameters.ContainsKey('To')) {
        $payload['to'] = @($To)
    }

    $jsonStr = $payload | ConvertTo-Json -Depth 4 -Compress
    $ptr = [DrainCtlNative]::DrainCtl_NotifySetTarget($jsonStr)
    $null = Invoke-DrainCtlNative -Ptr $ptr

    Write-Verbose "Added $Type notification target: $URL"
}

function Set-RDSHDrainNotificationTarget {
    <#
    .SYNOPSIS
    Updates an existing notification target in place.

    .DESCRIPTION
    Modifies the Nth target of the given type (use Get-RDSHDrainNotificationTarget
    or 'drainctl notify status' to see indices). Any parameter not supplied is
    preserved on the target.

    Pass -Secret as a [SecureString] to set the webhook HMAC key or SMTP
    password. The plaintext is DPAPI-encrypted by drainctl before it lands
    on disk.

    If TargetIndex is past the end of existing targets of Type, a new target
    is appended (matching the shared API semantics).

    .PARAMETER Type
    Target type: 'webhook', 'ntfy', or 'email'.

    .PARAMETER TargetIndex
    0-based index among targets of Type. Default: 0 (first target of that type).

    .PARAMETER URL
    Replacement URL. Required by the shared API.

    .PARAMETER Triggers
    Replacement trigger list (omit to preserve existing).

    .PARAMETER RepeatMinutes
    Replacement repeat interval (omit to preserve existing).

    .PARAMETER Secret
    SecureString containing new HMAC secret or SMTP password. Omit to preserve
    the existing secret.

    .PARAMETER From
    (email) Sender address (omit to preserve).

    .PARAMETER To
    (email) Recipient addresses (omit to preserve).

    .EXAMPLE
    PS> $pw = $env:SMTP_PASSWORD | ConvertTo-SecureString -AsPlainText -Force
    PS> Set-RDSHDrainNotificationTarget -Type email -TargetIndex 0 `
            -URL 'smtp://mail.example.com:587' -Secret $pw

    .EXAMPLE
    PS> Set-RDSHDrainNotificationTarget -Type webhook -TargetIndex 1 `
            -URL 'https://hooks.example.com/drain' -RepeatMinutes 30
    #>
    [CmdletBinding(SupportsShouldProcess)]
    [OutputType([void])]
    param(
        [Parameter(Mandatory)]
        [ValidateSet('webhook', 'ntfy', 'email')]
        [string]$Type,

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$TargetIndex = 0,

        [Parameter(Mandatory)]
        [ValidateNotNullOrEmpty()]
        [string]$URL,

        [Parameter()]
        [ValidateSet('drain_on', 'drain_off', 'grace_entered', 'alert', 'healthy',
            'session_warning', 'cpu_warning', 'cpu_critical',
            'input_delay_warning', 'input_delay_critical',
            'memory_warning', 'memory_critical')]
        [string[]]$Triggers,

        [Parameter()]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$RepeatMinutes,

        [Parameter()]
        [System.Security.SecureString]$Secret,

        [Parameter()]
        [string]$From,

        [Parameter()]
        [string[]]$To
    )

    if (-not $PSCmdlet.ShouldProcess("$Type target #$TargetIndex", 'Update notification target')) {
        return
    }

    $payload = [ordered]@{
        type         = $Type
        url          = $URL
        target_index = $TargetIndex
    }
    if ($PSBoundParameters.ContainsKey('Triggers'))      { $payload['triggers']       = @($Triggers) }
    if ($PSBoundParameters.ContainsKey('RepeatMinutes')) { $payload['repeat_minutes'] = $RepeatMinutes }
    if ($PSBoundParameters.ContainsKey('Secret'))        { $payload['secret']         = ConvertFrom-SecureStringToPlainText -Secure $Secret }
    if ($PSBoundParameters.ContainsKey('From'))          { $payload['from']           = $From }
    if ($PSBoundParameters.ContainsKey('To'))            { $payload['to']             = @($To) }

    $jsonStr = $payload | ConvertTo-Json -Depth 4 -Compress
    $ptr = [DrainCtlNative]::DrainCtl_NotifySetTarget($jsonStr)
    $null = Invoke-DrainCtlNative -Ptr $ptr

    Write-Verbose "Updated $Type[$TargetIndex]: $URL"
}

function Remove-RDSHDrainNotificationTarget {
    <#
    .SYNOPSIS
    Removes a notification target from DrainCtl by URL or by (Type, TargetIndex).

    .DESCRIPTION
    Two parameter sets:
      - ByURL (default, legacy): removes the first target whose URL matches.
      - ByIndex: removes the Nth target of the given type — preferred for
        deterministic scripting against multi-target configurations.

    Both paths are atomic via drainctl.dll.

    .PARAMETER URL
    URL of the target to remove (case-insensitive match). ByURL parameter set.

    .PARAMETER Type
    Target type ('webhook', 'ntfy', or 'email'). ByIndex parameter set.

    .PARAMETER TargetIndex
    0-based index among targets of Type. ByIndex parameter set.

    .EXAMPLE
    PS> Remove-RDSHDrainNotificationTarget -URL 'https://hooks.example.com/drain'

    .EXAMPLE
    PS> Remove-RDSHDrainNotificationTarget -Type webhook -TargetIndex 1
    #>
    [CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'ByURL')]
    [OutputType([void])]
    param(
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'ByURL')]
        [ValidateNotNullOrEmpty()]
        [string]$URL,

        [Parameter(Mandatory, ParameterSetName = 'ByIndex')]
        [ValidateSet('webhook', 'ntfy', 'email')]
        [string]$Type,

        [Parameter(Mandatory, ParameterSetName = 'ByIndex')]
        [ValidateRange(0, [int]::MaxValue)]
        [int]$TargetIndex
    )

    process {
        if ($PSCmdlet.ParameterSetName -eq 'ByIndex') {
            if (-not $PSCmdlet.ShouldProcess("$Type[$TargetIndex]", 'Remove notification target')) {
                return
            }
            $ptr = [DrainCtlNative]::DrainCtl_NotifyRemoveTarget($Type, $TargetIndex)
            $null = Invoke-DrainCtlNative -Ptr $ptr
            Write-Verbose "Removed $Type[$TargetIndex]"
            return
        }

        if (-not $PSCmdlet.ShouldProcess("target $URL", 'Remove notification target')) {
            return
        }

        # Legacy URL-match path: read, filter, write whole notifications array.
        $ptr = [DrainCtlNative]::DrainCtl_GetSettings()
        $raw = Invoke-DrainCtlNative -Ptr $ptr
        $targets = @(Get-SafeProperty $raw 'notifications' @())

        $filtered = @($targets | Where-Object { $_.url -ine $URL })

        if ($filtered.Count -eq $targets.Count) {
            Write-Warning "No notification target found with URL: $URL"
            return
        }

        $payload = @{ notifications = @($filtered) }
        $jsonStr = $payload | ConvertTo-Json -Depth 4 -Compress
        $ptr = [DrainCtlNative]::DrainCtl_SetSettings($jsonStr)
        $null = Invoke-DrainCtlNative -Ptr $ptr

        Write-Verbose "Removed notification target: $URL"
    }
}

function Test-RDSHDrainNotification {
    <#
    .SYNOPSIS
    Sends a test notification to all configured backends.

    .DESCRIPTION
    Reads the current notification configuration and sends a test message
    to each configured backend (webhook and/or ntfy). Returns an error
    if no backends are configured or if sending fails.

    .EXAMPLE
    PS> Test-RDSHDrainNotification
    #>
    [CmdletBinding()]
    [OutputType([void])]
    param()

    $ptr = [DrainCtlNative]::DrainCtl_TestNotify()
    $null = Invoke-DrainCtlNative -Ptr $ptr

    Write-Host 'Test notification sent successfully.'
}

function Enable-RDSHDrainDashboard {
    <#
    .SYNOPSIS
    Enable the DrainCtl multi-server dashboard on this server.

    .PARAMETER Port
    Port the dashboard listens on. Default: 49470.

    .PARAMETER Group
    AD group authorized to view the dashboard. Default: Domain Admins.

    .EXAMPLE
    Enable-RDSHDrainDashboard -Port 49470 -Group "RDS Admins"
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    param(
        [ValidateRange(1, 65535)][int]$Port = 49470,
        [ValidateNotNullOrEmpty()][string]$Group = 'Domain Admins'
    )
    if (-not $PSCmdlet.ShouldProcess('DrainCtl Dashboard', 'Enable')) { return }
    $ptr = [DrainCtlNative]::DrainCtl_EnableDashboard($Port, $Group)
    $null = Invoke-DrainCtlNative -Ptr $ptr
    Write-Host "Dashboard enabled on port $Port for group '$Group'."
    Write-Host 'Restart the DrainCtl service to activate: Restart-Service DrainCtl'
}

function Disable-RDSHDrainDashboard {
    <#
    .SYNOPSIS
    Disable the DrainCtl dashboard on this server.

    .EXAMPLE
    Disable-RDSHDrainDashboard
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    param()
    if (-not $PSCmdlet.ShouldProcess('DrainCtl Dashboard', 'Disable')) { return }
    $ptr = [DrainCtlNative]::DrainCtl_DisableDashboard()
    $null = Invoke-DrainCtlNative -Ptr $ptr
    Write-Host 'Dashboard disabled. Restart the DrainCtl service to apply: Restart-Service DrainCtl'
}

function Install-RDSHDrainCertificate {
    <#
    .SYNOPSIS
    Install a custom TLS certificate for the DrainCtl dashboard.

    .DESCRIPTION
    Copies a PEM certificate and private key into the DrainCtl data directory
    and updates config.json so the dashboard uses them instead of the
    auto-generated self-signed certificate.

    Restart the DrainCtl service after installing a new certificate.

    .PARAMETER CertPath
    Path to the PEM certificate file.

    .PARAMETER KeyPath
    Path to the PEM private key file.

    .EXAMPLE
    Install-RDSHDrainCertificate -CertPath C:\certs\dashboard.pem -KeyPath C:\certs\dashboard-key.pem
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    param(
        [Parameter(Mandatory)][string]$CertPath,
        [Parameter(Mandatory)][string]$KeyPath
    )

    if (-not (Test-Path $CertPath)) { throw "Certificate file not found: $CertPath" }
    if (-not (Test-Path $KeyPath))  { throw "Key file not found: $KeyPath" }
    if (-not $PSCmdlet.ShouldProcess("$CertPath + $KeyPath", 'Install as dashboard TLS certificate')) { return }

    $ptr = [DrainCtlNative]::DrainCtl_InstallCertificate($CertPath, $KeyPath)
    $null = Invoke-DrainCtlNative -Ptr $ptr
    Write-Host "Certificate installed. Restart the DrainCtl service to use the new certificate: Restart-Service DrainCtl"
}

Export-ModuleMember -Function @(
    'Get-RDSHDrainMode'
    'Test-RDSHDrainMode'
    'Get-RDSHDrainHistory'
    'Install-RDSHDrainAudit'
    'Get-RDSHDrainNotification'
    'Set-RDSHDrainNotification'
    'Get-RDSHDrainNotificationTarget'
    'Add-RDSHDrainNotificationTarget'
    'Set-RDSHDrainNotificationTarget'
    'Remove-RDSHDrainNotificationTarget'
    'Test-RDSHDrainNotification'
    'Enable-RDSHDrainDashboard'
    'Disable-RDSHDrainDashboard'
    'Install-RDSHDrainCertificate'
)
