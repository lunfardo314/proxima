package api

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
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

func runGetUTXOCmd(_ *cobra.Command, args []string) {
	oid, err := core.OutputIDFromHexString(args[0])
	glb.AssertNoError(err)

	oData, err := getClient().GetOutputDataFromHeaviestState(&oid)
	glb.AssertNoError(err)

	out, err := core.OutputFromBytesReadOnly(oData)
	glb.AssertNoError(err)

	glb.Infof((&core.OutputWithID{
		ID:     oid,
		Output: out,
	}).String())
}
