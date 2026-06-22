//go:build windows

package svc

import (
	"log/slog"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/logging"

	"golang.org/x/sys/windows/svc"
)

// Event IDs matching assets/drainctl.man ETW manifest.
// Pass as slog.Int("event_id", EvtXxx) to route to the specific manifest event.
const (
	EvtServiceStarted = 1000
	EvtServiceStopped = 1001
	EvtCheckHealthy   = 1002
	EvtConfigReloaded = 1003
	EvtTransition     = 1004
	EvtGenericInfo    = 1099
	EvtCheckGrace     = 2000
	EvtGenericWarning = 2099
	EvtCheckAlert     = 3000
	EvtRegistryFailed = 3001
	EvtServiceError   = 3002
	EvtGenericError   = 3099
	// Audit event IDs (5xxx) are in the root dc package.
)

// drainService implements svc.Handler.
type drainService struct {
	fileLevel *slog.LevelVar // min level for the file sink
	etwLevel  *slog.LevelVar // min level for the ETW sink
}

// RunService starts the Windows service. Called by the CLI's hidden
// "service run" subcommand.
func RunService() error {
	// Initialise log level vars from config (fall back to defaults if config
	// is unavailable — Execute() will re-load config and apply correct values).
	fileLevel := &slog.LevelVar{}
	fileLevel.Set(slog.LevelDebug)
	etwLevel := &slog.LevelVar{}
	etwLevel.Set(slog.LevelInfo)

	if startCfg, err := dc.LoadConfig(); err == nil {
		if fl, err := logging.ParseLevel(startCfg.LogFileLevel); err == nil {
			fileLevel.Set(fl)
		}
		if el, err := logging.ParseLevel(startCfg.LogEventLevel); err == nil {
			etwLevel.Set(el)
		}
	}

	// Logging subsystem owns the ETW handler + filelog writer and installs
	// itself as slog.Default. Deferred Stop runs after svc.Run returns so
	// SCM-stop log lines still land. See internal/logging/subsystem_windows.go.
	logSub := logging.NewSubsystem(dc.DefaultDataDir()+`\drainctl.log`, 7, fileLevel, etwLevel)
	defer logSub.Stop()

	return svc.Run(dc.ServiceName, &drainService{fileLevel: fileLevel, etwLevel: etwLevel})
}

// waitWithTimeout invokes wait (typically a Subsystem.Stop) on a
// goroutine and blocks until either it returns or timeout elapses. A log
// warning is emitted when the bound is exceeded so operators see a stuck
// worker rather than a silent service-stop hang. label distinguishes
// call sites in the warning.
func waitWithTimeout(label string, wait func(), timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn(label+": did not exit within shutdown budget",
			"timeout", timeout, slog.Int("event_id", EvtGenericWarning))
	}
}
