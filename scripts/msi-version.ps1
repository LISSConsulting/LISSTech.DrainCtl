#Requires -Version 5.1
<#!
.SYNOPSIS
    Emit a monotonic MSI ProductVersion.

.DESCRIPTION
    Windows Installer compares ProductVersion numerically when deciding whether
    one package may replace another. The user-facing app CalVer changed from
    legacy `YY.DOY.N` to `YY.MM.N` in June 2026, so an old `26.117.*` package
    otherwise outranks every new `26.9.*` package.

    MSI versions therefore use a new schema epoch in the major component while
    app, binary, and package names keep the normal CalVer:

      App version: `YY.MM.N`
      MSI version: `(100 + YY).MM.BUILD`
      BUILD = (N * 100) + R

      N = commit-derived app build number from scripts/version.ps1
      R = MSI revision counter (1-99)

    The epoch makes every monthly-CalVer MSI newer than every legacy
    day-of-year MSI from the same app year. It also causes a legacy package's
    own MajorUpgrade downgrade check to reject replay after the epoch package
    has been installed.

    N occupies the hundreds place so BUILD reads as `N<RR>` — e.g. app
    v26.4.2 on the 5th local MSI build produces MSI v126.4.205. R only
    bumps when the MSI is rebuilt on the same commit; a new commit advances
    N and resets R to 1, so `just all` never produces a stale ProductVersion.

    Sources for R, in priority order:
      1. MSI_REVISION environment variable (explicit override, any origin)
      2. GITHUB_RUN_NUMBER modulo 99 + 1 (CI builds)
      3. Local per-commit counter file `.msi-revision.json` at repo root
         — bumped by `-Increment`, rolls back to 1 when commit N changes.

    `-Increment` tells the script to bump and persist the local counter
    (called by `just all`/`just release` before building the MSI). Without
    `-Increment` the script reads the current value without mutating state
    so `just version` can print it cleanly.

    Upper bounds: MSI ProductVersion's major/minor fields are 8 bits and its
    build field is 16 bits. `100 + YY` tops out at 199; N*100 + R must stay
    within 65535, so N ≤ 654.

.EXAMPLE
    PS> scripts/msi-version.ps1 -Increment
    126.4.205
#>

[CmdletBinding()]
param(
    [switch]$Increment
)

$ErrorActionPreference = 'Stop'

$base = (& "$PSScriptRoot/version.ps1").Trim()
if ($LASTEXITCODE -ne 0 -or -not $base) {
    throw "version.ps1 failed"
}

$parts = $base.Split('.')
if ($parts.Length -ne 3) {
    throw "unexpected base version format: $base"
}

$yy = [int]$parts[0]
$month = [int]$parts[1]
$n = [int]$parts[2]

$msiMajor = 100 + $yy
if ($msiMajor -gt 255) {
    throw "MSI major component exceeds 255: $msiMajor"
}

$revision = $env:MSI_REVISION
if (-not $revision) {
    $run = $env:GITHUB_RUN_NUMBER
    if ($run) {
        $revision = (([int]$run - 1) % 99) + 1
    }
}

# Local fallback: persist R in a git-ignored per-commit counter so back-to-back
# `just all` invocations on the same commit produce strictly increasing MSI
# ProductVersions — otherwise msiexec refuses to upgrade "the same" MSI.
if (-not $revision) {
    $stateFile = Join-Path (Split-Path -Parent $PSScriptRoot) ".msi-revision.json"
    $storedN = 0
    $storedR = 0
    if (Test-Path $stateFile) {
        try {
            $s = Get-Content $stateFile -Raw | ConvertFrom-Json -ErrorAction Stop
            $storedN = [int]$s.n
            $storedR = [int]$s.r
        } catch {
            # Corrupt state file — treat as absent.
            $storedN = 0
            $storedR = 0
        }
    }
    if ($Increment) {
        if ($storedN -eq $n -and $storedR -ge 1) {
            $revision = $storedR + 1
        } else {
            $revision = 1
        }
        if ($revision -gt 99) {
            throw "MSI revision for commit N=$n exceeded 99 local rebuilds — make a new commit or set `$env:MSI_REVISION"
        }
        @{ n = $n; r = $revision } | ConvertTo-Json -Compress | Set-Content $stateFile -NoNewline
    } else {
        if ($storedN -eq $n -and $storedR -ge 1) {
            $revision = $storedR
        } else {
            # No build has been issued for this commit yet — report what the
            # next build would use.
            $revision = 1
        }
    }
}

$revision = [int]$revision
if ($revision -lt 1 -or $revision -gt 99) {
    throw "MSI revision must be between 1 and 99; got $revision"
}

$build = ($n * 100) + $revision
if ($build -gt 65535) {
    throw "MSI build component exceeds 65535: $build (N=$n * 100 + R=$revision)"
}

"$msiMajor.$month.$build"
