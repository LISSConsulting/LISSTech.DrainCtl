# Hourly diagnostics collector for LISSTech DrainCtl. Invoked by Task Scheduler
# (\LISS Technologies\DrainCtl-Diags). Always captures service and crash diagnostics;
# pprof and selfmetrics snapshots remain opt-in through DRAINCTL_PPROF_PORT.
# Output: %ProgramData%\LISS Technologies\LISSTech DrainCtl\diags
#
# Crash dumps are sensitive local diagnostic data. They are inventoried as metadata
# only unless DRAINCTL_INCLUDE_CRASH_DUMPS=1, which hashes and gzip-copies only the
# newest dump inside the protected dumps directory for authorized local review.

$ErrorActionPreference = 'Stop'

$dataDir  = Join-Path $env:ProgramData 'LISS Technologies\LISSTech DrainCtl'
$diagsDir = Join-Path $dataDir 'diags'
$ownLog   = Join-Path $diagsDir 'collector.log'

# The MSI pre-creates this path with a protected SYSTEM/Administrators-only DACL.
# Do not recreate a missing directory and inherit the broader product-data ACL.
if (-not (Test-Path -LiteralPath $diagsDir -PathType Container)) {
    throw "Protected diagnostics directory is missing: $diagsDir. Repair the approved MSI."
}

function Log([string]$msg) {
    $line = '{0} {1}' -f ([DateTime]::UtcNow.ToString('o')), $msg
    Add-Content -Path $ownLog -Value $line -ErrorAction SilentlyContinue
}

function GzipFile([string]$src, [string]$dst) {
    $in  = [System.IO.File]::OpenRead($src)
    try {
        $out = [System.IO.File]::Create($dst)
        try {
            $gz = New-Object System.IO.Compression.GZipStream($out, [System.IO.Compression.CompressionMode]::Compress)
            try { $in.CopyTo($gz) } finally { $gz.Dispose() }
        } finally { $out.Dispose() }
    } finally { $in.Dispose() }
}

function Write-TextFile([string]$path, [object[]]$lines) {
    $lines | Set-Content -Path $path -Encoding UTF8
}

function Get-ServiceCommandOutput([string[]]$arguments) {
    try {
        return (& sc.exe @arguments 2>&1 | Out-String).TrimEnd()
    } catch {
        return ('command failed: {0}' -f $_.Exception.Message)
    }
}

function Get-Sha256Hex([string]$path) {
    $stream = [System.IO.File]::OpenRead($path)
    try {
        $sha256 = [System.Security.Cryptography.SHA256]::Create()
        try {
            return [System.BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '')
        } finally { $sha256.Dispose() }
    } finally { $stream.Dispose() }
}

function Collect-ServiceDiagnostics([string]$stamp) {
    $path = Join-Path $diagsDir ('service-{0}.txt' -f $stamp)
    $lines = @(
        '# DrainCtl service state, configuration, and failure actions',
        '# Captured UTC: ' + ([DateTime]::UtcNow.ToString('o'))
    )
    try {
        $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='DrainCtl'" -ErrorAction Stop
        $lines += $service | Format-List Name, DisplayName, State, StartMode, StartName, PathName, ExitCode, ServiceSpecificExitCode | Out-String
    } catch {
        $lines += 'service query unavailable: ' + $_.Exception.Message
        Log ('service diagnostics unavailable: {0}' -f $_.Exception.Message)
    }
    $lines += '--- sc.exe qc DrainCtl ---'
    $lines += Get-ServiceCommandOutput @('qc', 'DrainCtl')
    $lines += '--- sc.exe qfailure DrainCtl ---'
    $lines += Get-ServiceCommandOutput @('qfailure', 'DrainCtl')
    Write-TextFile $path $lines
}

function Collect-WerConfiguration([string]$stamp) {
    $path = Join-Path $diagsDir ('wer-localdumps-{0}.txt' -f $stamp)
    $keys = @(
        'HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting\LocalDumps',
        'HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting\LocalDumps\drainctld.exe'
    )
    $lines = @(
        '# Windows Error Reporting LocalDumps configuration for drainctld.exe',
        '# Captured UTC: ' + ([DateTime]::UtcNow.ToString('o'))
    )
    foreach ($key in $keys) {
        $lines += '--- ' + $key + ' ---'
        if (-not (Test-Path -LiteralPath $key)) {
            $lines += 'not configured'
            Log ('WER registry key not found: {0}' -f $key)
            continue
        }
        try {
            $properties = Get-ItemProperty -LiteralPath $key -ErrorAction Stop
            $values = $properties.PSObject.Properties | Where-Object { $_.Name -notmatch '^PS' }
            if ($values.Count -eq 0) {
                $lines += '(no values)'
            } else {
                foreach ($value in $values) {
                    $lines += '{0}={1}' -f $value.Name, $value.Value
                }
            }
        } catch {
            $lines += 'read failed: ' + $_.Exception.Message
            Log ('WER registry read failed for {0}: {1}' -f $key, $_.Exception.Message)
        }
    }
    Write-TextFile $path $lines
}

