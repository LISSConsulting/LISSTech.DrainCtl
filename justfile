set shell := ["pwsh", "-NoProfile", "-Command"]
set dotenv-load

# Paths
dist_dir     := justfile_directory() / "dist"
module_dir   := dist_dir / "LISSTech.DrainCtl"
bin_dir      := module_dir / "bin"
ca_dir       := dist_dir / "customactions"
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

# Print build header with timestamp
[private]
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
header recipe:
    $d = Get-Date -Format 'MMM d yyyy'
    $t = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🚀 Starting just {{recipe}}  " -NoNewline -ForegroundColor Cyan
    Write-Host "·  $d $t" -ForegroundColor DarkGray

# ── Version ──────────────────────────────────────────────────────────────────

# Print the version that the next build will embed.
# Derived from the HEAD commit's date and same-day commit count by
# scripts/version.ps1; no files are modified.
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
version:
    $clean = & "{{justfile_directory()}}/scripts/version.ps1"
    $full  = & "{{justfile_directory()}}/scripts/version.ps1" -Full
    $msi   = & "{{justfile_directory()}}/scripts/msi-version.ps1"
    Write-Host "   clean: $clean" -ForegroundColor DarkGray
    Write-Host "   full:  $full"  -ForegroundColor DarkGray
    Write-Host "   msi:   $msi"   -ForegroundColor DarkGray

# ── Build ────────────────────────────────────────────────────────────────────

# Dev build with auth bypass (NEVER deploy to production)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
dev: (header "dev")
    Write-Host "`n⚠️  Building DEV mode (auth bypassed)" -ForegroundColor Yellow
    & go build -trimpath -buildvcs=false -tags devmode -ldflags "-s -w" -o "{{bin_dir}}/drainctl.exe" ./cmd/drainctl/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.exe").Length / 1MB)
    Write-Host "   drainctl.exe ($size) — SSPI auth DISABLED" -ForegroundColor Yellow


# Compile ETW manifest → resource DLL (assets/drainctl-msg.dll)
# Requires Windows SDK mc.exe/rc.exe and MSVC link.exe.
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
man:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔨 Compiling ETW manifest  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray

    # Locate MSVC link.exe (not MinGW's hardlink utility).
    $vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
    $vsPath = & $vswhere -latest -property installationPath 2>$null
    if (-not $vsPath) { Write-Error "Visual Studio not found (vswhere failed)"; exit 1 }
    $msvcLink = Get-ChildItem "$vsPath\VC\Tools\MSVC\*\bin\Hostx64\x64\link.exe" | Sort-Object FullName | Select-Object -Last 1
    if (-not $msvcLink) { Write-Error "MSVC link.exe not found under $vsPath"; exit 1 }

    Push-Location assets
    & mc -um drainctl.man
    if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
    & rc drainctl.rc
    if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
    & $msvcLink /DLL /NOENTRY /MACHINE:X64 /OUT:drainctl-msg.dll drainctl.res
    if ($LASTEXITCODE -ne 0) { Pop-Location; exit $LASTEXITCODE }
    Remove-Item -ErrorAction SilentlyContinue drainctl.rc, drainctl.h, drainctlTEMP.BIN, MSG00409.bin, drainctl.res
    Pop-Location
    Write-Host "   drainctl-msg.dll" -ForegroundColor DarkGray

# Render drainctl.rc from its template with the git-derived version,
# then compile to drainctl.syso. Both .rc and .syso are build artifacts.
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
resource:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔨 Compiling Windows resource file  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $ver = & "{{justfile_directory()}}/scripts/version.ps1"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $csv = & "{{justfile_directory()}}/scripts/version.ps1" -Csv
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $tmpl = Get-Content "cmd/drainctl/drainctl.rc.tmpl" -Raw
    $rc = $tmpl -replace '\{\{VERSION_CSV\}\}', $csv -replace '\{\{VERSION\}\}', $ver
    Set-Content "cmd/drainctl/drainctl.rc" -Value $rc -NoNewline
    & windres cmd/drainctl/drainctl.rc -o cmd/drainctl/drainctl.syso
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   drainctl.syso — v$ver" -ForegroundColor DarkGray

