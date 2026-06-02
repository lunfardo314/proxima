package seq_cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/spf13/cobra"
)

func initSeqSetCmd() *cobra.Command {
	setCmd := &cobra.Command{
		Use:   "set-params",
		Short: `update sequencer parameters (name, fee, margin, greedy, pace, ignore-freeze-bound)`,
		Args:  cobra.NoArgs,
		Run:   runSeqSetCmd,
	}

	glb.AddFlagTarget(setCmd)

	setCmd.Flags().String("name", "", "sequencer name")
	setCmd.Flags().Uint64("fee", 0, "minimum tag-along fee")
	setCmd.Flags().Uint16("margin", 0, "inflation profit margin promille (0-1000)")
	setCmd.Flags().Bool("greedy", false, "greedy flag")
	setCmd.Flags().Uint8("pace", 0, "pace value (ticks)")
	setCmd.Flags().Bool("ignore-freeze-bound", false, "ignore upper bound on freeze")

	setCmd.InitDefaultHelpCmd()
	return setCmd
}

func runSeqSetCmd(cmd *cobra.Command, _ []string) {
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")

	seqID := *walletData.Sequencer
	glb.Infof("sequencer id: %s", seqID.String())

	// Fetch current sequencer data.
	clnt := glb.GetClient()
	seqOut, _, err := clnt.GetChainOutput(seqID)
	glb.AssertNoError(err)

	currentSD, err := ledger.ParseSequencerData(seqOut.Output)
	if err != nil {
		currentSD = seqdata.SequencerData{}
	}

	// Apply only explicitly changed flags.
	newSD := currentSD.Clone()
	changed := false

	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		glb.Assertf(len(v) <= 6, "name must be empty (reset to default) or 1 to 6 characters")
		newSD.SetName(v)
		changed = true
	}
	if cmd.Flags().Changed("fee") {
		v, _ := cmd.Flags().GetUint64("fee")
		newSD.SetMinimumFee(v)
		changed = true
	}
	if cmd.Flags().Changed("margin") {
		v, _ := cmd.Flags().GetUint16("margin")
		glb.Assertf(v <= 1000, "margin must be 0-1000")
		newSD.SetSeqProfitMarginPromille(v)
		changed = true
	}
	if cmd.Flags().Changed("greedy") {
		v, _ := cmd.Flags().GetBool("greedy")
		newSD.SetGreedy(v)
		changed = true
	}
	if cmd.Flags().Changed("pace") {
		v, _ := cmd.Flags().GetUint8("pace")
		newSD.SetPace(v)
		changed = true
	}
	if cmd.Flags().Changed("ignore-freeze-bound") {
		v, _ := cmd.Flags().GetBool("ignore-freeze-bound")
		newSD.SetIgnoreFreezeBound(v)
		changed = true
	}

	if !changed {
		glb.Infof("no flags specified, nothing to change")
		glb.Infof("current sequencer data:\n%s", currentSD.Lines("  ").String())
		return
	}

	glb.Infof("current:\n%s", currentSD.Lines("  ").String())
	glb.Infof("new:\n%s", newSD.Lines("  ").String())

	// Get the minimum tag-along fee from the sequencer.
	fee, err := glb.GetRequiredTagAlongFee(seqID)
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

	// Compose the set-seq-data sequencer-request output.
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldSetSequencerDataBinary, newSD.Bytes())
	reqOut, err := lib.NewSequencerRequestOutput(
		fee,
		seqID,
		walletHolderID,
		txbuilder_seq.RequestCodeSetSequencerData,
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

	prompt := fmt.Sprintf("\nupdate sequencer %s parameters?", seqID.String())
	if !glb.YesNoPrompt(prompt, true) {
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
