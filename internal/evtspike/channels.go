//go:build windows

package evtspike

import (
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// SecurityChannel is the Windows Security log channel name. Kept out of
// Defaults because subscribing requires SeSecurityPrivilege; admins opt in
// via EvtSpikeConfig.SecurityChannelEnabled.
const SecurityChannel = "Security"

// Defaults is the curated 54-channel default watch list. Security is NOT
// included here — see SecurityChannel.
var Defaults = []string{
	// Core
	"Application",
	"System",

	// FSLogix
	"Microsoft-FSLogix-Apps/Admin",
	"Microsoft-FSLogix-Apps/Operational",
	"Microsoft-FSLogix-CloudCache/Admin",
	"Microsoft-FSLogix-CloudCache/Operational",

	// RemoteApp / RDP core
	"Microsoft-Windows-RemoteApp and Desktop Connections/Admin",
	"Microsoft-Windows-RemoteApp and Desktop Connections/Operational",
	"Microsoft-Windows-RemoteDesktopServices-RdpCoreTS/Admin",
	"Microsoft-Windows-RemoteDesktopServices-RdpCoreTS/Operational",
	"Microsoft-Windows-RemoteDesktopServices-SessionServices/Operational",

	// SMB client
	"Microsoft-Windows-SMBClient/Operational",
	"Microsoft-Windows-SmbClient/Connectivity",
	"Microsoft-Windows-SmbClient/Audit",
	"Microsoft-Windows-SmbClient/Security",

	// SMB server
	"Microsoft-Windows-SMBServer/Operational",
	"Microsoft-Windows-SMBServer/Connectivity",

	// Terminal Services
	"Microsoft-Windows-TerminalServices-ClientUSBDevices/Admin",
	"Microsoft-Windows-TerminalServices-ClientUSBDevices/Operational",
	"Microsoft-Windows-TerminalServices-LocalSessionManager/Admin",
	"Microsoft-Windows-TerminalServices-LocalSessionManager/Operational",
	"Microsoft-Windows-TerminalServices-PnPDevices/Admin",
	"Microsoft-Windows-TerminalServices-PnPDevices/Operational",
	"Microsoft-Windows-TerminalServices-Printers/Admin",
	"Microsoft-Windows-TerminalServices-Printers/Operational",
	"Microsoft-Windows-TerminalServices-RDPClient/Operational",
	"Microsoft-Windows-TerminalServices-RemoteConnectionManager/Admin",
	"Microsoft-Windows-TerminalServices-RemoteConnectionManager/Operational",
	"Microsoft-Windows-TerminalServices-ServerUSBDevices/Admin",
	"Microsoft-Windows-TerminalServices-ServerUSBDevices/Operational",
	"Microsoft-Windows-TerminalServices-SessionBroker-Client/Admin",
	"Microsoft-Windows-TerminalServices-SessionBroker-Client/Operational",
	"Microsoft-Windows-TerminalServices-TSAppSrv-TSMSI/Admin",
	"Microsoft-Windows-TerminalServices-TSAppSrv-TSMSI/Operational",
	"Microsoft-Windows-TerminalServices-TSAppSrv-TSVIP/Admin",
	"Microsoft-Windows-TerminalServices-TSAppSrv-TSVIP/Operational",
	"Microsoft-Windows-TerminalServices-TSFairShare/Admin",
	"Microsoft-Windows-TerminalServices-TSFairShare/Operational",

	// User experience
	"Microsoft-Windows-GroupPolicy/Operational",
	"Microsoft-Windows-User Profile Service/Operational",
	"Microsoft-Windows-PrintService/Operational",
	"Microsoft-Windows-Winlogon/Operational",
	"Microsoft-Windows-Folder Redirection/Operational",
	"Microsoft-Windows-Resource-Exhaustion-Detector/Operational",

	// Auth
	"Microsoft-Windows-NTLM/Operational",
	"Microsoft-Windows-Kerberos/Operational",
	"Microsoft-Windows-LSA/Operational",

	// Infrastructure
	"Microsoft-Windows-DNS-Client/Operational",
	"Microsoft-Windows-TCPIP/Operational",
	"Microsoft-Windows-CAPI2/Operational",

	// Stability
	"Microsoft-Windows-Kernel-WHEA/Errors",
	"Microsoft-Windows-Windows Defender/Operational",
	"Microsoft-Windows-Crashdump/Operational",

	// WMI
	"Microsoft-Windows-WMI-Activity/Operational",
}

// ResolveChannels returns the ordered set of event-log channels to subscribe
// to, applying the merge rules from data-model.md §4 in order:
//
//  1. Start with Defaults. If Defaults ever contains "Security" and
//     cfg.SecurityChannelEnabled is false, the entry is dropped (defensive —
//     Security must only enter via the opt-in flag or AddedChannels).
//  2. If cfg.SecurityChannelEnabled, add "Security".
//  3. Remove any channel whose case-insensitive name appears in
//     cfg.DisabledChannels. DisabledChannels targets the curated default
//     set — it does NOT filter AddedChannels, so an admin can override a
//     disable by re-adding via AddedChannels.
//  4. Append cfg.AddedChannels. An explicit "Security" entry here is
//     preserved even when the flag is off; subscription then fails at
//     startup and is logged as skipped (FR-009).
//  5. Case-insensitive dedup, preserving the first occurrence's casing.
func ResolveChannels(cfg dc.EvtSpikeConfig) []string {
	disabled := make(map[string]struct{}, len(cfg.DisabledChannels))
	for _, name := range cfg.DisabledChannels {
		disabled[strings.ToLower(name)] = struct{}{}
	}

	base := make([]string, 0, len(Defaults)+1)
	for _, name := range Defaults {
		if !cfg.SecurityChannelEnabled && strings.EqualFold(name, SecurityChannel) {
			continue
		}
		base = append(base, name)
	}
	if cfg.SecurityChannelEnabled {
		base = append(base, SecurityChannel)
	}

	merged := make([]string, 0, len(base)+len(cfg.AddedChannels))
	for _, name := range base {
		if _, off := disabled[strings.ToLower(name)]; off {
			continue
		}
		merged = append(merged, name)
	}
	merged = append(merged, cfg.AddedChannels...)

	result := make([]string, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, name := range merged {
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result
}