function Collect-CrashEvents([string]$stamp) {
    $path = Join-Path $diagsDir ('crash-events-{0}.log' -f $stamp)
    $start = (Get-Date).AddDays(-7)
    $lines = @(
        '# DrainCtl-related SCM and application crash events from the last 7 days',
        '# Captured UTC: ' + ([DateTime]::UtcNow.ToString('o'))
    )
    $queries = @(
        @{ Name = 'System'; Filter = @{ LogName = 'System'; Id = @(7031, 7034); StartTime = $start }; Match = 'DrainCtl|drainctld' },
        @{ Name = 'Application'; Filter = @{ LogName = 'Application'; ProviderName = @('Application Error', 'Windows Error Reporting', '.NET Runtime'); StartTime = $start }; Match = 'DrainCtl|drainctld' }
    )
    foreach ($query in $queries) {
        try {
            $events = Get-WinEvent -FilterHashtable $query.Filter -ErrorAction Stop |
                Where-Object { $_.Message -match $query.Match }
            if (@($events).Count -eq 0) {
                $lines += ('[{0}] no matching events' -f $query.Name)
                continue
            }
            foreach ($event in $events) {
                $lines += ('[{0}] UTC={1:o} ID={2} Provider={3} Level={4}' -f $query.Name, $event.TimeCreated.ToUniversalTime(), $event.Id, $event.ProviderName, $event.LevelDisplayName)
                $lines += $event.Message.Trim()
                $lines += ''
            }
        } catch {
            $lines += ('[{0}] unavailable: {1}' -f $query.Name, $_.Exception.Message)
            Log ('event channel {0} unavailable: {1}' -f $query.Name, $_.Exception.Message)
        }
    }
    Write-TextFile $path $lines
}

function Collect-DumpInventory([string]$stamp) {
    $path = Join-Path $diagsDir ('dump-inventory-{0}.txt' -f $stamp)
    $dumpDir = Join-Path $dataDir 'dumps'
    $lines = @(
        '# WER dump inventory. Dump bytes and hashes are not read by default.',
        '# Captured UTC: ' + ([DateTime]::UtcNow.ToString('o')),
        '# Columns: name | size_bytes | last_write_utc'
    )
    if (-not (Test-Path -LiteralPath $dumpDir)) {
        $lines += 'dump folder not found: ' + $dumpDir
        Log ('dump folder not found: {0}' -f $dumpDir)
        Write-TextFile $path $lines
        return $null
    }
    try {
        $dumps = @(Get-ChildItem -LiteralPath $dumpDir -File -Filter '*.dmp' -ErrorAction Stop |
            Sort-Object LastWriteTimeUtc -Descending)
        if ($dumps.Count -eq 0) {
            $lines += '(no dumps found)'
        }
        foreach ($dump in $dumps) {
            $lines += ('{0} | {1} | {2:o}' -f $dump.Name, $dump.Length, $dump.LastWriteTimeUtc)
        }
        Write-TextFile $path $lines
        return $dumps | Select-Object -First 1
    } catch {
        $lines += 'inventory failed: ' + $_.Exception.Message
        Log ('dump inventory failed: {0}' -f $_.Exception.Message)
        Write-TextFile $path $lines
        return $null
    }
}

