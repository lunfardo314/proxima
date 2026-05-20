package delegate

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TODO implement random delegation target option

func initDelegationSubmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chain <chain ID> [flags]",
		Short: `delegates existing chain to the target sequencer`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegationSubmitCmd,
	}

	glb.AddFlagTarget(cmd)

	cmd.PersistentFlags().StringVarP(&targetChainIDStr, "delegation_target", "q", "", "target sequencer id")
	err := viper.BindPFlag("delegation_target", cmd.PersistentFlags().Lookup("delegation_target"))
	glb.AssertNoError(err)

	// 0 means use the ledger constant constDelegationMaxFrozenEpochs (default maximum)
	cmd.PersistentFlags().Uint8VarP(&maxFreezeEpochs, "epochs", "e", 0, "max frozen epochs allowed by the delegator (0 = maximum)")
	err = viper.BindPFlag("epochs", cmd.PersistentFlags().Lookup("epochs"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint16Var(&requiredShare, "share", 900, "required inflation share in promille (0-1000)")
	err = viper.BindPFlag("share", cmd.PersistentFlags().Lookup("share"))
	glb.AssertNoError(err)

	cmd.InitDefaultHelpCmd()
	return cmd
}

func runDelegationSubmitCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	if targetChainIDStr == "" {
		glb.Infof("selecting optimal/random target sequencer..")
		targetSeqID, err = chooseRandomSequencerForDelegation()
		glb.AssertNoError(err)
	} else {
		targetSeqID, err = base.ChainIDFromHexString(targetChainIDStr)
		glb.Assertf(err == nil, "failed parsing target chainID: %v", err)
	}

	glb.Assertf(requiredShare <= 1000, "required inflation share must be 0-1000 promille")

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(feeAmount))

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	client := glb.GetClient()
	oIn, _, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)

	ti, err := client.GetSequencerTargetInfo(targetSeqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", targetSeqID.StringShort(), err)

	est := estimateDelegation(ti, oIn.Output.TokenBalance(), maxFreezeEpochs, requiredShare, targetSeqID, ts.Slot)
	effShare := confirmDelegationEstimate(est, oIn.Output.TokenBalance(), requiredShare, targetSeqID)

	dOut, isDelegation := ledger.AsDelegationOutput(oIn.Output, oIn.ID)
	glb.Assertf(!isDelegation || dOut.IsUnlockableByMaster(ts.Slot), "chain is delegation output NOT unlockable by master")

	// Inflation calc still goes through the ledger singleton (the wallet
	// path keeps InitLedgerFromNode for now per the refactor plan).
	inflation := ledger.L(base.MaxSlot).ChainInflationOneSlot(oIn.Output.TokenBalance(), oIn.ID.Slot())

	// Phase 5 of delegation_epoch_params: source per-target epochSlots /
	// maxFrozenEpochs from the target sequencer's own delegationParams.
	epochSlots := ti.EpochDurationSlots
	targetMaxFrozenEpochs := byte(ti.MaxFrozenEpochs)

	// Wasm-style build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderID(walletData.Account)
	txb := txbuildercore.New(0)

	// Consume the predecessor chain output as input 0.
	predBytes := oIn.Output.Bytes()
	txb.ConsumeOutput(predBytes, oIn.ID)
	consumedBytes := [][]byte{predBytes}

	// Master-unlock byte (0xff) satisfies the delegation lock's master path;
	// chain unlock params point at successor output index 0.
	txb.PutSignatureUnlock(0, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))

	// Compose the new delegation chain transition output.
	newAmount := oIn.Output.TokenBalance() + inflation - feeAmount
	delegateLockBin, err := lib.NewDelegateLockBytecode(maxFreezeEpochs, effShare, epochSlots, targetMaxFrozenEpochs)
	glb.AssertNoError(err)
	chainTransitionBin, err := lib.NewChainTransition(
		chainID,
		0, // predInputIndex
		oIn.OriginSlot,
		oIn.CumulativeChainInflation+inflation,
		oIn.CumulativeBranchBonus,
		oIn.TransitionCounter+1,
		oIn.BranchCounter,
	)
	glb.AssertNoError(err)
	stateBin, err := lib.NewDelegateLockState(0, 0)
	glb.AssertNoError(err)

	ob := txbuildercore.NewOutputBuilder()
	ob.PutConstraint(txbuildercore.EncodeAmounts(newAmount, inflation), txbuildercore.ConstraintIndexAmounts)
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{walletHolderID[:], targetSeqID[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(delegateLockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainTransitionBin, txbuildercore.ConstraintIndexChain)
	ob.MustPushConstraint(stateBin)
	succIdx := txb.ProduceOutput(ob.Output().Bytes())
	glb.Assertf(succIdx == 0, "succIdx==0")

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	tagAlongIdx := txb.ProduceOutput(tagAlongOut.Bytes())
	glb.Assertf(tagAlongIdx == 1, "tagAlongIdx==1")

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	prompt := fmt.Sprintf("delegate %s to sequencer %s (share %d promille)?", chainID.StringShort(), targetSeqID.String(), effShare)
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	glb.TrackTxInclusion(txid, 2*time.Second)
}
