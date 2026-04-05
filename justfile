set shell := ["pwsh", "-NoProfile", "-Command"]
set dotenv-load

# Paths
dist_dir     := justfile_directory() / "dist"
module_dir   := dist_dir / "LISSTech.DrainCtl"
bin_dir      := module_dir / "bin"
installer_dir := justfile_directory() / "installer"

# Code signing (set CODE_SIGNING_CERTIFICATE_THUMBPRINT in .env or environment)
signing_thumbprint := env("CODE_SIGNING_CERTIFICATE_THUMBPRINT", "")
timestamp_url      := "http://timestamp.digicert.com"
sign_description   := "LISSTech DrainCtl"

# PSGallery (set PSGALLERY_API_KEY in .env or environment)
psgallery_key := env("PSGALLERY_API_KEY", "")

[private]
default:
    @just --list

# ── Version ──────────────────────────────────────────────────────────────────

# Bump patch version (CalVer YY.DOY.patch) across all 8 files + recompile .syso
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
bump:
    $exePath = "{{bin_dir}}/drainctl.exe"

    # Determine current version from source
    $src = Get-Content "drainctl.go" -Raw
    if ($src -match 'Version\s*=\s*"([^"]+)"') {
        $current = $Matches[1]
    } else {
        Write-Error "Could not read version from drainctl.go"
        exit 1
    }

    # Parse and bump patch
    $parts = $current -split '\.'
    $yy = (Get-Date).Year % 100
    $doy = (Get-Date).DayOfYear
    if ([int]$parts[0] -eq $yy -and [int]$parts[1] -eq $doy) {
        $patch = [int]$parts[2] + 1
    } else {
        $patch = 0
    }
    $new = "$yy.$doy.$patch"

    Write-Host "`n🔖 Bumping version: $current → $new" -ForegroundColor Cyan

    # Update all version-bearing files
    $files = @(
        "drainctl.go",
        "installer/LISSTech.DrainCtl.wxs",
        "installer/LISSTech.DrainCtl.wixproj",
        "powershell/LISSTech.DrainCtl.psd1",
        "README.md",
        "CLAUDE.md",
        "docs/index.html"
    )
    foreach ($f in $files) {
        (Get-Content $f -Raw) -replace [regex]::Escape($current), $new | Set-Content $f -NoNewline
        Write-Host "   $f" -ForegroundColor DarkGray
    }

    # RC file has comma-separated version too
    $rc = Get-Content "cmd/drainctl/drainctl.rc" -Raw
    $oldComma = $current -replace '\.', ','
    $newComma = $new -replace '\.', ','
    $rc = $rc -replace [regex]::Escape("$oldComma,0"), "$newComma,0"
    $rc = $rc -replace [regex]::Escape($current), $new
    $rc | Set-Content "cmd/drainctl/drainctl.rc" -NoNewline
    Write-Host "   cmd/drainctl/drainctl.rc" -ForegroundColor DarkGray

    # Recompile .syso
    & windres cmd/drainctl/drainctl.rc -o cmd/drainctl/drainctl.syso
    if ($LASTEXITCODE -ne 0) { Write-Error "windres failed"; exit $LASTEXITCODE }
    Write-Host "   cmd/drainctl/drainctl.syso (recompiled)" -ForegroundColor DarkGray

    Write-Host "   ✅ Version is now $new" -ForegroundColor Green
    Write-Host ""

# ── Build ────────────────────────────────────────────────────────────────────

# Dev build with auth bypass (NEVER deploy to production)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
dev:
    Write-Host "`n⚠️  Building DEV mode (auth bypassed)" -ForegroundColor Yellow
    & go build -tags devmode -ldflags "-s -w" -o "{{bin_dir}}/drainctl.exe" ./cmd/drainctl/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.exe").Length / 1MB)
    Write-Host "   drainctl.exe ($size) — SSPI auth DISABLED" -ForegroundColor Yellow


# Compile Windows resource file (icon + version info)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
resource:
    Write-Host "`n🔨 Compiling Windows resource file" -ForegroundColor Cyan
    & windres cmd/drainctl/drainctl.rc -o cmd/drainctl/drainctl.syso
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   drainctl.syso" -ForegroundColor DarkGray

