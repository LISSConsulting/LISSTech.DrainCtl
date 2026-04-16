# spike-test.ps1 - Generate event log spikes for evtspike POC testing.
# Run as Administrator.
#
# Usage:
#   .\spike-test.ps1              # 50 events split across Application + System
#   .\spike-test.ps1 -Count 200   # 200 events
#   .\spike-test.ps1 -Burst       # 3 rapid bursts of 80 events with pauses

param(
    [int]$Count = 50,
    [switch]$Burst
)

$logs = @(
    @{ Log = 'Application'; Source = 'evtspike-App' },
    @{ Log = 'System';      Source = 'evtspike-Sys' }
)

foreach ($entry in $logs) {
    if (-not [System.Diagnostics.EventLog]::SourceExists($entry.Source)) {
        [System.Diagnostics.EventLog]::CreateEventSource($entry.Source, $entry.Log)
        Write-Host "  registered $($entry.Source) in $($entry.Log)" -ForegroundColor DarkGray
    }
}

$levels = @(
    [System.Diagnostics.EventLogEntryType]::Error,
    [System.Diagnostics.EventLogEntryType]::Warning,
    [System.Diagnostics.EventLogEntryType]::Information
)

function Write-Spike([int]$N) {
    $counts = @{}
    for ($i = 0; $i -lt $N; $i++) {
        $entry = $logs | Get-Random
        $lvl = $levels | Get-Random
        $id = Get-Random -Minimum 1000 -Maximum 9999
        Write-EventLog -LogName $entry.Log -Source $entry.Source -EventId $id -EntryType $lvl `
            -Message "evtspike load test $i"
        $counts[$entry.Log] = ($counts[$entry.Log] -as [int]) + 1
    }
    foreach ($k in $counts.Keys | Sort-Object) {
        Write-Host "  $k : $($counts[$k])" -ForegroundColor Cyan
    }
}

if ($Burst) {
    Write-Host "Burst mode: 3 x 80 events, 15s apart" -ForegroundColor Yellow
    for ($b = 1; $b -le 3; $b++) {
        Write-Host "  burst $b/3" -ForegroundColor Yellow
        Write-Spike 80
        if ($b -lt 3) {
            Write-Host "  waiting 15s..." -ForegroundColor DarkGray
            Start-Sleep -Seconds 15
        }
    }
} else {
    Write-Host "Writing $Count events..." -ForegroundColor Yellow
    Write-Spike $Count
}

Write-Host "Done." -ForegroundColor Green