# Build the Svelte dashboard (runs pnpm build in frontend/)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
frontend:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🎨 Building frontend  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    Push-Location "{{justfile_directory()}}/frontend"
    try {
        & pnpm build
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } finally {
        Pop-Location
    }
    Write-Host "   frontend/dist/" -ForegroundColor DarkGray

# Copy Vite build output to internal/dashboard/dist/ for Go embedding
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
frontend-copy: frontend
    $src  = "{{justfile_directory()}}/frontend/dist"
    $dest = "{{justfile_directory()}}/internal/dashboard/dist"
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $dest
    Copy-Item -Recurse $src $dest
    Write-Host "   → internal/dashboard/dist/" -ForegroundColor DarkGray

# Build the CLI binary
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
cli: frontend-copy resource
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔨 Building CLI  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $ver = & "{{justfile_directory()}}/scripts/version.ps1" -Full
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & go build -trimpath -buildvcs=false -ldflags "-s -w -X github.com/LISSConsulting/LISSTech.DrainCtl.Version=$ver" -o "{{bin_dir}}/drainctl.exe" ./cmd/drainctl/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.exe").Length / 1MB)
    Write-Host "   drainctl.exe ($size) — v$ver" -ForegroundColor DarkGray

# Build the C-shared DLL (requires CGo + MinGW)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
dll:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔨 Building DLL  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $ver = & "{{justfile_directory()}}/scripts/version.ps1" -Full
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $env:CGO_ENABLED = "1"
    & go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-s -w -X github.com/LISSConsulting/LISSTech.DrainCtl.Version=$ver" -o "{{bin_dir}}/drainctl.dll" ./cmd/cshared/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Remove-Item -ErrorAction SilentlyContinue "{{bin_dir}}/drainctl.h"
    $size = "{0:N1} MB" -f ((Get-Item "{{bin_dir}}/drainctl.dll").Length / 1MB)
    Write-Host "   drainctl.dll ($size) — v$ver" -ForegroundColor DarkGray

# Render the PS module manifest from its template and copy the .psm1.
# ModuleVersion is injected from scripts/version.ps1.
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
psmodule: cli dll
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n📦 Copying PowerShell module  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $ver = & "{{justfile_directory()}}/scripts/version.ps1"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $tmpl = Get-Content "powershell/LISSTech.DrainCtl.psd1.tmpl" -Raw
    $psd1 = $tmpl -replace '\{\{VERSION\}\}', $ver
    Set-Content "{{module_dir}}/LISSTech.DrainCtl.psd1" -Value $psd1 -NoNewline
    Copy-Item "powershell/LISSTech.DrainCtl.psm1" "{{module_dir}}/"
    Write-Host "   LISSTech.DrainCtl.psd1 — v$ver" -ForegroundColor DarkGray
    Write-Host "   LISSTech.DrainCtl.psm1" -ForegroundColor DarkGray

# Build the MSI custom action DLL.
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
msica:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔨 Building MSI custom action  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    New-Item -ItemType Directory -Force "{{ca_dir}}" | Out-Null
    $env:CGO_ENABLED = "1"
    & go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-s -w" -o "{{ca_dir}}/drainctl-msica.dll" ./cmd/msica/
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Remove-Item -ErrorAction SilentlyContinue "{{ca_dir}}/drainctl-msica.h"
    $size = "{0:N1} MB" -f ((Get-Item "{{ca_dir}}/drainctl-msica.dll").Length / 1MB)
    Write-Host "   drainctl-msica.dll ($size)" -ForegroundColor DarkGray