# Build the CLI binary
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
cli:
    Write-Host "`n🔨 Building CLI" -ForegroundColor Cyan
    & go build -ldflags "-s -w" -o "{{bin_dir}}/drainctl.exe" ./cmd/drainctl/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.exe").Length / 1MB)
    Write-Host "   drainctl.exe ($size)" -ForegroundColor DarkGray

# Build the C-shared DLL (requires CGo + MinGW)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
dll:
    Write-Host "`n🔨 Building DLL" -ForegroundColor Cyan
    $env:CGO_ENABLED = "1"
    & go build -buildmode=c-shared -ldflags "-s -w" -o "{{bin_dir}}/drainctl.dll" ./cmd/cshared/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Remove-Item -ErrorAction SilentlyContinue "{{bin_dir}}/drainctl.h"
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.dll").Length / 1MB)
    Write-Host "   drainctl.dll ($size)" -ForegroundColor DarkGray

# Copy PowerShell module files
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
psmodule: cli dll
    Write-Host "`n📦 Copying PowerShell module" -ForegroundColor Cyan
    Copy-Item "powershell/LISSTech.DrainCtl.psd1" "{{module_dir}}/"
    Copy-Item "powershell/LISSTech.DrainCtl.psm1" "{{module_dir}}/"
    Write-Host "   LISSTech.DrainCtl.psd1" -ForegroundColor DarkGray
    Write-Host "   LISSTech.DrainCtl.psm1" -ForegroundColor DarkGray

# Build the WiX MSI installer
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
msi: psmodule
    Write-Host "`n📦 Building MSI" -ForegroundColor Cyan
    & dotnet build "{{installer_dir}}/LISSTech.DrainCtl.wixproj" -c Release -p:Platform=x64 -nologo -v:q
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{dist_dir}}/LISSTech.DrainCtl.msi").Length / 1MB)
    Write-Host "   LISSTech.DrainCtl.msi ($size)" -ForegroundColor DarkGray

# ── Sign ─────────────────────────────────────────────────────────────────────

# Sign binaries and PS module files (before MSI packaging)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
sign-binaries:
    $thumbprint = "{{signing_thumbprint}}"
    $timestampUrl = "{{timestamp_url}}"
    $description = "{{sign_description}}"
    $binDir = "{{bin_dir}}"
    $moduleDir = "{{module_dir}}"

    if (-not $thumbprint) {
        Write-Host "`n⏭️  Skipping signing (no certificate)" -ForegroundColor Yellow
        exit 0
    }

    $cert = Get-ChildItem Cert:\CurrentUser\My | Where-Object Thumbprint -eq $thumbprint
    if (-not $cert) { $cert = Get-ChildItem Cert:\LocalMachine\My | Where-Object Thumbprint -eq $thumbprint }
    if (-not $cert) { Write-Error "Certificate with thumbprint $thumbprint not found"; exit 1 }

    $cn = $cert.Subject -replace '^CN=', '' -replace ',.*', ''
    Write-Host "`n🔏 Signing binaries and module" -ForegroundColor Cyan
    Write-Host "   Certificate: $cn" -ForegroundColor DarkGray
    Write-Host "   Thumbprint:  $($thumbprint.Substring(0,8))..." -ForegroundColor DarkGray

    # PowerShell files (Authenticode)
    foreach ($file in @(
        (Join-Path $moduleDir "LISSTech.DrainCtl.psm1"),
        (Join-Path $moduleDir "LISSTech.DrainCtl.psd1"),
        "docs/install.ps1"
    )) {
        if (-not (Test-Path $file)) { Write-Error "Not found: $file"; exit 1 }
        $name = [System.IO.Path]::GetFileName($file)
        Set-AuthenticodeSignature -FilePath $file -Certificate $cert -TimestampServer $timestampUrl -HashAlgorithm SHA256 | Out-Null
        if ((Get-AuthenticodeSignature $file).Status -ne 'Valid') { Write-Error "Failed: $name"; exit 1 }
        Write-Host "   ✅ $name" -ForegroundColor Green
    }

    # Binaries (signtool)
    foreach ($file in @(
        (Join-Path $binDir "drainctl.exe"),
        (Join-Path $binDir "drainctl.dll")
    )) {
        if (-not (Test-Path $file)) { Write-Error "Not found: $file"; exit 1 }
        $name = [System.IO.Path]::GetFileName($file)
        $out = & signtool sign /sha1 $thumbprint /d $description /fd sha256 /tr $timestampUrl /td sha256 /a /ph $file 2>&1
        if ($LASTEXITCODE -ne 0) { Write-Error "Failed: $name`n$out"; exit $LASTEXITCODE }
        Write-Host "   ✅ $name" -ForegroundColor Green
    }

