package seq_cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/spf13/cobra"
)

func initSeqWithdrawCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw <amount>",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to controller's account or to the the target lock`,
		Args:    cobra.ExactArgs(1),
		Run:     runSeqWithdrawCmd,
	}

	glb.AddFlagTarget(seqSendCmd)

	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")
	glb.Infof("sequencer id (source): %s", walletData.Sequencer.String())

	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	glb.Infof("amount: %s", util.Th(amount))

	// Get the minimum tag-along fee from the sequencer.
	fee, err := glb.GetRequiredTagAlongFee(*walletData.Sequencer)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(fee))

	// Wasm-style build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)

	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(12)
	}

	// Pull wallet inputs (all sigLock-controlled outputs).
	clnt := glb.GetClient()
	walletOutputs, _, amountInWallet, err := clnt.GetTransferableOutputs(walletData.Account, 255)
	glb.AssertNoError(err)
	glb.Assertf(len(walletOutputs) > 0, "wallet has no outputs to create transaction")
	glb.Assertf(amountInWallet >= fee, "not enough balance: have %d, need %d", amountInWallet, fee)

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

	// Compose the withdraw sequencer-request output.
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	params.Set(txbuilder_seq.FieldWithdrawTarget, []byte(targetLock.Source()))
	reqOut, err := lib.NewSequencerRequestOutput(
		fee,
		*walletData.Sequencer,
		walletHolderID,
		txbuilder_seq.RequestCodeWithdrawFromSeq,
		&params,
	)
	glb.AssertNoError(err)
	txb.ProduceOutput(reqOut.Bytes())

	// Remainder back to wallet.
	if amountInWallet > fee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, amountInWallet-fee, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	prompt := fmt.Sprintf("\nwithdraw %s from sequencer %s?", util.Th(amount), walletData.Sequencer.String())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts = glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(12)
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

	glb.Infof("submitting the transaction...")

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}
