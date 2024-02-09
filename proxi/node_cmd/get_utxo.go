package node_cmd

import (
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initGetUTXOCmd() *cobra.Command {
	getUTXOCmd := &cobra.Command{
		Use:   "get_utxo <output ID hex-encoded>",
		Short: `returns output by output ID`,
		Args:  cobra.ExactArgs(1),
		Run:   runGetUTXOCmd,
	}
	getUTXOCmd.InitDefaultHelpCmd()
	return getUTXOCmd
}

func runGetUTXOCmd(_ *cobra.Command, args []string) {
	oid, err := ledger.OutputIDFromHexString(args[0])
	glb.AssertNoError(err)

	oData, err := getClient().GetOutputDataFromHeaviestState(&oid)
	glb.AssertNoError(err)
	if len(oData) > 0 {
		out, err := ledger.OutputFromBytesReadOnly(oData)
		glb.AssertNoError(err)

		glb.Infof((&ledger.OutputWithID{
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

func displayInclusionState(inclusion []api.InclusionData, inSec ...float64) {
	scoreAll, scorePercTotal, scorePercDominating := glb.InclusionScore(inclusion, genesis.DefaultInitialSupply)
	inSecStr := ""
	if len(inSec) > 0 {
		inSecStr = fmt.Sprintf(" in %.1f sec", inSec[0])
	}
	percDominatingStr := "??"
	if scorePercDominating >= 0 {
		percDominatingStr = fmt.Sprintf("%d%%", scorePercDominating)
	}
	glb.Infof("Inclusion score%s: num branches: %d, %d%% of all, %s of dominating", inSecStr, scoreAll, scorePercTotal, percDominatingStr)
	yn := ""
	for i := range inclusion {
		if inclusion[i].Included {
			yn = "YES"
		} else {
			yn = " NO"
		}
		glb.Verbosef("   %s   %s    %s", yn, inclusion[i].BranchID.StringShort(), util.GoTh(inclusion[i].Coverage))
	}
}
