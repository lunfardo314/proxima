package api

import (
	"github.com/lunfardo314/proxima/proxi_old/glb"
	"github.com/spf13/cobra"
)

func initBalanceCmd(apiCmd *cobra.Command) {
	getBalanceCmd := &cobra.Command{
		Use:     "balance",
		Aliases: []string{"bal"},
		Short:   `displays account totals`,
		Args:    cobra.NoArgs,
		Run:     runBalanceCmd,
	}

	getBalanceCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getBalanceCmd)
}

func runBalanceCmd(_ *cobra.Command, _ []string) {
	accountable := glb.MustGetTarget()

	outs, err := getClient().GetAccountOutputs(accountable)
	glb.AssertNoError(err)
	glb.Infof("TOTALS:")
	displayTotals(outs)
}
