package node_cmd

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initGetOutputsCmd() *cobra.Command {
	getOutputsCmd := &cobra.Command{
		Use:     "utxo",
		Aliases: []string{"outputs", "utxo"},
		Short:   `returns all UTXOs (outputs) locked in the accountable from the heaviest state of the latest epoch`,
		Args:    cobra.NoArgs,
		Run:     runGetOutputsCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	return getOutputsCmd
}

func runGetOutputsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()

	accountable := glb.MustGetTarget()

	res, err := glb.GetClient().GetOutputs(accountable.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		MaxOutputs: 100,
	})
	glb.AssertNoError(err)

	if len(res.Outputs) == 0 {
		glb.Infof("no outputs found")
		return
	}
	if res.LimitExceeded {
		glb.Infof("WARNING: server-side iteration cap hit; results are partial")
	}
	glb.PrintLRB(&res.LRBID)

	for i, o := range res.Outputs {
		glb.Infof("\n-- output %d --", i)
		glb.Infof("   id %s, hex = %s", o.ID.String(), o.ID.StringHex())
		glb.Infof("   amount: %s, lock name: '%s'", util.Th(o.Output.TokenBalance()), o.Output.Lock().Name())
		if chainID, ok := o.ExtractChainID(); ok {
			glb.Verbosef("   chain id: %s", chainID.StringHex())
		}
		glb.Verbosef("   raw data: %s (%d bytes) ", o.Output.Hex(), len(o.Output.Bytes()))
		if glb.IsVerbose() {
			glb.Infof("   parsed constraints:")
			for _, constraint := range o.Output.LinesPlainSource().Slice() {
				glb.Infof("        - %s", constraint)
			}
		}
	}
}
