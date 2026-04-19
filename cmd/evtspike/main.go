//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
	"golang.org/x/sys/windows"
)

var (
	wevtapi          = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = wevtapi.NewProc("EvtSubscribe")
	procEvtNext      = wevtapi.NewProc("EvtNext")
	procEvtClose     = wevtapi.NewProc("EvtClose")
)

const (
	evtSubscribeToFutureEvents = 1
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	channels := []string{
		// Core
		"Application",
		"System",
		"Security",

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
	counters := make([]atomic.Int64, len(channels))

	query := `*[System[(Level<=4)]]`

	active := 0
	for i, ch := range channels {
		if err := subscribe(ctx, ch, query, &counters[i]); err != nil {
			fmt.Fprintf(os.Stderr, "  [skip] %s: %v\n", ch, err)
			continue
		}
		active++
	}
	fmt.Printf("  subscribed to %d/%d channels\n", active, len(channels))

	cfg := evtspike.DetectorConfig{
		MinCount:                 10,
		Threshold:                1e-4,
		Cooldown:                 10 * time.Minute,
		SlotMaturityObservations: 7,
	}

	detectors := make([]*evtspike.Detector, len(channels))
	for i := range detectors {
		detectors[i] = evtspike.NewDetector(0.1, 60, 360, cfg)
	}

	fmt.Println("evtspike: watching Application + System (Level 0-4), 10s buckets")
	fmt.Println("          generate test events: eventcreate /T ERROR /ID 999 /L Application /D \"test\"")
	fmt.Println()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nshutting down")
			return
		case t := <-ticker.C:
			ts := t.Format("15:04:05")
			for i, ch := range channels {
				count := int(counters[i].Swap(0))
				r := detectors[i].ObserveBucket(t, count)

				switch {
				case r.Alert:
					fmt.Printf("  [ALERT]  %s %-55s count=%-4d mean=%-7.2f p=%.2e\n",
						ts, ch, count, r.Mean, r.TailProb)
				case r.Anomalous:
					fmt.Printf("  [spike]  %s %-55s count=%-4d mean=%-7.2f p=%.2e\n",
						ts, ch, count, r.Mean, r.TailProb)
				case count > 0:
					fmt.Printf("  [ok]     %s %-55s count=%-4d mean=%.2f\n",
						ts, ch, count, r.Mean)
				}
			}
		}
	}
}

// subscribe creates an EvtSubscribe subscription and drains events in a
// goroutine using EvtNext with a 1-second timeout. We skip EvtRender entirely
// and just count + close handles.
func subscribe(ctx context.Context, channel, query string, counter *atomic.Int64) error {
	chPtr, _ := windows.UTF16PtrFromString(channel)
	qPtr, _ := windows.UTF16PtrFromString(query)

	// Signal event for EvtSubscribe pull model.
	sigEvent, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return fmt.Errorf("CreateEvent: %w", err)
	}

	r, _, e := procEvtSubscribe.Call(
		0, uintptr(sigEvent),
		uintptr(unsafe.Pointer(chPtr)),
		uintptr(unsafe.Pointer(qPtr)),
		0, 0, 0,
		evtSubscribeToFutureEvents,
	)
	if r == 0 {
		_ = windows.CloseHandle(sigEvent)
		return fmt.Errorf("EvtSubscribe %s: %w", channel, e)
	}
	sub := r

	go func() {
		defer func() {
			_, _, _ = procEvtClose.Call(sub)
			_ = windows.CloseHandle(sigEvent)
		}()

		var evts [64]uintptr
		var returned uint32

		for {
			_, _ = windows.WaitForSingleObject(sigEvent, 1000)

			select {
			case <-ctx.Done():
				return
			default:
			}

			for {
				r, _, _ := procEvtNext.Call(
					sub, 64,
					uintptr(unsafe.Pointer(&evts[0])),
					0, 0,
					uintptr(unsafe.Pointer(&returned)),
				)
				if r == 0 || returned == 0 {
					break
				}
				counter.Add(int64(returned))
				for j := range returned {
					_, _, _ = procEvtClose.Call(evts[j])
				}
			}
		}
	}()

	return nil
}
