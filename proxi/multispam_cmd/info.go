package multispam_cmd

import (
	"fmt"

	"github.com/lunfardo314/proxima/multispam"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "display sender account balances",
		Args:  cobra.NoArgs,
		Run:   runInfoCmd,
	}
}

func runInfoCmd(_ *cobra.Command, _ []string) {
	configFile := viper.GetString("multispam-config")
	cfg, err := multispam.LoadConfig(configFile)
	glb.AssertNoError(err)

	// Use first API host from multispam config so wallet config is not required
	firstHost := cfg.APIHosts[0]
	viper.Set("api.endpoint", firstHost.URL)
	if firstHost.Timeout > 0 {
		viper.Set("api.timeout_sec", int(firstHost.Timeout.Seconds()))
	}

	glb.InitLedgerFromNode()

	clnt := glb.GetClient()

	fmt.Printf("%-12s %-20s %8s %15s\n", "Name", "Holder ID", "Outputs", "Balance")
	fmt.Printf("%-12s %-20s %8s %15s\n", "----", "---------", "-------", "-------")

	var totalBalance uint64
	for _, s := range cfg.Senders {
		addr, err := multispam.SenderAddress(s.KeyFile)
		if err != nil {
			fmt.Printf("%-12s error: %v\n", s.Name, err)
			continue
		}
		holderID, _ := multispam.SenderHolderID(s.KeyFile)

		outs, _, balance, err := clnt.GetTransferableOutputs(addr, 256)
		if err != nil {
			fmt.Printf("%-12s %-20s error: %v\n", s.Name, holderID[:16]+"...", err)
			continue
		}

		fmt.Printf("%-12s %-20s %8d %15d\n", s.Name, holderID[:16]+"...", len(outs), balance)
		totalBalance += balance
	}
	fmt.Printf("%-12s %-20s %8s %15d\n", "TOTAL", "", "", totalBalance)
}
