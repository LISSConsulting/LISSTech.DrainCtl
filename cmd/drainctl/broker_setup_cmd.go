//go:build windows

package main

import (
	"fmt"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/pipe"
	"github.com/spf13/cobra"
)

// brokerSetupViaPipe is replaceable by command tests. The command deliberately
// has no direct PowerShell fallback: discovery must run as the service identity.
var brokerSetupViaPipe = pipe.BrokerSetupViaPipe

func brokerSetupCmd() *cobra.Command {
	var requestedBroker string

	cmd := &cobra.Command{
		Use:   "broker-setup",
		Short: "Probe and save RD Connection Broker discovery",
		Long: `Probe RD Session Collection discovery as the running DrainCtl service, then save the Connection Broker only if the probe succeeds.

The command requires an elevated caller and a running DrainCtl service. Discovery imports the RemoteDesktop PowerShell module and queries Get-RDSessionCollection and Get-RDSessionHost under the actual service identity. That identity needs the module and appropriate administrative/RDS-management rights on the selected Connection Broker.

Use --connection-broker for a remote broker hostname or FQDN. Leave it blank to probe the local host. The probe runs before configuration is saved, so a failed probe never changes the configured broker. This command never creates accounts, changes permissions, installs Windows features, or changes the service identity.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			broker, err := dc.NormalizeRDConnectionBroker(requestedBroker)
			if err != nil {
				return fmt.Errorf("invalid connection broker: %w", err)
			}

			result, err := brokerSetupViaPipe(broker)
			if err != nil {
				return fmt.Errorf("broker setup failed: %w", err)
			}

			effectiveBroker := result.ConnectionBroker
			if effectiveBroker == "" {
				effectiveBroker = "local host (default)"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connection broker: %s\nCollections: %d\nSession hosts: %d\nService identity: %s\n", effectiveBroker, result.CollectionCount, result.SessionHostCount, result.ServiceIdentity)
			return err
		},
	}
	cmd.Flags().StringVar(&requestedBroker, "connection-broker", "", "RD Connection Broker hostname or FQDN (blank uses local host)")
	return cmd
}
