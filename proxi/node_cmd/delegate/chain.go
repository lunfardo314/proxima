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

	// Clamped to the target chain's own maximum; 0 means "use target's".
	cmd.PersistentFlags().Uint8VarP(&maxFreezeEpochs, "epochs", "e", defaultMaxFrozenEpochs, "max frozen epochs allowed by the delegator (capped at target's maximum)")
	err = viper.BindPFlag("epochs", cmd.PersistentFlags().Lookup("epochs"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint16Var(&requiredCut, "cut", 900, "required inflation cut in promille (0-1000)")
	err = viper.BindPFlag("cut", cmd.PersistentFlags().Lookup("cut"))
	glb.AssertNoError(err)

	cmd.InitDefaultHelpCmd()
	return cmd
}

func runDelegationSubmitCmd(_ *cobra.Command, args []string) {
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

	glb.Assertf(requiredCut <= 1000, "required inflation cut must be 0-1000 promille")

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(feeAmount))

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	client := glb.GetClient()

	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	oIn, _, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)

	ti, err := client.GetSequencerTargetInfo(targetSeqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", targetSeqID.StringShort(), err)

	maxFreezeEpochs = resolveFrozenEpochs(maxFreezeEpochs, ti)

	est := estimateDelegation(consts, client, ti, oIn.Output.TokenBalance(), maxFreezeEpochs, requiredCut, targetSeqID, ts.Slot)
	effCut := confirmDelegationEstimate(est, oIn.Output.TokenBalance(), requiredCut, targetSeqID)

	// If the input is already a delegation output, ensure the master can still
	// unlock it at ts.Slot. Pure wallet-side parse + Constants math.
	// predIsDelegation also tells the builder below whether the predecessor
	// already carries a trailing delegateLockState (to replace vs. append).
	view, predIsDelegation, err := lib.ParseDelegationOutput(oIn.Output.Output, oIn.ID)
	glb.AssertNoError(err)
	if predIsDelegation {
		glb.Assertf(!view.IsInFrozenSlot(ts.Slot, consts),
			"chain is delegation output NOT unlockable by master at slot %d", ts.Slot)
	}

	// One-slot inflation projection, evaluated server-side via /eval.
	inflation := evalChainInflationMultiStep(client, oIn.Output.TokenBalance(), oIn.ID.Slot(), 1)

	// Wasm-style build via txbuildercore + helpers.
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
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
	delegateLockBin, err := lib.NewDelegateLockBytecode(effCut)
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
	stateBin, err := lib.NewDelegateLockState(0, 0, 0)
	glb.AssertNoError(err)

	// Build the successor from the predecessor bytes and overlay ONLY the
	// constraints delegation owns: amounts (0), index-values (1), lock (2),
	// chain (3). Everything else the predecessor carries (foundry at 4,
	// foundryPolicy at 5, …) is preserved untouched — delegation must not
	// drop immutable constraints that aren't its concern. delegateLockState
	// lives at the LAST position (Option C): replace the predecessor's
	// trailing state when re-delegating, otherwise append after the extras.
	ob, err := txbuildercore.OutputBuilderFromBytes(predBytes)
	glb.AssertNoError(err)
	ob.PutConstraint(txbuildercore.EncodeAmounts(newAmount, inflation), txbuildercore.ConstraintIndexAmounts)
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{walletHolderID[:], targetSeqID[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(delegateLockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainTransitionBin, txbuildercore.ConstraintIndexChain)
	if predIsDelegation {
		ob.PutConstraint(stateBin, byte(ob.NumConstraints()-1))
	} else {
		ob.MustPushConstraint(stateBin)
	}
	succIdx := txb.ProduceOutput(ob.Output().Bytes())
	glb.Assertf(succIdx == 0, "succIdx==0")

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	tagAlongIdx := txb.ProduceOutput(tagAlongOut.Bytes())
	glb.Assertf(tagAlongIdx == 1, "tagAlongIdx==1")

	prompt := fmt.Sprintf("delegate %s to sequencer %s (cut %d promille)?", chainID.StringShort(), targetSeqID.String(), effCut)
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts = glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, oIn.ID.Timestamp().AddTicks(int(consts.TransactionPace)))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	glb.TrackTxInclusion(txid, 2*time.Second)
}
