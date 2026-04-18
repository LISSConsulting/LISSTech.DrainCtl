//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/svc"
)

func main() {
	if err := svc.RunService(); err != nil {
		fmt.Fprintf(os.Stderr, "drainctld: %v\n", err)
		os.Exit(1)
	}
}