try {
    $stamp = (Get-Date).ToString('yyyyMMdd-HHmm')
    Collect-ServiceDiagnostics $stamp
    Collect-WerConfiguration $stamp
    Collect-CrashEvents $stamp
    $newestDump = Collect-DumpInventory $stamp

    if ($env:DRAINCTL_INCLUDE_CRASH_DUMPS -eq '1') {
        if ($null -eq $newestDump) {
            Log 'crash dump inclusion requested, but no dump is available.'
        } else {
            $dumpDir = Join-Path $dataDir 'dumps'
            $dumpArchive = Join-Path $dumpDir ('crash-dump-{0}-{1}.gz' -f $stamp, $newestDump.Name)
            $dumpHash = "$dumpArchive.sha256"
            try {
                $sha256 = Get-Sha256Hex $newestDump.FullName
                GzipFile $newestDump.FullName $dumpArchive
                @('{0} *{1}' -f $sha256, $newestDump.Name) |
                    Set-Content -LiteralPath $dumpHash -Encoding ASCII
                Log ('SENSITIVITY WARNING: hashed and gzip-copied newest crash dump only into protected dump directory: {0}. The archive and hash remain local; do not copy them to diagnostics or upload them.' -f $dumpDir)
            } catch {
                Log ('crash dump hash or gzip failed for {0}: {1}' -f $newestDump.Name, $_.Exception.Message)
            }
        }
    }

    $portRaw = $env:DRAINCTL_PPROF_PORT
    $port = 0
    if ([string]::IsNullOrEmpty($portRaw)) {
        Log 'DRAINCTL_PPROF_PORT not set; pprof and selfmetrics collection skipped.'
    } elseif (-not [int]::TryParse($portRaw, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        Log 'DRAINCTL_PPROF_PORT is invalid; pprof and selfmetrics collection skipped.'
    } else {
        $endpoints = 'heap','goroutine','allocs','threadcreate'
        foreach ($p in $endpoints) {
            $raw = Join-Path $diagsDir ('{0}-{1}.pprof' -f $p, $stamp)
            $gz  = "$raw.gz"
            try {
                Invoke-WebRequest -UseBasicParsing -Uri ('http://127.0.0.1:{0}/debug/pprof/{1}' -f $port, $p) `
                    -OutFile $raw -TimeoutSec 10
            } catch {
                Log ('fetch {0} failed: {1}' -f $p, $_.Exception.Message)
                if (Test-Path $raw) { Remove-Item $raw -Force -ErrorAction SilentlyContinue }
                continue
            }
            try {
                GzipFile $raw $gz
                Remove-Item $raw -Force
            } catch {
                Log ('gzip {0} failed: {1}' -f $p, $_.Exception.Message)
            }
        }

        # Selfmetrics excerpt — read the rotating file log(s) and grep for the
        # selfmetrics emission tag. Only present when log_file_level=debug.
        $selfPath = Join-Path $diagsDir ('selfmetrics-{0}.log' -f $stamp)
        $logs = Get-ChildItem -Path $dataDir -Filter 'drainctl*.log' -File -ErrorAction SilentlyContinue |
                Sort-Object LastWriteTime -Descending |
                Select-Object -First 2
        $hits = @()
        foreach ($f in $logs) {
            try {
                $hits += Get-Content -Path $f.FullName -ErrorAction Stop |
                         Where-Object { $_ -match 'msg=selfmetrics' }
            } catch {
                Log ('read {0} failed: {1}' -f $f.Name, $_.Exception.Message)
            }
        }
        if ($hits.Count -eq 0) {
            @(
                '# no selfmetrics emissions found in recent drainctl*.log',
                '# selfmetrics emits at slog.Debug — set log_file_level=debug in config.json to enable.',
                '# config: %ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json'
            ) | Set-Content -Path $selfPath -Encoding UTF8
        } else {
            $hits | Select-Object -Last 120 | Set-Content -Path $selfPath -Encoding UTF8
        }
    }

    # Retention: drop diagnostics collected by this task after 7 days.
    $cutoff = (Get-Date).AddDays(-7)
    Get-ChildItem -Path $diagsDir -File -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Name -like '*.pprof.gz' -or
            $_.Name -like 'selfmetrics-*.log' -or
            $_.Name -like 'service-*.txt' -or
            $_.Name -like 'wer-localdumps-*.txt' -or
            $_.Name -like 'crash-events-*.log' -or
            $_.Name -like 'dump-inventory-*.txt'
        } |
        Where-Object { $_.LastWriteTime -lt $cutoff } |
        Remove-Item -Force -ErrorAction SilentlyContinue

    # Only task-created opt-in archives are eligible for cleanup. WER-owned .dmp
    # files are never selected or deleted here.
    Get-ChildItem -Path (Join-Path $dataDir 'dumps') -File -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Name -like 'crash-dump-*.dmp.gz' -or
            $_.Name -like 'crash-dump-*.dmp.gz.sha256'
        } |
        Where-Object { $_.LastWriteTime -lt $cutoff } |
        Remove-Item -Force -ErrorAction SilentlyContinue
} catch {
    Log ('fatal: {0}' -f $_.Exception.Message)
    throw
}
