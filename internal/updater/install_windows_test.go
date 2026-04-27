//go:build windows

package updater

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
)

func TestSpawnInstall_ConstructsExpectedCommand(t *testing.T) {
	var captured *exec.Cmd
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, arg ...string) *exec.Cmd {
		captured = exec.Command("cmd.exe", "/c", "exit 0") // benign no-op
		// Record the args we would have sent to msiexec on captured.Args
		// so the assertion can read them.
		captured.Args = append([]string{name}, arg...)
		return captured
	}

	if err := spawnInstall(`C:\Temp\fake.msi`); err != nil {
		t.Fatalf("spawnInstall: %v", err)
	}
	if captured == nil {
		t.Fatal("execCommand was not invoked")
	}
	want := []string{"msiexec", "/i", `C:\Temp\fake.msi`, "/quiet", "/norestart"}
	if len(captured.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", captured.Args, want)
	}
	for i, w := range want {
		if captured.Args[i] != w {
			t.Errorf("Args[%d] = %q, want %q", i, captured.Args[i], w)
		}
	}
	// SysProcAttr.CreationFlags MUST include both DETACHED_PROCESS (0x8)
	// and CREATE_NEW_PROCESS_GROUP — without those, msiexec dies when our
	// service exits.
	if captured.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	flags := captured.SysProcAttr.CreationFlags
	if flags&detachedProcess == 0 {
		t.Errorf("CreationFlags=0x%x missing DETACHED_PROCESS (0x%x)", flags, detachedProcess)
	}
	if flags&createNewProcessGroup == 0 {
		t.Errorf("CreationFlags=0x%x missing CREATE_NEW_PROCESS_GROUP (0x%x)", flags, createNewProcessGroup)
	}
}

func TestSpawnInstall_ReturnsErrorOnStartFailure(t *testing.T) {
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, arg ...string) *exec.Cmd {
		// Path that definitely doesn't exist, so cmd.Start() fails.
		return exec.Command(`Z:\nonexistent\msiexec-impostor.exe`, arg...)
	}
	if err := spawnInstall(`C:\Temp\fake.msi`); err == nil {
		t.Fatal("spawnInstall on bad executable returned nil, want error")
	}
}

func TestTriggerSelfShutdown_CallsCancel(t *testing.T) {
	var called atomic.Bool
	cancel := func() { called.Store(true) }
	triggerSelfShutdown(cancel)
	if !called.Load() {
		t.Error("triggerSelfShutdown did not call cancel")
	}
}

func TestTriggerSelfShutdown_NilCancelIsNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("triggerSelfShutdown(nil) panicked: %v", r)
		}
	}()
	triggerSelfShutdown(nil)
}

func TestTriggerSelfShutdown_ContextCancellationIsObservable(t *testing.T) {
	// Confirms the contract the caller relies on: the context cancel
	// fires immediately, and a downstream selectable observes Done().
	ctx, cancel := context.WithCancel(context.Background())
	triggerSelfShutdown(cancel)
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("ctx.Done() did not fire after triggerSelfShutdown")
	}
}
