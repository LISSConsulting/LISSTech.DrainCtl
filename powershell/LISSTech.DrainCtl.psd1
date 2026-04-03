@{
    RootModule        = 'LISSTech.DrainCtl.psm1'
    ModuleVersion     = '26.93.3'
    GUID              = 'a3f7c8e1-4b2d-4f9a-8e6c-1d5b3a9f7e2c'
    Author            = 'LISS Consulting, Corp.'
    CompanyName       = 'LISS Consulting, Corp.'
    Copyright         = '(c) 2026 LISS Consulting, Corp. All rights reserved.'
    Description       = 'Monitor and manage Remote Desktop Session Host drain mode (TSServerDrainMode). Reads the registry natively via a Go shared library, maintains a 90-day JSONL audit trail of state changes, and attributes changes to specific users via Windows Security Event Log (Event ID 4657). Ships drainctl.exe CLI for N-central automation and drainctl.dll for PowerShell cmdlet integration. Supports the DrainCtl Windows Service for continuous monitoring.'

    PowerShellVersion      = '5.1'
    CompatiblePSEditions   = @('Desktop', 'Core')
    ProcessorArchitecture  = 'Amd64'

    FunctionsToExport = @(
        'Get-RDSHDrainMode'
        'Test-RDSHDrainMode'
        'Get-RDSHDrainHistory'
        'Install-RDSHDrainAudit'
    )

    CmdletsToExport   = @()
    VariablesToExport  = @()
    AliasesToExport    = @()

    FileList = @(
        'LISSTech.DrainCtl.psd1'
        'LISSTech.DrainCtl.psm1'
        'bin\drainctl.exe'
        'bin\drainctl.dll'
    )

    PrivateData = @{
        PSData = @{
            Tags         = @('RDSH', 'RemoteDesktop', 'DrainMode', 'TerminalServer', 'NCentral', 'Windows', 'Monitoring')
            LicenseUri   = 'https://github.com/LISSConsulting/LISSTech.DrainCtl/blob/main/LICENSE'
            ProjectUri   = 'https://github.com/LISSConsulting/LISSTech.DrainCtl'
            ReleaseNotes = 'v26.93.3: CalVer versioning, enhanced pipe output, service event log entries.'
        }
    }
}
