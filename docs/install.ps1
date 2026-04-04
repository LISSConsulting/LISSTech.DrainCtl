# LISSTech DrainCtl — one-line installer
# Usage: irm https://lissconsulting.github.io/LISSTech.DrainCtl/install.ps1 | iex
#Requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'

$repo = 'LISSConsulting/LISSTech.DrainCtl'
$asset = 'LISSTech.DrainCtl.msi'
$tmp = Join-Path $env:TEMP $asset

Write-Host "`n  DrainCtl Installer" -ForegroundColor Cyan
Write-Host "  Fetching latest release from GitHub...`n" -ForegroundColor DarkGray

$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$url = ($release.assets | Where-Object name -eq $asset).browser_download_url

if (-not $url) {
    throw "Could not find $asset in the latest release."
}

Write-Host "  Version : $($release.tag_name)" -ForegroundColor White
Write-Host "  Download: $url" -ForegroundColor DarkGray

Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
Write-Host "  Installing..." -ForegroundColor DarkGray

$proc = Start-Process msiexec.exe -ArgumentList "/i `"$tmp`" /qn" -Wait -PassThru
Remove-Item $tmp -ErrorAction SilentlyContinue

if ($proc.ExitCode -eq 0) {
    Write-Host "`n  Done! DrainCtl is installed." -ForegroundColor Green
    Write-Host "  Run 'drainctl check' to verify.`n" -ForegroundColor DarkGray
} else {
    throw "MSI install failed with exit code $($proc.ExitCode)"
}
