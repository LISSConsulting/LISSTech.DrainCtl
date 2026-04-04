//go:build windows

package dashboard

import (
	"fmt"
	"net"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"golang.org/x/sys/windows"
)

// SRVService is the SRV service name used for dashboard discovery.
const SRVService = "drainctl"

// DiscoverDashboardURL attempts to find the dashboard server via DNS SRV
// lookup on _drainctl._tcp.<domain>. Returns the URL (e.g.
// "https://dashboard.contoso.com:49470") or an empty string if no SRV
// record is found.
func DiscoverDashboardURL(log dc.LogFunc) string {
	domain := machineDomain()
	if domain == "" {
		dc.LogMsg(log, dc.LvlINF, "dashboard discovery: machine is not domain-joined, skipping SRV lookup", "")
		return ""
	}

	_, addrs, err := net.LookupSRV(SRVService, "tcp", domain)
	if err != nil || len(addrs) == 0 {
		dc.LogMsg(log, dc.LvlINF, "dashboard discovery: no SRV record found",
			fmt.Sprintf("query=_drainctl._tcp.%s error=%v", domain, err))
		return ""
	}

	// Use the highest-priority (lowest Priority value) SRV record.
	best := addrs[0]
	target := strings.TrimSuffix(best.Target, ".")
	if target == "" {
		return ""
	}

	url := fmt.Sprintf("https://%s:%d", target, best.Port)
	log(dc.LvlINF, fmt.Sprintf("dashboard discovery: found SRV record _drainctl._tcp.%s → %s", domain, url))
	return url
}

// machineDomain returns the DNS domain the machine is joined to, or empty
// if workgroup/standalone. Uses GetComputerNameEx(ComputerNameDnsDomain),
// the canonical Win32 API for domain discovery. Works under SYSTEM.
func machineDomain() string {
	var size uint32
	// First call to get buffer size.
	_ = windows.GetComputerNameEx(windows.ComputerNameDnsDomain, nil, &size)
	if size == 0 {
		return ""
	}

	buf := make([]uint16, size)
	if err := windows.GetComputerNameEx(windows.ComputerNameDnsDomain, &buf[0], &size); err != nil {
		return ""
	}

	domain := windows.UTF16ToString(buf[:size])
	return strings.ToLower(domain)
}
