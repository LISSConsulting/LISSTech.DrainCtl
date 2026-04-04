//go:build windows

package dashboard

import (
	"fmt"
	"net"
	"os"
	"strings"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
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
// if workgroup/standalone. Uses USERDNSDOMAIN first (set for domain logons),
// falls back to parsing the machine's FQDN.
func machineDomain() string {
	// USERDNSDOMAIN is set when a domain user is logged on. The service
	// runs as SYSTEM which may not have it, so also try the FQDN.
	if d := os.Getenv("USERDNSDOMAIN"); d != "" {
		return strings.ToLower(d)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}

	// Resolve the hostname to get the FQDN.
	addrs, err := net.LookupHost(hostname)
	if err != nil || len(addrs) == 0 {
		return ""
	}

	// Reverse-lookup to get FQDN.
	names, err := net.LookupAddr(addrs[0])
	if err != nil || len(names) == 0 {
		return ""
	}

	fqdn := strings.TrimSuffix(names[0], ".")
	parts := strings.SplitN(fqdn, ".", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}
