//go:build windows

package main

/*
#include <stdint.h>
*/
import "C"

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"golang.org/x/sys/windows"
)

const (
	errorSuccess        = 0
	errorInstallFailure = 1603
	errorMoreData       = 234
	installMessageError = 0x01000000
	installMessageInfo  = 0x04000000

	customActionDataProperty = "CustomActionData"
)

var (
	modMsi                 = syscall.NewLazyDLL("msi.dll")
	procMsiCreateRecord    = modMsi.NewProc("MsiCreateRecord")
	procMsiRecordSetString = modMsi.NewProc("MsiRecordSetStringW")
	procMsiProcessMessage  = modMsi.NewProc("MsiProcessMessage")
	procMsiGetProperty     = modMsi.NewProc("MsiGetPropertyW")
	procMsiCloseHandle     = modMsi.NewProc("MsiCloseHandle")
)

//export ApplyInstallerConfig
func ApplyInstallerConfig(handle C.uintptr_t) C.uint {
	h := uintptr(handle)
	payload, err := msiGetProperty(h, customActionDataProperty)
	if err != nil {
		msiError(h, fmt.Sprintf("failed to read CustomActionData: %v", err))
		return errorInstallFailure
	}

	input, err := parseInstallerPayload(payload)
	if err != nil {
		msiError(h, fmt.Sprintf("invalid installer payload: %v", err))
		return errorInstallFailure
	}

	cfg, err := dc.LoadConfig()
	if err != nil {
		msiError(h, fmt.Sprintf("load config: %v", err))
		return errorInstallFailure
	}

	dc.ApplyInstallerConfig(cfg, input)
	if err := dc.SaveConfig(cfg); err != nil {
		msiError(h, fmt.Sprintf("save config: %v", err))
		return errorInstallFailure
	}

	msiInfo(h, "installer configuration applied")
	return errorSuccess
}

func parseInstallerPayload(payload string) (dc.InstallerConfigInput, error) {
	vals := map[string]string{}
	for _, line := range strings.Split(payload, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return dc.InstallerConfigInput{}, fmt.Errorf("malformed line %q", line)
		}
		vals[parts[0]] = parts[1]
	}

	mode := vals["m"]
	if mode == "" {
		mode = "standalone"
	}

	input := dc.InstallerConfigInput{Mode: mode}
	if v, ok := vals["u"]; ok {
		input.DashboardURL = &v
	}
	if v, ok, err := parseIntValue(vals, "P"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.DashboardPort = &v
	}
	if v, ok := vals["G"]; ok {
		input.DashboardGroup = &v
	}
	if v, ok, err := parseIntValue(vals, "g"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.GracePeriod = &v
	}
	if v, ok, err := parseIntValue(vals, "p"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.PollInterval = &v
	}
	if v, ok, err := parseIntValue(vals, "s"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.SessionThreshold = &v
	}
	if v, ok, err := parseBoolValue(vals, "e"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.PerfEnabled = &v
	}
	if v, ok, err := parseBoolValue(vals, "x"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.PerfDisabled = &v
	}
	if v, ok, err := parseBoolValue(vals, "r"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.PerfRFX = &v
	}
	if v, ok := vals["f"]; ok {
		input.LogFileLevel = &v
	}
	if v, ok := vals["v"]; ok {
		input.LogEventLevel = &v
	}
	if v, ok, err := parseBoolValue(vals, "d"); err != nil {
		return dc.InstallerConfigInput{}, err
	} else if ok {
		input.DashboardOnly = &v
	}

	return input, nil
}

func parseIntValue(vals map[string]string, key string) (int, bool, error) {
	raw, ok := vals[key]
	if !ok || raw == "" {
		return 0, false, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %w", key, err)
	}
	return v, true, nil
}

func parseBoolValue(vals map[string]string, key string) (bool, bool, error) {
	raw, ok := vals[key]
	if !ok || raw == "" {
		return false, false, nil
	}
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "1", "true", "yes":
		return true, true, nil
	case "0", "false", "no":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("%s: invalid bool %q", key, raw)
	}
}

func msiGetProperty(handle uintptr, name string) (string, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	sz := uint32(256)
	for {
		buf := make([]uint16, sz)
		need := sz
		r1, _, _ := procMsiGetProperty.Call(handle, uintptr(unsafe.Pointer(namePtr)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&need)))
		switch r1 {
		case errorSuccess:
			return windows.UTF16ToString(buf[:need]), nil
		case errorMoreData:
			sz = need + 1
			continue
		default:
			return "", fmt.Errorf("MsiGetPropertyW(%s): %d", name, r1)
		}
	}
}

func msiInfo(handle uintptr, text string) {
	msiMessage(handle, installMessageInfo, text)
}

func msiError(handle uintptr, text string) {
	msiMessage(handle, installMessageError, text)
}

func msiMessage(handle uintptr, kind uintptr, text string) {
	rec, _, _ := procMsiCreateRecord.Call(1)
	if rec == 0 {
		return
	}
	defer func() { _, _, _ = procMsiCloseHandle.Call(rec) }()
	msg, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMsiRecordSetString.Call(rec, 1, uintptr(unsafe.Pointer(msg)))
	_, _, _ = procMsiProcessMessage.Call(handle, kind, rec)
}

func main() {}
