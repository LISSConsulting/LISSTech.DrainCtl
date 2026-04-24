//go:build windows

package main

import (
	"strings"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
)

func TestRegisterCmd_FingerprintMismatchRefusesOverwrite(t *testing.T) {
	t.Setenv("ProgramData", t.TempDir())

	cfg := dc.DefaultConfig()
	cfg.Dashboard.URL = "https://dashboard.example.test"
	cfg.Dashboard.TLSFingerprint = "A"
	if err := dc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	orig := registerWithDashboardFunc
	registerWithDashboardFunc = func(string) (*dashboard.RegisterResult, error) {
		return &dashboard.RegisterResult{TLSFingerprint: "B"}, nil
	}
	t.Cleanup(func() { registerWithDashboardFunc = orig })

	cmd := registerCmd()
	cmd.SetArgs([]string{"--pin", "https://dashboard.example.test"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() returned nil, want fingerprint mismatch error")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("err = %q, want substring %q", err.Error(), "fingerprint mismatch")
	}

	got, err := dc.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Dashboard.TLSFingerprint != "A" {
		t.Fatalf("TLSFingerprint = %q, want %q", got.Dashboard.TLSFingerprint, "A")
	}
}
