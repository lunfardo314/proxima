package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initGetChainOutputCmd() *cobra.Command {
	getUTXOCmd := &cobra.Command{
		Use:   "get_chain_output <chain id hex-encoded>",
		Short: `returns chain output by chain id`,
		Args:  cobra.ExactArgs(1),
		Run:   runGetChainOutputCmd,
	}
	getUTXOCmd.InitDefaultHelpCmd()
	return getUTXOCmd
}

func runGetChainOutputCmd(_ *cobra.Command, args []string) {
	lib := glb.GetTxLibrary()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	o, _, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)

	// OutputWithID.String() uses singleton-bound LinesHR. Build the same
	// shape (id + per-slot decompiled source) via the wallet library.
	glb.Infof("output id: %s", o.ID.String())
	glb.Infof("token balance: %s", util.Th(o.Output.TokenBalance()))
	glb.Infof("constraints:")
	for j, raw := range o.Output.ConstraintsRawBytes() {
		if len(raw) == 0 {
			continue
		}
		src, err := lib.DecompileBytecode(raw)
		if err != nil {
			glb.Infof("    [%d] <decompile error: %v>", j, err)
		} else {
			glb.Infof("    [%d] %s", j, src)
		}
	}
}