# Sign the MSI installer (after packaging)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
sign-msi:
    $thumbprint = "{{signing_thumbprint}}"
    $timestampUrl = "{{timestamp_url}}"
    $description = "{{sign_description}}"
    $msiPath = "{{dist_dir}}/LISSTech.DrainCtl.msi"

    if (-not $thumbprint) {
        Write-Host "`n⏭️  Skipping MSI signing (no certificate)" -ForegroundColor Yellow
        exit 0
    }

    if (-not (Test-Path $msiPath)) { Write-Error "MSI not found: $msiPath"; exit 1 }

    Write-Host "`n🔏 Signing MSI" -ForegroundColor Cyan
    $out = & signtool sign /sha1 $thumbprint /d $description /fd sha256 /tr $timestampUrl /td sha256 /a /ph $msiPath 2>&1
    if ($LASTEXITCODE -ne 0) { Write-Error "Failed: LISSTech.DrainCtl.msi`n$out"; exit $LASTEXITCODE }
    Write-Host "   ✅ LISSTech.DrainCtl.msi" -ForegroundColor Green

# ── Aggregate ────────────────────────────────────────────────────────────────

# Build everything (CLI + DLL + PS module + MSI), unsigned
all: msi

# Build and sign everything: binaries → sign → MSI → sign MSI
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
release: psmodule sign-binaries msi sign-msi
    $exe = Get-Item "{{bin_dir}}/drainctl.exe"
    $dll = Get-Item "{{bin_dir}}/drainctl.dll"
    $msi = Get-Item "{{dist_dir}}/LISSTech.DrainCtl.msi"
    $vi = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($exe.FullName)
    Write-Host ""
    Write-Host "🚀 Release complete" -ForegroundColor Green
    Write-Host ("   drainctl.exe  {0,5:N1} MB" -f ($exe.Length / 1MB)) -ForegroundColor DarkGray
    Write-Host ("   drainctl.dll  {0,5:N1} MB" -f ($dll.Length / 1MB)) -ForegroundColor DarkGray
    Write-Host ("   MSI           {0,5:N1} MB" -f ($msi.Length / 1MB)) -ForegroundColor DarkGray
    Write-Host "   Version       $($vi.FileVersion)" -ForegroundColor DarkGray
    Write-Host ""

