package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initBalanceCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:     "balance",
		Aliases: []string{"bal"},
		Short:   `displays account totals`,
		Args:    cobra.NoArgs,
		Run:     runBalanceCmd,
	}

	getBalanceCmd.InitDefaultHelpCmd()
	return getBalanceCmd
}

func runBalanceCmd(_ *cobra.Command, _ []string) {
	accountable := glb.MustGetTarget()

	outs, err := getClient().GetAccountOutputs(accountable)
	glb.AssertNoError(err)
	glb.Infof("TOTALS:")
	displayTotals(outs)
}
