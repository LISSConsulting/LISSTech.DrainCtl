package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestRunStartsOnlyFixedMSTSCArguments(t *testing.T) {
	const systemDirectory = `C:\Windows\System32`

	var gotName string
	var gotArgs []string
	start := func(name string, args ...string) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}

	err := run(
		[]string{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42"},
		start,
		func() (string, error) { return systemDirectory, nil },
	)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := filepath.Join(systemDirectory, "mstsc.exe"); gotName != want {
		t.Errorf("launched %q, want trusted system executable %q", gotName, want)
	}
	wantArgs := []string{"/v:rdsh-01.example.test", "/shadow:42", "/control"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("arguments = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestRunRejectsSystemDirectoryResolutionFailureWithoutLaunching(t *testing.T) {
	resolveErr := errors.New("system directory unavailable")
	calls := 0
	err := run(
		[]string{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42"},
		func(string, ...string) error {
			calls++
			return nil
		},
		func() (string, error) { return "", resolveErr },
	)
	if !errors.Is(err, resolveErr) {
		t.Errorf("run() error = %v, want resolution error", err)
	}
	if calls != 0 {
		t.Errorf("launcher called %d times after resolution failure", calls)
	}
}

func TestRunRejectsUnsafeInvocationWithoutLaunching(t *testing.T) {
	calls := 0
	start := func(string, ...string) error {
		calls++
		return errors.New("must not be called")
	}
	for _, args := range [][]string{
		{"drainctl-shadow.exe"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42", "extra"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42&session=43"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42%26arg%3D/noConsentPrompt"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=042"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42&arg=/noConsentPrompt"},
		{"drainctl-shadow.exe", "drainctl-shadow://shadow?host=" + strings.Repeat("a", sessiondata.MaxShadowProtocolURILength) + "&session=42"},
	} {
		if err := run(args, start, func() (string, error) { return `C:\Windows\System32`, nil }); err == nil {
			t.Errorf("run(%#v) succeeded", args)
		}
	}
	if calls != 0 {
		t.Errorf("launcher called %d times for invalid invocations", calls)
	}
}
