# Registers the LISSTech\DrainCtl-Diags scheduled task from the supplied XML,
# preserving the operator's enable bit across MSI upgrades. Invoked by the
# RegisterDiagsTask custom action; safe to run by hand to re-register.
#
# Schtasks expects UTF-16 LE for /Create /XML; we transcode via a temp file so
# the source XML in INSTALLDIR\Tasks can be checked-in/edited as UTF-8.

param(
    [Parameter(Mandatory=$true)] [string] $TaskXml
)

$ErrorActionPreference = 'Stop'
$taskName = 'LISSTech\DrainCtl-Diags'

if (-not (Test-Path -LiteralPath $TaskXml)) {
    Write-Error "task XML not found: $TaskXml"
    exit 2
}

# Capture prior enable state, if a task with this name already exists.
$wasEnabled = $false
$existing = & schtasks.exe /Query /TN $taskName /XML 2>$null
if ($LASTEXITCODE -eq 0) {
    if (($existing -join "`n") -match '<Enabled>true</Enabled>') {
        $wasEnabled = $true
    }
}

# Re-encode to UTF-16 LE for schtasks.
$content = Get-Content -LiteralPath $TaskXml -Raw
$tmp = [System.IO.Path]::GetTempFileName()
try {
    [System.IO.File]::WriteAllText($tmp, $content, [System.Text.Encoding]::Unicode)

    & schtasks.exe /Create /XML "$tmp" /TN $taskName /F | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Error "schtasks /Create failed (exit $LASTEXITCODE)"
        exit $LASTEXITCODE
    }
}
finally {
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
}

if ($wasEnabled) {
    & schtasks.exe /Change /TN $taskName /Enable | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Error "schtasks /Change /Enable failed (exit $LASTEXITCODE)"
        exit $LASTEXITCODE
    }
}