# Build the WiX MSI installer
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
msi: psmodule msica
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n📦 Building MSI  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $appVer = & "{{justfile_directory()}}/scripts/version.ps1"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $msiVer = & "{{justfile_directory()}}/scripts/msi-version.ps1"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & dotnet build "{{installer_dir}}/LISSTech.DrainCtl.wixproj" -c Release -p:Platform=x64 "-p:ProductVersion=$msiVer" "-p:AppVersion=$appVer" -nologo -v:q
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $size = "{0:N1} MB" -f ((Get-Item "{{dist_dir}}/LISSTech.DrainCtl.msi").Length / 1MB)
    Write-Host "   LISSTech.DrainCtl.msi ($size) — app v$appVer, msi v$msiVer" -ForegroundColor DarkGray

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
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔏 Signing binaries and module  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
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

    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔏 Signing MSI  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    $out = & signtool sign /sha1 $thumbprint /d $description /fd sha256 /tr $timestampUrl /td sha256 /a /ph $msiPath 2>&1
    if ($LASTEXITCODE -ne 0) { Write-Error "Failed: LISSTech.DrainCtl.msi`n$out"; exit $LASTEXITCODE }
    Write-Host "   ✅ LISSTech.DrainCtl.msi" -ForegroundColor Green

# ── Aggregate ────────────────────────────────────────────────────────────────

# Build everything (CLI + DLL + PS module + MSI), unsigned
all: (header "all") msi

# Build and sign everything: binaries → sign → MSI → sign MSI
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
release: (header "release") gotest psmodule sign-binaries msi sign-msi
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
publish: (header "publish")
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
    $prevTag = & git describe --tags --abbrev=0 "$tag^" 2>$null
    if ($prevTag) {
        $notes = & git log "$prevTag..$tag" --pretty=format:"- %s" --no-merges
        $body = "## What's Changed`n`n$($notes -join "`n")`n`n**Full Changelog**: https://github.com/LISSConsulting/LISSTech.DrainCtl/compare/$prevTag...$tag"
    } else {
        $body = "Initial release"
    }
    & gh release create $tag @assets --title "LISSTech DrainCtl $version" --notes $body
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
lint: (header "lint")
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🔍 Linting  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    $unformatted = & gofmt -l . cmd/drainctl/ cmd/cshared/ 2>&1
    if ($unformatted) { Write-Error "gofmt: $unformatted"; exit 1 }
    & golangci-lint run ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   ✅ All clean" -ForegroundColor Green

# Run Go tests
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
gotest:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🧪 Running Go tests  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    & go test -p 1 -count=1 ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   ✅ All tests pass" -ForegroundColor Green

# Check for known vulnerabilities in dependencies
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
vulncheck:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🛡️ Vulnerability scan  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    & govulncheck ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "   ✅ No vulnerabilities" -ForegroundColor Green

# Run all quality checks: lint + test + vulncheck
check: (header "check") lint gotest vulncheck

# Format all Go source files
fmt:
    gofmt -w . cmd/drainctl/ cmd/cshared/

# Format frontend (Svelte/JS/CSS) with Prettier + docs HTML
fmt-web:
    cd frontend && pnpm exec prettier --write "src/**/*.svelte" "src/**/*.js" "src/**/*.css"
    npx --yes prettier --write "docs/**/*.html" --print-width 120 --no-bracket-same-line

# ── Test ─────────────────────────────────────────────────────────────────────

# Test the PS module (requires psmodule built)
[script('pwsh', '-NoProfile')]
[extension('.ps1')]
test:
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🧪 Testing PowerShell module  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
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
clean: (header "clean")
    $ts = Get-Date -Format 'h:mm:ss tt'
    Write-Host "`n🧹 Cleaning  " -NoNewline -ForegroundColor Cyan; Write-Host "·  $ts" -ForegroundColor DarkGray
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "{{dist_dir}}"
    Write-Host "   dist/ removed" -ForegroundColor DarkGray
