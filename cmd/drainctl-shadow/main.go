package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
	"golang.org/x/sys/windows"
)

// Version is set by the release build so this binary carries the product version.
var Version = "dev"

type launcher func(name string, args ...string) error

type systemDirectoryResolver func() (string, error)

func main() {
	if Version == "" {
		os.Exit(1)
	}
	if err := run(os.Args, startProcess, windows.GetSystemDirectory); err != nil {
		os.Exit(1)
	}
}

func run(args []string, start launcher, systemDirectory systemDirectoryResolver) error {
	if len(args) != 2 {
		return fmt.Errorf("expected one shadow URI")
	}
	target, err := sessiondata.ParseShadowURI(args[1])
	if err != nil {
		return err
	}
	directory, err := systemDirectory()
	if err != nil {
		return fmt.Errorf("resolve Windows system directory: %w", err)
	}
	return start(filepath.Join(directory, "mstsc.exe"), "/v:"+target.Host, fmt.Sprintf("/shadow:%d", target.SessionID), "/control")
}

func startProcess(name string, args ...string) error {
	command := exec.Command(name, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
