@{
    RootModule        = 'LISSTech.DrainCtl.psm1'
    ModuleVersion     = '26.99.22'
    GUID              = 'a3f7c8e1-4b2d-4f9a-8e6c-1d5b3a9f7e2c'
    Author            = 'LISS Consulting, Corp.'
    CompanyName       = 'LISS Consulting, Corp.'
    Copyright         = '(c) 2026 LISS Consulting, Corp. All rights reserved.'
    Description       = 'Know the instant someone blocks new connections on your RDSH servers. DrainCtl monitors drain mode in real time, tracks who changed it, counts active sessions, and alerts you before capacity runs out. Includes a multi-server dashboard with Kerberos SSO, webhook and ntfy.sh notifications with granular triggers, and a 90-day audit trail. Works standalone or as a Windows Service.'

    PowerShellVersion      = '5.1'
    CompatiblePSEditions   = @('Desktop', 'Core')
    ProcessorArchitecture  = 'Amd64'

    FunctionsToExport = @(
        'Get-RDSHDrainMode'
        'Test-RDSHDrainMode'
        'Get-RDSHDrainHistory'
        'Install-RDSHDrainAudit'
        'Get-RDSHDrainNotification'
        'Set-RDSHDrainNotification'
        'Get-RDSHDrainNotificationTarget'
        'Add-RDSHDrainNotificationTarget'
        'Remove-RDSHDrainNotificationTarget'
        'Test-RDSHDrainNotification'
        'Enable-RDSHDrainDashboard'
        'Disable-RDSHDrainDashboard'
        'Install-RDSHDrainCertificate'
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
            Tags         = @('RDSH', 'RemoteDesktop', 'DrainMode', 'TerminalServer', 'RDS', 'Sessions', 'Windows', 'Monitoring', 'Dashboard', 'Notifications')
            LicenseUri   = 'https://github.com/LISSConsulting/LISSTech.DrainCtl/blob/main/LICENSE'
            ProjectUri   = 'https://lissconsulting.github.io/LISSTech.DrainCtl/'
            ReleaseNotes = 'v27: JSON config (migrated from registry), multi-target webhook/ntfy notifications with granular triggers (drain_on, drain_off, alert, session_warning, etc.), WTS session tracking with utilization alerts, uPlot dashboard with session gauges, dark mode, EV code-signed.'
        }
    }
}
