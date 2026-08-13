[CmdletBinding()]
param(
    [Parameter(Mandatory, Position = 0, ValueFromRemainingArguments)]
    [string[]] $OpArgs
)

$ErrorActionPreference = 'Stop'

$tokenPath = if ($env:OP_SERVICE_ACCOUNT_TOKEN_FILE) {
    $env:OP_SERVICE_ACCOUNT_TOKEN_FILE
} else {
    Join-Path $env:USERPROFILE '.config\1password\drainctl-build-agent.token.dpapi'
}

if (-not (Test-Path -LiteralPath $tokenPath -PathType Leaf)) {
    Write-Error "1Password service-account token file not found: $tokenPath"
    exit 1
}

$encrypted = [IO.File]::ReadAllText($tokenPath)
$secure = ConvertTo-SecureString $encrypted
$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)

try {
    $env:OP_SERVICE_ACCOUNT_TOKEN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    & op @OpArgs
    exit $LASTEXITCODE
} finally {
    Remove-Item Env:OP_SERVICE_ACCOUNT_TOKEN -ErrorAction SilentlyContinue
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
}
