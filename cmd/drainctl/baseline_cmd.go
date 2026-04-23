//go:build windows

package main

import (
	"fmt"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/spf13/cobra"
)

// baselineCmd hosts subcommands that manage the evtspike anomaly-detector
// baseline. The baseline is a per-channel Gamma-Poisson posterior stored in
// %ProgramData%\LISS Technologies\LISSTech DrainCtl\baseline.json and
// hydrated at service start. Deleting the file by hand doesn't reset the
// running detector — the service rewrites it from in-memory state on the
// next 15-minute persistence tick. Use the subcommands here to route the
// reset through the live service so both halves stay in sync.
func baselineCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "baseline",
		Short: "Manage the evtspike anomaly-detector baseline",
	}
	c.AddCommand(baselineResetCmd())
	return c
}

func baselineResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Wipe the evtspike baseline (in-memory detectors + baseline.json)",
		Long: `Reset the evtspike baseline on the local host.

This instructs the running drainctl service to clear the in-memory per-channel
detector state for every subscribed channel and delete baseline.json. Scoring
starts afresh from the prior on the next 10-second tick. Subscriptions and the
scoring loop keep running — the subsystem is not restarted.

The service must be running. Deleting baseline.json directly while the service
runs has no durable effect: the file is rewritten from memory on the next
persistence tick.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pipe.BaselineResetViaPipe(); err != nil {
				return fmt.Errorf("baseline reset failed: %w", err)
			}
			fmt.Println("evtspike baseline reset")
			return nil
		},
	}
}
