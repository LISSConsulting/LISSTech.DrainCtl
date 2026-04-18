#Requires -Version 5.1
<#!
.SYNOPSIS
    Emit a monotonic MSI ProductVersion.

.DESCRIPTION
    Windows Installer upgrades depend on ProductVersion changing even when
    rebuilding the same commit. This script keeps the existing app/binary
    CalVer (`YY.DOY.N`) as the first two fields and derives a monotonic third
    field from the app build number plus an MSI revision counter.

    Output format: `YY.DOY.BUILD`

    BUILD = (N * 100) + R
      N = commit-derived app build number from scripts/version.ps1
      R = MSI revision counter (1-99, default 1)

    Sources for R, in priority order:
      1. MSI_REVISION environment variable
      2. GITHUB_RUN_NUMBER modulo 99 + 1
      3. 1 (local/default)

    This keeps ProductVersion valid for MSI (three numeric fields) while
    allowing multiple upgradeable MSI builds from the same commit.

.EXAMPLE
    PS> scripts/msi-version.ps1
    26.108.3101
#>

[CmdletBinding()]
param()

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
$doy = [int]$parts[1]
$n = [int]$parts[2]

$revision = $env:MSI_REVISION
if (-not $revision) {
    $run = $env:GITHUB_RUN_NUMBER
    if ($run) {
        $revision = (([int]$run - 1) % 99) + 1
    }
}
if (-not $revision) {
    $revision = 1
}

$revision = [int]$revision
if ($revision -lt 1 -or $revision -gt 99) {
    throw "MSI revision must be between 1 and 99; got $revision"
}

$build = ($n * 100) + $revision
if ($build -gt 65535) {
    throw "MSI build component exceeds 65535: $build"
}

"$yy.$doy.$build"
