//go:build windows

package svc

import (
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// InstallService registers the service with SCM.
func InstallService(exePath string) error {
	return installServiceImpl(exePath)
}

// UninstallService removes the service from SCM.
func UninstallService() error {
	return uninstallServiceImpl()
}

func installServiceImpl(exePath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.CreateService(dc.ServiceName, exePath, mgr.Config{
		DisplayName:      dc.ServiceDisplayName,
		Description:      dc.ServiceDescription,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		ServiceStartName: "NT SERVICE\\" + dc.ServiceName,
	}, "service", "run") // args passed to binary: drainctl.exe service run
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Set recovery: restart after 5 seconds on first and second failure.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * 1e9}, // 5 seconds in 100ns units
		{Type: mgr.ServiceRestart, Delay: 5 * 1e9},
		{Type: mgr.NoAction, Delay: 0},
	}, 86400); err != nil { // reset failure count after 24 hours
		slog.Warn("set_recovery_actions_failed", "error", err)
	}

	// Write default parameters.
	if err := dc.WriteDefaultParameters(); err != nil {
		slog.Warn("write_defaults_failed", "error", err)
	}

	slog.Info("service=installed", "name", dc.ServiceName)
	return nil
}

func uninstallServiceImpl() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	_ = eventlog.Remove(dc.ServiceName)
	slog.Info("service=uninstalled", "name", dc.ServiceName)
	return nil
}

// StartService sends a start request to SCM and waits up to 30 s for the
// service to reach the Running state.
func StartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if status.State == svc.Running {
		slog.Info("service=already_running")
		return nil
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	slog.Info("service=start_pending")

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if status.State == svc.Running {
			slog.Info("service=started")
			return nil
		}
		if status.State == svc.Stopped {
			return fmt.Errorf("service stopped unexpectedly (win32 exit code %d)", status.Win32ExitCode)
		}
	}
	return fmt.Errorf("timed out waiting for service to start")
}

// StopService sends a stop control to the service and waits up to 30 s for it
// to reach the Stopped state.
func StopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(dc.ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if status.State == svc.Stopped {
		slog.Info("service=already_stopped")
		return nil
	}

	status, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	slog.Info("service=stop_pending")

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if status.State == svc.Stopped {
			slog.Info("service=stopped")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
	}
	// Check one last time after the loop.
	if status.State == svc.Stopped {
		slog.Info("service=stopped")
		return nil
	}
	return fmt.Errorf("timed out waiting for service to stop")
}

// RestartService stops the service (if running) and starts it again.
func RestartService() error {
	if err := StopService(); err != nil {
		return err
	}
	return StartService()
}

// GrantEventLogAccess adds the virtual service account to the Event Log Readers
// local group so the service can subscribe to Security log events.
func GrantEventLogAccess() error {
	account := `NT SERVICE\` + dc.ServiceName
	out, err := exec.Command("net", "localgroup", "Event Log Readers", account, "/add").CombinedOutput()
	if err != nil {
		// Error 1378 = "already a member" — not a real failure.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			slog.Info("eventlog_readers", "account", account, "note", "already a member")
			return nil
		}
		return fmt.Errorf("net localgroup: %w (%s)", err, string(out))
	}
	slog.Info("eventlog_readers", "account", account, "note", "added")
	return nil
}

// ServiceStatus returns the current SCM state of the service as a human-readable string.
func ServiceStatus() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(dc.ServiceName)
	if err != nil {
		return "", fmt.Errorf("open service (not installed?): %w", err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return "", fmt.Errorf("query service: %w", err)
	}
	return svcStateString(status.State), nil
}

func svcStateString(state svc.State) string {
	switch state {
	case svc.Running:
		return "Running"
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.PausePending:
		return "PausePending"
	case svc.Paused:
		return "Paused"
	case svc.ContinuePending:
		return "ContinuePending"
	default:
		return fmt.Sprintf("Unknown(%d)", state)
	}
}
