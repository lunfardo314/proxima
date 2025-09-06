package node_cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initKillChainCmd() *cobra.Command {
	deleteChainCmd := &cobra.Command{
		Use:     "killchain <chain id>",
		Aliases: []string{"endchain, delchain"},
		Short:   `ends a chain by destroying chain output. All tokens are converted into the addressED25519-locked output with the same controlling private key`,
		Args:    cobra.ExactArgs(1),
		Run:     runKillChainCmd,
	}
	deleteChainCmd.InitDefaultHelpCmd()

	return deleteChainCmd
}

func runKillChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	walletData := glb.GetWalletData()

	var tagAlongSeqID base.ChainID
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee must be > 0")
	clnt := glb.GetClient()

	pTagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(pTagAlongSeqID != nil, "tag-along sequencer not specified")
	tagAlongSeqID = *pTagAlongSeqID

	md, err := clnt.GetSequencerData(tagAlongSeqID)
	glb.AssertNoError(err)

	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}
	glb.Assertf(feeAmount > 0, "tag-along fee must be > 0")

	prompt := fmt.Sprintf("discontinue chain %s?", chainID.String())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	out, _, _, err := clnt.GetChainOutput(chainID)
	glb.AssertNoError(err)

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	dOut, isDelegation := ledger.AsDelegationOutput(out.Output, out.ID)

	var tx *transaction.Transaction
	if !isDelegation {
		tx, err = txbuilder.MakeEndChainTransaction(txbuilder.EndChainParams{
			Timestamp:     ts,
			ChainIn:       out,
			PrivateKey:    walletData.PrivateKey,
			TagAlongSeqID: *pTagAlongSeqID,
			TagAlongFee:   feeAmount,
		})
	} else {
		dOut.UnfreezeSlot()
		IsInSafeRevocationWindow(uint32(ts.Slot))

		dconst := ledger.DelegationConst()
		dconst.LastSlotInEpochDirect(dOut.Target.ChainID())

	}

	tx, err := txbuilder.MakeEndChainTransaction(txbuilder.EndChainParams{
		Timestamp:     nowis,
		ChainIn:       out,
		PrivateKey:    walletData.PrivateKey,
		TagAlongSeqID: *pTagAlongSeqID,
		TagAlongFee:   feeAmount,
	})

}
