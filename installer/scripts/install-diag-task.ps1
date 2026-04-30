# Registers the LISS Technologies\DrainCtl-Diags scheduled task from the
# supplied XML, preserving the operator's enable bit across MSI upgrades.
# Invoked by the RegisterDiagsTask custom action; safe to run by hand to
# re-register.
#
# Schtasks expects UTF-16 LE for /Create /XML; we transcode via a temp file so
# the source XML in INSTALLDIR\Tasks can be checked-in/edited as UTF-8.

param(
    [Parameter(Mandatory=$true)] [string] $TaskXml
)

$ErrorActionPreference = 'Stop'
$taskName = 'LISS Technologies\DrainCtl-Diags'
# v26.119.10 and earlier registered the task under a folder named "LISSTech".
# The d/b/a is "LISS Technologies" everywhere else (Program Files\LISS
# Technologies, ProgramData\LISS Technologies, …) so the task folder name
# was a one-off inconsistency. Delete the legacy task on every install pass
# so upgrades don't leave an orphan in the wrong folder.
$legacyTaskName = 'LISSTech\DrainCtl-Diags'

if (-not (Test-Path -LiteralPath $TaskXml)) {
    Write-Error "task XML not found: $TaskXml"
    exit 2
}

# Capture prior enable state. Look at the new path first; fall back to the
# legacy path so the operator's enable choice survives a folder rename.
# schtasks /Query exits non-zero with stderr text ("The system cannot find
# the file specified") when the task doesn't exist. Under
# $ErrorActionPreference='Stop' that surfaces as a terminating
# NativeCommandError despite `2>$null` (which redirects the text but not
# PowerShell's exit-code-driven error promotion). Pop the EAP for the
# probes so not-found cases are treated as data, not as exceptions.
$wasEnabled = $false
$prevEAP = $ErrorActionPreference
$ErrorActionPreference = 'SilentlyContinue'
try {
    foreach ($probe in @($taskName, $legacyTaskName)) {
        $existing = & schtasks.exe /Query /TN $probe /XML 2>$null
        if ($LASTEXITCODE -eq 0) {
            if (($existing -join "`n") -match '<Enabled>true</Enabled>') {
                $wasEnabled = $true
            }
            break
        }
    }
    # Always remove the legacy task — leaving it would create two scheduled
    # tasks both pointing at collect-diags.ps1 and double the snapshot rate.
    & schtasks.exe /Delete /TN $legacyTaskName /F 2>$null | Out-Null
} finally {
    $ErrorActionPreference = $prevEAP
    $global:LASTEXITCODE = 0
}

# Re-encode to UTF-16 LE for schtasks. The source XML declares
# encoding="UTF-8" so the declaration must be rewritten to match the new
# on-disk encoding — otherwise schtasks reads the UTF-16 BOM, sees
# "UTF-8" in the prolog, and aborts with "unable to switch the encoding"
# (1,40)::ERROR before any element is parsed.
#
# `-Encoding UTF8` is required: PowerShell 5.1's Get-Content -Raw defaults
# to the local ANSI codepage (CP1252 on US-English Windows) when the file
# has no BOM, so multi-byte UTF-8 sequences round-trip through CP1252 as
# mojibake (the em-dash `—` becomes `â€"`). The XML on disk is
# deliberately unBOMed UTF-8.
$content = Get-Content -LiteralPath $TaskXml -Raw -Encoding UTF8
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
