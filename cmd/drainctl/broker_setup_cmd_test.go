//go:build windows

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
)

func withBrokerSetupRPC(t *testing.T, fn func(string) (*pipe.BrokerSetupResult, error)) {
	t.Helper()
	original := brokerSetupViaPipe
	brokerSetupViaPipe = fn
	t.Cleanup(func() { brokerSetupViaPipe = original })
}

func runBrokerSetup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := brokerSetupCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestBrokerSetupCommandRegistrationAndHelp(t *testing.T) {
	root := newRootCmd()
	command, _, err := root.Find([]string{"broker-setup"})
	if err != nil {
		t.Fatalf("find broker-setup: %v", err)
	}
	if command == root {
		t.Fatal("broker-setup was not registered")
	}

	var help bytes.Buffer
	command.SetOut(&help)
	if err := command.Help(); err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, want := range []string{"--connection-broker", "running DrainCtl service", "RemoteDesktop", "probe runs before configuration is saved", "never creates accounts"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("help missing %q:\n%s", want, help.String())
		}
	}
}

func TestBrokerSetupNormalizesBrokerBeforeRPC(t *testing.T) {
	var received string
	withBrokerSetupRPC(t, func(broker string) (*pipe.BrokerSetupResult, error) {
		received = broker
		return &pipe.BrokerSetupResult{ConnectionBroker: broker, CollectionCount: 2, SessionHostCount: 3, ServiceIdentity: "DOMAIN\\svc-drainctl$"}, nil
	})

	output, err := runBrokerSetup(t, "--connection-broker", "  rdc.example.test  ")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if received != "rdc.example.test" {
		t.Errorf("RPC broker = %q, want normalized broker", received)
	}
	for _, want := range []string{"Connection broker: rdc.example.test", "Collections: 2", "Session hosts: 3", "Service identity: DOMAIN\\svc-drainctl$"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestBrokerSetupBlankBrokerUsesLocalDefault(t *testing.T) {
	var received string
	withBrokerSetupRPC(t, func(broker string) (*pipe.BrokerSetupResult, error) {
		received = broker
		return &pipe.BrokerSetupResult{CollectionCount: 0, SessionHostCount: 0, ServiceIdentity: "NT AUTHORITY\\SYSTEM"}, nil
	})

	output, err := runBrokerSetup(t, "--connection-broker", "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if received != "" {
		t.Errorf("RPC broker = %q, want empty local default", received)
	}
	if !strings.Contains(output, "Connection broker: local host (default)") {
		t.Errorf("output = %q, want local default", output)
	}
}

func TestBrokerSetupRejectsInvalidBrokerBeforeRPC(t *testing.T) {
	called := false
	withBrokerSetupRPC(t, func(string) (*pipe.BrokerSetupResult, error) {
		called = true
		return nil, nil
	})

	_, err := runBrokerSetup(t, "--connection-broker", "invalid_broker.example.test")
	if err == nil || !strings.Contains(err.Error(), "invalid connection broker") {
		t.Fatalf("error = %v, want invalid broker error", err)
	}
	if called {
		t.Fatal("RPC called for invalid broker")
	}
}

func TestBrokerSetupReturnsRPCErrorWithoutFallback(t *testing.T) {
	want := errors.New("service pipe unavailable")
	calls := 0
	withBrokerSetupRPC(t, func(string) (*pipe.BrokerSetupResult, error) {
		calls++
		return nil, want
	})

	_, err := runBrokerSetup(t, "--connection-broker", "rdc.example.test")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Errorf("RPC calls = %d, want 1; command must not use a direct fallback", calls)
	}
}
