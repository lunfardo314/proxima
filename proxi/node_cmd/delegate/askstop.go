package delegate

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/spf13/cobra"
)

func initRevokeDelegationCmd() *cobra.Command {
	revokeCmd := &cobra.Command{
		Use:     "askstop <delegation ID>",
		Aliases: util.List("stop"),
		Short:   "send 'stop delegation' request to the target sequencer with the given delegation ID",
		Args:    cobra.ExactArgs(1),
		Run:     runRevokeDelegationCmd,
	}

	glb.AddFlagTarget(revokeCmd)

	revokeCmd.InitDefaultHelpCmd()
	return revokeCmd
}

func runRevokeDelegationCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	delegationID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)

	clnt := glb.GetClient()
	out, _, err := clnt.GetChainOutput(delegationID)
	glb.AssertNoError(err)
	view, ok, err := lib.ParseDelegationOutput(out.Output.Output, out.ID)
	glb.AssertNoError(err)
	glb.Assertf(ok, "not a delegation output: %s", delegationID.String())
	if glb.IsVerbose() {
		glb.Infof("delegation output:\n%s", out.String())
	}

	glb.Assertf(view.MasterID == walletHolderID, "this wallet is not a master controller of the delegation %s", delegationID.String())

	targetID := view.Target
	glb.Infof("delegation target ID: %s", targetID.String())

	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(5)
	}
	// `askstop` is meaningful only while the master CANNOT unlock the
	// delegation directly (i.e. it's in a frozen slot). Otherwise the
	// master can just consume the output.
	glb.Assertf(view.IsInFrozenSlot(ts.Slot, consts), "delegation is unlockable by master, no need for revocation")
	unfreeze := view.UnfreezeSlot(consts)
	glb.Assertf(unfreeze > ts.Slot+6, "delegation is not frozen or safe revocation window is very close, just wait up to a minute")

	// The request output carries the ordinary tag-along fee; whatever
	// compensation it does not cover is authorised as an allowance and comes
	// out of the delegation itself. That is the point of the allowance: a
	// delegator need not park liquid tokens just to be able to stop. Shared
	// with the display path so the figure shown by `node chain` / `balance`
	// is the one actually charged.
	compensation, fee, allowance, err := glb.AskStopCost(clnt, out.Output.TokenBalance(), ts.Slot, unfreeze)
	glb.AssertNoError(err)
	const minimumFee = 50
	glb.Assertf(compensation >= minimumFee, "estimated compensation is even less than minimum fee %d", minimumFee)

	// Pull wallet inputs (all sigLock-controlled outputs).
	walletOutputs, _, amountInWallet, err := clnt.GetTransferableOutputs(walletData.Account, 255)
	glb.AssertNoError(err)
	glb.Assertf(len(walletOutputs) > 0, "wallet has no outputs to create transaction")

	// a wallet too poor for the full fee shifts the shortfall to the allowance
	if fee > amountInWallet {
		allowance += fee - amountInWallet
		fee = amountInWallet
	}
	// Ceiling the constraint will enforce. Measured from the delegation
	// output's own slot, so it does not move while the request sits in the
	// tag-along window.
	ceiling := evalChainInflationMultiStep(clnt, out.Output.TokenBalance(), out.ID.Slot(), unfreeze-out.ID.Slot())
	glb.Assertf(allowance <= ceiling, "computed allowance %s exceeds the ceiling %s", util.Th(allowance), util.Th(ceiling))

	glb.Infof("delegation balance: %s", util.Th(out.Output.TokenBalance()))
	glb.Infof("estimated compensation to the sequencer: %s", util.Th(compensation))
	glb.Infof("   paid from this wallet (tag-along fee): %s", util.Th(fee))
	glb.Infof("   taken from the delegation (allowance): %s", util.Th(allowance))
	if allowance > 0 {
		prompt := fmt.Sprintf("authorise sequencer %s to take up to %s out of delegation %s?",
			targetID.StringShort(), util.Th(allowance), delegationID.StringShort())
		if !glb.YesNoPrompt(prompt, true) {
			glb.Infof("exit")
			os.Exit(0)
		}
	}

	txb := txbuildercore.New(0)
	consumedBytes := make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	// Compose the ask-stop-delegation sequencer-request output.
	extra, err := lib.NewEnsureStopDelegationConstraint(delegationID, allowance)
	glb.AssertNoError(err)
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldRevokeDelegationID, delegationID[:])
	reqOut, err := lib.NewSequencerRequestOutput(
		fee,
		targetID,
		walletHolderID,
		txbuilder_seq.RequestCodeAskStopDelegation,
		&params,
		extra,
	)
	glb.AssertNoError(err)
	txb.ProduceOutput(reqOut.Bytes())

	// Remainder back to wallet.
	if amountInWallet > fee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, amountInWallet-fee, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	prompt := fmt.Sprintf("send request to stop delegation %s to the sequencer %s?", delegationID.StringShort(), targetID.String())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts = glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(5)
	}
	for _, in := range walletOutputs {
		ts = base.MaximumTime(ts, in.Timestamp())
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	glb.TrackTxInclusion(txid, time.Second)
}
