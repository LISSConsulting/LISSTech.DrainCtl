# protect-shared-branches.ps1
#
# Hard guard against accidental deletion of long-lived shared branches.
# Exit 1 if any of the listed branches is missing from origin.
#
# Background -- 2026-10-02:
# A previous release used `gh pr merge --merge --delete-branch` on a
# develop -> trunk PR. --delete-branch deletes the PR's SOURCE branch,
# which on a develop -> trunk PR IS develop itself. develop vanished
# from origin and had to be restored by hand. The foot-gun has no warning;
# --delete-branch is correct for one-shot feature branches (release/XX,
# chore/XX, fix/XX, etc.) but is wrong for shared integration branches
# (develop, trunk, main). The fix is to (a) enforce an explicit list of
# protected branches and (b) make --delete-branch conditional on the
# source being non-protected.
#
# This script is invoked from `just release` and `just publish`
# preflight chains. Any release pipeline that ships a tag and a PR
# merge should also call it before either action.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Remote = 'origin'
)

$ErrorActionPreference = 'Stop'

# Branch names that MUST exist on the remote after the release runs.
# Edit this list to add or remove protected branches; the rest of the
# guard derives from it. This repo uses 'trunk' as the GitHub default
# branch (per origin/trunk HEAD pointing), so 'main' is intentionally
# absent; for forks that use 'main' as default, add it here.
$Protected = @('develop', 'trunk')

$missing = @()
foreach ($branch in $Protected) {
    $exists = & git ls-remote --heads $Remote $branch 2>$null
    if (-not $exists) {
        $missing += $branch
    }
}

if ($missing.Count -gt 0) {
    $missingList = $missing -join ', '
    Write-Error ("PROTECTED BRANCH MISSING from " + $Remote + ": " + $missingList +
        '. Refusing to continue. Restore from a known-good mirror, fork,' +
        ' or local clone before retrying. See CHRONICLE.md (2026-10-02 entry)' +
        ' for the v26.10.48 incident.')
    exit 1
}

$presentList = $Protected -join ', '
Write-Host ("   Protected branches present on " + $Remote + ": " + $presentList) -ForegroundColor DarkGray
exit 0