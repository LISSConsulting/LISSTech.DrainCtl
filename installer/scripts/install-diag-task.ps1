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
# schtasks /Query exits non-zero with stderr text ("The system cannot find
# the file specified") when the task doesn't exist. Under
# $ErrorActionPreference='Stop' that surfaces as a terminating
# NativeCommandError despite `2>$null` (which redirects the text but not
# PowerShell's exit-code-driven error promotion). On a fresh install the
# task always doesn't exist yet → CA fails → registration silently skipped.
# Pop the EAP for just the probe so the not-found case is treated as data,
# not an exception.
$wasEnabled = $false
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = 'SilentlyContinue'
try {
    $existing = & schtasks.exe /Query /TN $taskName /XML 2>$null
    if ($LASTEXITCODE -eq 0 -and (($existing -join "`n") -match '<Enabled>true</Enabled>')) {
        $wasEnabled = $true
    }
} finally {
    $ErrorActionPreference = $prevEAP
    $global:LASTEXITCODE = 0
}

# Re-encode to UTF-16 LE for schtasks. The source XML declares
# encoding="UTF-8" so the declaration must be rewritten to match the new
# on-disk encoding — otherwise schtasks reads the UTF-16 BOM, sees
# "UTF-8" in the prolog, and aborts with "unable to switch the encoding"
# (1,40)::ERROR before any element is parsed.
$content = Get-Content -LiteralPath $TaskXml -Raw
$content = $content -replace '(?i)encoding\s*=\s*"UTF-8"', 'encoding="UTF-16"' `
                    -replace "(?i)encoding\s*=\s*'UTF-8'", "encoding='UTF-16'"
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