# Tag, create GH release, and upload signed MSI (run after `just release`)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
publish:
    $msiPath = "{{dist_dir}}/LISSTech.DrainCtl.msi"
    $cliPath = "{{bin_dir}}/drainctl.exe"

    if (-not (Test-Path $msiPath)) {
        Write-Error "MSI not found. Run 'just release' first."
        exit 1
    }

    # Verify the MSI is signed
    $sig = Get-AuthenticodeSignature $msiPath
    if ($sig.Status -ne 'Valid') {
        Write-Error "MSI is not signed. Run 'just release' first."
        exit 1
    }

    $version = (& $cliPath --version 2>&1) -replace 'drainctl version ', ''
    $tag = "v$version"

    Write-Host "`n📤 Publishing $tag" -ForegroundColor Cyan

    # Tag and push
    & git tag -a $tag -m "Release $tag"
    if ($LASTEXITCODE -ne 0) { Write-Error "git tag failed"; exit $LASTEXITCODE }
    & git push origin $tag
    if ($LASTEXITCODE -ne 0) { Write-Error "git push tag failed"; exit $LASTEXITCODE }
    Write-Host "   ✅ Tag $tag pushed" -ForegroundColor Green

    # Create release with MSI
    $assets = @($msiPath)
    & gh release create $tag @assets --title "LISSTech DrainCtl $version" --generate-notes
    if ($LASTEXITCODE -ne 0) { Write-Error "gh release create failed"; exit $LASTEXITCODE }
    Write-Host "   ✅ Release created with $($assets.Count) asset(s)" -ForegroundColor Green
    Write-Host "   https://github.com/LISSConsulting/LISSTech.DrainCtl/releases/tag/$tag" -ForegroundColor DarkGray

    # Publish PowerShell module to PSGallery
    $psKey = "{{psgallery_key}}"
    $moduleDir = "{{module_dir}}"
    if ($psKey) {
        Write-Host "`n📤 Publishing to PSGallery" -ForegroundColor Cyan
        Publish-Module -Path $moduleDir -NuGetApiKey $psKey -ErrorAction Stop
        Write-Host "   ✅ LISSTech.DrainCtl published to PSGallery" -ForegroundColor Green
    } else {
        Write-Host "`n⏭️  Skipping PSGallery (PSGALLERY_API_KEY not set)" -ForegroundColor Yellow
    }
    Write-Host ""

# Publish PS module to PSGallery (standalone, for retries after 500 errors)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
publish-psgallery:
    $psKey = "{{psgallery_key}}"
    $moduleDir = "{{module_dir}}"
    if (-not $psKey) { Write-Error "PSGALLERY_API_KEY not set in .env"; exit 1 }
    if (-not (Test-Path "$moduleDir/LISSTech.DrainCtl.psd1")) { Write-Error "Module not built. Run 'just release' first."; exit 1 }
    Write-Host "`n📤 Publishing to PSGallery" -ForegroundColor Cyan
    Publish-Module -Path $moduleDir -NuGetApiKey $psKey -ErrorAction Stop
    Write-Host "   ✅ LISSTech.DrainCtl published to PSGallery" -ForegroundColor Green

# ── Lint ─────────────────────────────────────────────────────────────────────

# Run all Go linters
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
lint:
    Write-Host "`n🔍 Linting" -ForegroundColor Cyan
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $unformatted = & gofmt -l . cmd/drainctl/ cmd/cshared/ 2>&1
    if ($unformatted) { Write-Error "gofmt: $unformatted"; exit 1 }
    & golangci-lint run ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   ✅ All clean" -ForegroundColor Green

# Format all Go source files
fmt:
    gofmt -w . cmd/drainctl/ cmd/cshared/

# Format frontend HTML/CSS/JS with Prettier
fmt-web:
    npx --yes prettier --write "docs/**/*.html" "internal/dashboard/*.html" "internal/dashboard/testdata/*.js" --print-width 120 --no-bracket-same-line

# ── Test ─────────────────────────────────────────────────────────────────────

# Test the PS module (requires psmodule built)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
test:
    Write-Host "`n🧪 Testing PowerShell module" -ForegroundColor Cyan
    Import-Module "{{module_dir}}/LISSTech.DrainCtl.psd1" -Force
    Write-Host "   Get-RDSHDrainMode" -ForegroundColor DarkGray
    Get-RDSHDrainMode | Format-List
    Write-Host "   Test-RDSHDrainMode" -ForegroundColor DarkGray
    Test-RDSHDrainMode
    Write-Host "`n   Get-RDSHDrainHistory" -ForegroundColor DarkGray
    Get-RDSHDrainHistory -Limit 3 | Format-Table
    $cmd = Get-Command drainctl -ErrorAction SilentlyContinue
    if ($cmd) { Write-Host "   drainctl on PATH: $($cmd.Source)" -ForegroundColor DarkGray }

# ── Clean ────────────────────────────────────────────────────────────────────

# Remove all build artifacts
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
clean:
    Write-Host "`n🧹 Cleaning" -ForegroundColor Cyan
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "{{dist_dir}}"
    Write-Host "   dist/ removed" -ForegroundColor DarkGray
