//go:build windows

package drainctl

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

func installServiceImpl(exePath string, log LogFunc) error {
	// Register event log source.
	err := eventlog.InstallAsEventCreate(ServiceName,
		eventlog.Info|eventlog.Warning|eventlog.Error)
	if err != nil {
		// Ignore "already exists" — not a real error.
		log(LvlINF, fmt.Sprintf("eventlog_source=%s (may already exist)", ServiceName))
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.CreateService(ServiceName, exePath, mgr.Config{
		DisplayName:  ServiceDisplayName,
		Description:  ServiceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
	}, "service", "run") // args passed to binary: drainctl.exe service run
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Set recovery: restart after 5 seconds on first and second failure.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * 1e9}, // 5 seconds in 100ns units
		{Type: mgr.ServiceRestart, Delay: 5 * 1e9},
		{Type: mgr.NoAction, Delay: 0},
	}, 86400) // reset failure count after 24 hours

	// Write default parameters.
	if err := WriteDefaultParameters(log); err != nil {
		log(LvlWRN, fmt.Sprintf("write_defaults_failed=%q", err))
	}

	log(LvlOK, "service=installed", fmt.Sprintf("name=%s", ServiceName))
	return nil
}

func uninstallServiceImpl(log LogFunc) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	_ = eventlog.Remove(ServiceName)
	log(LvlOK, "service=uninstalled", fmt.Sprintf("name=%s", ServiceName))
	return nil
}
