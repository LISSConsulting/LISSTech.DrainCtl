#Requires -Version 5.1
<#
.SYNOPSIS
    Emit the git-derived CalVer version string.

.DESCRIPTION
    The version is `YY.DOY.N` where:
      YY  = last two digits of HEAD commit's committer-local date
      DOY = day-of-year (1-366) of that date
      N   = count of commits in HEAD's ancestry sharing the same
            committer-local date, 0-indexed (first commit of the day is 0)

    This is deterministic: the same commit always yields the same
    version, and each commit gets a distinct value because N advances.

.PARAMETER Full
    Append `-dirty+g<sha>` when the working tree has uncommitted
    changes. Only used for Go ldflags injection (shown in
    `drainctl --version`). Windows PE and MSI metadata always use
    the clean 3-part form.

.PARAMETER Csv
    Emit comma-separated form `YY,DOY,N,0` suitable for the .rc
    FILEVERSION / PRODUCTVERSION fields.

.OUTPUTS
    One line: the version string, no trailing newline beyond the
    default pipeline terminator.

.EXAMPLE
    PS> scripts/version.ps1
    26.108.3

    PS> scripts/version.ps1 -Full
    26.108.3-dirty+ga1b2c3d

    PS> scripts/version.ps1 -Csv
    26,108,3,0
#>

[CmdletBinding()]
param(
    [switch]$Full,
    [switch]$Csv
)

$ErrorActionPreference = 'Stop'

$commitDateStr = (& git show -s --format=%cs HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or -not $commitDateStr) {
    throw "git show failed; is this a git repo with at least one commit?"
}

$dt = [DateTime]::ParseExact($commitDateStr, 'yyyy-MM-dd', $null)
$yy = $dt.Year % 100
$doy = $dt.DayOfYear

$sameDayCount = (& git log HEAD --format='%cs' |
    Where-Object { $_ -eq $commitDateStr }).Count
$n = [Math]::Max(0, $sameDayCount - 1)

if ($Csv) {
    "$yy,$doy,$n,0"
    return
}

$version = "$yy.$doy.$n"

if ($Full) {
    $dirty = & git status --porcelain
    if ($dirty) {
        $sha = (& git rev-parse --short HEAD).Trim()
        $version = "$version-dirty+g$sha"
    }
}

$version
