package api

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
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
	if len(oData) > 0 {
		out, err := core.OutputFromBytesReadOnly(oData)
		glb.AssertNoError(err)

		glb.Infof((&core.OutputWithID{
			ID:     oid,
			Output: out,
		}).String())
	}
	glb.Assertf(glb.IsVerbose(), "output not found in the heaviest state. Use '--verbose, -v' to retrieve inclusions state")

	glb.Infof("Inclusion state:")
	inclusion, err := getClient().GetOutputInclusion(&oid)
	glb.AssertNoError(err)

	displayInclusionState(inclusion)
}

func displayInclusionState(inclusion []api.InclusionData) {
	yn := ""
	for i := range inclusion {
		if inclusion[i].Included {
			yn = "YES"
		} else {
			yn = " NO"
		}
		glb.Infof("%s   %s    %s", yn, inclusion[i].BranchID.Short(), util.GoThousands(inclusion[i].Coverage))
	}
}
