package node_cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
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

	out, _, err := clnt.GetChainOutput(chainID)
	glb.AssertNoError(err)

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	dOut, isDelegation := ledger.AsDelegationOutput(out.Output, out.ID)

	if isDelegation && dOut.IsInFrozenSlot(ts.Slot) {
		unfreeze := dOut.UnfreezeSlot()
		glb.Infof("in the current slot %d the delegation output cannot be unlocked by the master lock because it is frozen until slot %d",
			ts.Slot, unfreeze)
		glb.Infof("safe revocation window is %d slots from now: slots %d - %d",
			unfreeze-uint32(ts.Slot), ts.Slot, unfreeze)
		glb.Infof("===============\n%s", dOut.LinesHRFull("     ").String())
		return
	}
	tx, err := txbuilder.MakeEndChainTransaction(txbuilder.EndChainParams{
		Timestamp:     ts,
		ChainIn:       out,
		PrivateKey:    walletData.PrivateKey,
		TagAlongSeqID: *pTagAlongSeqID,
		TagAlongFee:   feeAmount,
	})
	glb.AssertNoError(err)

	glb.Infof("submitting transaction %s", tx.IDString())
	err = glb.GetClient().SubmitTransaction(tx.Bytes())
	glb.AssertNoError(err)

	if !glb.NoWait() {
		glb.TrackTxInclusion(tx.ID(), time.Second)
	}
}
