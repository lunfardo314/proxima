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
	glb.InitLedgerFromNode()
	accountable := glb.MustGetTarget()

	outs, lrbid, err := glb.GetClient().GetAccountOutputs(accountable)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)
	glb.Infof("TOTALS:")
	displayTotals(outs)
}
