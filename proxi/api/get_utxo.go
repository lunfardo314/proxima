package api

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
)

func initGetUTXOCmd(apiCmd *cobra.Command) {
	getUTXOCmd := &cobra.Command{
		Use:   "get_utxo <output ID hex-encoded>",
		Short: `returns output by output ID`,
		Args:  cobra.ExactArgs(1),
		Run:   runGetUTXOCmd,
	}
	getUTXOCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getUTXOCmd)

}

func runGetUTXOCmd(cmd *cobra.Command, args []string) {
	oid, err := core.OutputIDFromHexString(args[0])
	console.AssertNoError(err)

	oData, err := getClient().GetOutputData(&oid)
	console.AssertNoError(err)

	out, err := core.OutputFromBytesReadOnly(oData)
	console.AssertNoError(err)

	console.Infof((&core.OutputWithID{
		ID:     oid,
		Output: out,
	}).String())
}
