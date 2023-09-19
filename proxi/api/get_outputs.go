package api

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/spf13/cobra"
)

func initGetOutputsCmd(apiCmd *cobra.Command) {
	getOutputsCmd := &cobra.Command{
		Use:   "get_outputs <accountable in the EasyF source>",
		Short: `returns all outputs locked in the accountable from the heaviest state of the latest epoch`,
		Args:  cobra.ExactArgs(1),
		Run:   runGetOutputsCmd,
	}
	getOutputsCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getOutputsCmd)

}

func runGetOutputsCmd(cmd *cobra.Command, args []string) {
	accountable, err := core.AccountableFromSource(args[0])
	console.AssertNoError(err)

	oData, err := getClient().GetAccountOutputs(accountable)
	console.AssertNoError(err)

	outs, err := txutils.ParseAndSortOutputData(oData, nil)
	console.AssertNoError(err)

	console.Infof("------- %d outputs locked in the account %s", len(outs), accountable.String())
	for _, o := range outs {
		console.Infof(o.String())
	}
}
