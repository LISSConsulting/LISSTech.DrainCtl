# Hourly diagnostics collector for LISSTech DrainCtl. Invoked by Task Scheduler
# (\LISS Technologies\DrainCtl-Diags). Snapshots heap/goroutine/allocs/threadcreate
# profiles from the loopback pprof endpoint and tails recent selfmetrics lines
# from the rotating file log. Output: %ProgramData%\LISS Technologies\LISSTech DrainCtl\diags
#
# Operator gate: pprof endpoint is opt-in via DRAINCTL_PPROF_PORT (machine env
# var). If the var is unset or the port is unreachable, this script no-ops.

$ErrorActionPreference = 'Stop'

$dataDir  = Join-Path $env:ProgramData 'LISS Technologies\LISSTech DrainCtl'
$diagsDir = Join-Path $dataDir 'diags'
$ownLog   = Join-Path $diagsDir 'collector.log'

New-Item -ItemType Directory -Force -Path $diagsDir | Out-Null

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

try {
    $portRaw = $env:DRAINCTL_PPROF_PORT
    if ([string]::IsNullOrEmpty($portRaw)) {
        Log 'DRAINCTL_PPROF_PORT not set; nothing to collect. Set machine env var to enable pprof on drainctld and restart the service.'
        return
    }

    $port = 0
    if (-not [int]::TryParse($portRaw, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        Log ('DRAINCTL_PPROF_PORT={0} invalid; expected 1-65535.' -f $portRaw)
        return
    }

    $stamp     = (Get-Date).ToString('yyyyMMdd-HHmm')
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

    # Retention: drop pprof + selfmetrics excerpts older than 7 days.
    $cutoff = (Get-Date).AddDays(-7)
    Get-ChildItem -Path $diagsDir -File -ErrorAction SilentlyContinue |
        Where-Object {
            ($_.Name -like '*.pprof.gz' -or $_.Name -like 'selfmetrics-*.log') -and
            $_.LastWriteTime -lt $cutoff
        } |
        Remove-Item -Force -ErrorAction SilentlyContinue
}
catch {
    Log ('fatal: {0}' -f $_.Exception.Message)
    throw
}
