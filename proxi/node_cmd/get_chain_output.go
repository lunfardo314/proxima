package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
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
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	o, _, _, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)

	glb.Infof("%s", o.String())
}
