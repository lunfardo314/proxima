package node_cmd

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initDeleteChainCmd() *cobra.Command {
	deleteChainCmd := &cobra.Command{
		Use:   "delchain <chain id>",
		Short: `deletes a chain origin (not a sequencer)`,
		Args:  cobra.ExactArgs(1),
		Run:   runDeleteChainCmd,
	}
	glb.AddFlagTraceTx(deleteChainCmd)
	deleteChainCmd.InitDefaultHelpCmd()

	return deleteChainCmd
}

func DeleteChain(chainId *ledger.ChainID) (*transaction.TxContext, error) {
	walletData := glb.GetWalletData()

	target := glb.MustGetTarget()

	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}
	glb.Infof("trace on node: %v", glb.TraceTx())

	glb.Infof("Deleting chain origin:")
	glb.Infof("   chain id: %s", chainId.String())
	glb.Infof("   tag-along fee %s to the sequencer %s", util.Th(feeAmount), tagAlongSeqID)
	glb.Infof("   source account: %s", walletData.Account.String())
	glb.Infof("   chain controller: %s", target)

	if !glb.YesNoPrompt("proceed?:", true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	return glb.GetClient().DeleteChainOrigin(client.DeleteChainOriginParams{
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		ChainID:          chainId,
	})
}

func runDeleteChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	chainId, err := ledger.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	txCtx, err := DeleteChain(&chainId)

	glb.AssertNoError(err)
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}
}
