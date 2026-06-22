#Requires -Version 5.1
<#!
.SYNOPSIS
    Emit a monotonic MSI ProductVersion.

.DESCRIPTION
    Windows Installer upgrades depend on ProductVersion changing even when
    rebuilding the same commit. This script keeps the existing app/binary
    CalVer (`YY.MM.N`) as the first two fields and derives a monotonic third
    field from the app build number plus an MSI revision counter.

    Output format: `YY.MM.BUILD`

    BUILD = (N * 100) + R
      N = commit-derived app build number from scripts/version.ps1
      R = MSI revision counter (1-99)

    N occupies the hundreds place so BUILD reads as `N<RR>` — e.g. app
    v26.4.2 on the 5th local MSI build produces MSI v26.4.205. R only
    bumps when the MSI is rebuilt on the same commit; a new commit advances
    N and resets R to 1, so `just all` never produces a stale (or
    downgrading) ProductVersion without touching the app CalVer.

    Sources for R, in priority order:
      1. MSI_REVISION environment variable (explicit override, any origin)
      2. GITHUB_RUN_NUMBER modulo 999 + 1 (CI builds)
      3. Local per-commit counter file `.msi-revision.json` at repo root
         — bumped by `-Increment`, rolls back to 1 when commit N changes.

    `-Increment` tells the script to bump and persist the local counter
    (called by `just all`/`just release` before building the MSI). Without
    `-Increment` the script reads the current value without mutating state
    so `just version` can print it cleanly.

    Upper bound: MSI ProductVersion's build field is 16 bits (max 65535), so
    N*100 + R ≤ 65535 ⇒ N ≤ 654. Monthly commit counts should not approach this.

.EXAMPLE
    PS> scripts/msi-version.ps1 -Increment
    26.4.205
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

"$yy.$month.$build"
