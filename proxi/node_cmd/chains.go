package node_cmd

import (
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initChainsCmd() *cobra.Command {
	chainsCmd := &cobra.Command{
		Use:   "chains",
		Short: `lists chains controlled by the account`,
		Args:  cobra.NoArgs,
		Run:   runChainsCmd,
	}
	chainsCmd.InitDefaultHelpCmd()
	return chainsCmd
}

func runChainsCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	wallet := glb.GetWalletData()

	outs, err := glb.GetClient().GetAccountOutputs(wallet.Account, func(_ *ledger.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx != 0xff
	})
	glb.AssertNoError(err)

	if len(outs) == 0 {
		glb.Infof("no chains have been found controlled by %s", wallet.Account.String())
		os.Exit(0)
	}

	glb.Infof("list of chains controlled by %s", wallet.Account.String())
	for _, o := range outs {
		chainID, _, ok := o.ExtractChainID()
		glb.Assertf(ok, "can't extract chainID")
		glb.Infof("   %s with balance %s on %s", chainID.String(), util.GoTh(o.Output.Amount()), o.IDShort())
	}
}
