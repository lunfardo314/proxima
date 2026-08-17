package node_cmd

import (
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

const (
	// Small batches on purpose: several cleaners racing over the same dust
	// would each lose their whole transaction to a double-spend conflict, so
	// the batch size bounds what one collision costs.
	defaultCleanupBatchSize = 5
	maxCleanupBatchSize     = 64
)

func initUTXOCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "utxo-cleanup [<UTXOs per transaction. Default 5, maximum 64>]",
		Short: `sweep abandoned dust that has become claimable by anybody`,
		Long: `Scan old ledger state for UTXOs whose conditional lock has expired all the
way into its PUBLIC window — the state in which any signer may consume them —
and sweep them into this wallet, one small transaction at a time, until no
such dust is left.

Two locks have such a window:
  - sendWithDeadline, public once Δ ≥ cleanupSlots (each output carries its
    own deadline in its lock arguments);
  - tagAlong, public once Δ ≥ tag_along_reclaim_slots.

This is the counterpart of 'proxi node compact', not a replacement: compacting
claims outputs this wallet has a role in, cleanup claims outputs that no longer
belong to anybody in particular. Running it is a service to the network — it
returns storage deposits of abandoned UTXOs to circulation and keeps the state
trie from accumulating dust — and the swept tokens go to this wallet.

The node scans by slot chunk (256 slots per trie traversal) and stops as soon
as one batch is filled, so each round costs little. Batches are deliberately
small: cleaners race each other for the same dust, and a collision wastes the
whole transaction, so a small batch bounds the loss. Each transaction is
awaited before the next round starts.

Outputs carrying returnToSender are NOT swept. They are publicly claimable,
but returnToSender keys off the transaction signer rather than the window, so
anyone other than the master must still pay a return receipt in the same
transaction. They are reported and left alone.`,
		Args: cobra.MaximumNArgs(1),
		Run:  runUTXOCleanupCmd,
	}
	cmd.PersistentFlags().Int("max-rounds", 0, "stop after this many transactions (0 = until clean)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runUTXOCleanupCmd(cmd *cobra.Command, args []string) {
	batchSize := defaultCleanupBatchSize
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(1 <= n && n <= maxCleanupBatchSize, "UTXOs per transaction must be 1..%d", maxCleanupBatchSize)
		batchSize = n
	}
	maxRounds, err := cmd.PersistentFlags().GetInt("max-rounds")
	glb.AssertNoError(err)

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)
	glb.Assertf(feeAmount > 0, "tag-along fee resolved to 0. Fee-less option not supported yet")

	walletData := glb.GetWalletData()
	consts := glb.GetLedgerConstants()
	lib := glb.GetTxLibrary()

	glb.Infof("cleaning up abandoned dust into %s, %d UTXO(s) per transaction",
		walletData.Account.String(), batchSize)
	glb.Infof("each transaction pays %s in tag-along fees to %s",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt("proceed?", true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	var (
		scan          client.CleanableOutputsParams
		rounds        int
		sweptOutputs  int
		sweptTokens   uint64
		skippedReturn int
	)
	scan.MaxOutputs = batchSize

	for maxRounds <= 0 || rounds < maxRounds {
		res, err := glb.GetClient().GetCleanableOutputs(scan)
		glb.AssertNoError(err)
		skippedReturn += res.NeedsReturn

		if len(res.Outputs) == 0 {
			if res.Exhausted {
				glb.Infof("no dust left: the scan reached the oldest state")
				break
			}
			// This window of chunks was clean; carry the cursor and keep going.
			scan.FromChunk, scan.FromChunkSet = res.NextChunk, true
			continue
		}

		txid, amount, ok := cleanupRound(lib, consts, walletData, res.Outputs, *tagAlongSeqID, feeAmount)
		rounds++
		if !ok {
			// Most likely another cleaner took the same dust first. Resume from
			// the same place — the state has moved on, so the next scan sees
			// what is actually left.
			glb.Infof("round %d failed; continuing", rounds)
			continue
		}
		sweptOutputs += len(res.Outputs)
		sweptTokens += amount
		glb.Infof("round %d: swept %d UTXO(s), %s tokens, tx %s",
			rounds, len(res.Outputs), util.Th(amount), txid.StringShort())

		if !glb.NoWait() && !glb.TrackTxInclusion(txid, time.Second, time.Minute) {
			glb.Infof("transaction was not confirmed in time; stopping")
			break
		}
		// Re-scan from the top: the state changed under us, and dust taken by
		// other cleaners should not be retried.
		scan.FromChunkSet = false
	}

	glb.Infof("done: %d transaction(s), %d UTXO(s) swept, %s tokens recovered",
		rounds, sweptOutputs, util.Th(sweptTokens))
	if skippedReturn > 0 {
		glb.Infof("skipped %d output(s) carrying returnToSender — claiming them owes the master a return receipt, which this command does not build", skippedReturn)
	}
}

// cleanupRound builds and submits one cleanup transaction. The wallet's own
// funding UTXO goes in FIRST: the swept dust can total less than the storage
// deposit of the single output produced, and it has to cover the tag-along fee
// too, so the sweep cannot stand on the dust alone.
func cleanupRound(
	lib *txbuildercore.Library[any],
	consts *txbuildercore.Constants,
	walletData glb.WalletData,
	dust []*ledger.OutputWithID,
	tagAlongSeqID base.ChainID,
	feeAmount uint64,
) (base.TransactionID, uint64, bool) {

	funding, _, _, err := glb.GetClient().GetTransferableOutputs(walletData.Account, 1)
	if err != nil || len(funding) == 0 {
		glb.Infof("   cannot fund the cleanup transaction: %v", err)
		return base.TransactionID{}, 0, false
	}

	inputs := make([]txbuildercore.CompactInput, 0, len(dust)+1)
	inputs = append(inputs, txbuildercore.CompactInput{
		OutputBytes: funding[0].Output.Bytes(),
		ID:          funding[0].ID,
	})
	dustTotal := uint64(0)
	for _, o := range dust {
		inputs = append(inputs, txbuildercore.CompactInput{OutputBytes: o.Output.Bytes(), ID: o.ID})
		dustTotal += o.Output.TokenBalance()
	}

	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(lib, consts, txbuildercore.CompactParams{
		Inputs:           inputs,
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		TargetSlot:       glb.GetLedgerTimeNow().Slot,
	})
	if err != nil {
		glb.Infof("   cleanup build failed: %v", err)
		return base.TransactionID{}, 0, false
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   cleanup submit failed: %v", err)
		return base.TransactionID{}, 0, false
	}
	return txid, dustTotal, true
}
