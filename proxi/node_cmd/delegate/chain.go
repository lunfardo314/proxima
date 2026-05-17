package delegate

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
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

	inflation := ledger.L(base.MaxSlot).ChainInflationOneSlot(oIn.Output.TokenBalance(), oIn.ID.Slot())

	// Phase 5 of delegation_epoch_params: source per-target epochSlots /
	// maxFrozenEpochs from the target sequencer's own delegationParams,
	// surfaced via SequencerTargetInfo (see api/server populating ti
	// from the chain output's index-6 constraint in Phase 3).
	epochSlots := ti.EpochDurationSlots
	targetMaxFrozenEpochs := byte(ti.MaxFrozenEpochs)
	oOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(oIn.Output.TokenBalance()+inflation-feeAmount), int64(inflation))
		lock := ledger.NewDelegateLock(targetSeqID, base.HolderID(walletData.Account), targetMaxFrozenEpochs, effShare, epochSlots, targetMaxFrozenEpochs)
		o.WithLock(lock)
		cc := ledger.NewChainConstraint(chainID, 0, oIn.OriginSlot, oIn.CumulativeChainInflation+inflation, oIn.CumulativeBranchBonus, oIn.TransitionCounter+1, oIn.BranchCounter)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})
	glb.AssertNoError(oOut.EnoughAmountForStorageDeposit())

	oOut = ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(oIn.Output.TokenBalance()+inflation-feeAmount), int64(inflation))
		lock := ledger.NewDelegateLock(targetSeqID, base.HolderID(walletData.Account), maxFreezeEpochs, effShare, epochSlots, targetMaxFrozenEpochs)
		o.WithLock(lock)
		cc := ledger.NewChainConstraint(chainID, 0, oIn.OriginSlot, oIn.CumulativeChainInflation+inflation, oIn.CumulativeBranchBonus, oIn.TransitionCounter+1, oIn.BranchCounter)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(oIn.Output, oIn.ID)
	glb.AssertNoError(err)
	glb.Assertf(predIdx == 0, "predIdx==0")
	txb.PutSignatureUnlock(0, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	succIdx, err := txb.ProduceOutput(oOut)
	glb.AssertNoError(err)
	glb.Assertf(succIdx == 0, "succIdx==0")

	taOut := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, base.HolderID(walletData.Account))
	tagAlongIdx, err := txb.ProduceOutput(taOut)
	glb.AssertNoError(err)
	glb.Assertf(tagAlongIdx == 1, "tagAlongIdx==1")

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(oIn.Output)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, txid, txString, err := txb.BytesWithValidation()
	if err != nil {
		glb.Infof("\nFAILED to produce transaction: '%v'\n-------------------\n%s", err, txString)
		return
	}
	glb.Verbosef("\n-------- tx OK (len = %d) -----------\n%s", len(txBytes), txString)

	prompt := fmt.Sprintf("delegate %s to sequencer %s (share %d promille)?", chainID.StringShort(), targetSeqID.String(), effShare)
	if !glb.YesNoPrompt(prompt, true) {
		return
	}
	err = client.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	glb.TrackTxInclusion(txid, 2*time.Second)
}
