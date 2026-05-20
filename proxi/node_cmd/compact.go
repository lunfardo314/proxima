package node_cmd

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

const (
	defaultMaxNumberOfInputs = 100
)

func initCompactOutputsCmd() *cobra.Command {
	compactCmd := &cobra.Command{
		Use:   "compact [<max number of inputs. Default 100, maximum allowed 256>]",
		Short: `claim+compact all consumable wallet outputs into one ED25519 output`,
		Long: `Sweep all unlockable outputs for this wallet — pure sigLock outputs AND
sendWithDeadline outputs the wallet can currently claim — into one
ED25519 sigLock output back to the wallet (minus the tag-along fee).

The sendWithDeadline outputs consumed are those where:
  - the wallet is master AND Δ ≥ acceptanceSlots (master-reclaim path), OR
  - the wallet is the sigLock target AND Δ < acceptanceSlots
    (target-accept path).

Δ is measured at the wall-clock target slot. chainLock-target
acceptance is NOT included — it requires a chain input in the same
tx, handled by a separate flow.`,
		Args: cobra.MaximumNArgs(1),
		Run:  runCompactCmd,
	}
	compactCmd.InitDefaultHelpCmd()
	return compactCmd
}

func runCompactCmd(_ *cobra.Command, args []string) {
	maxNumberOfInputs := defaultMaxNumberOfInputs
	var err error
	if len(args) > 0 {
		maxNumberOfInputs, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(2 <= maxNumberOfInputs && maxNumberOfInputs <= 256, "parameter must be >= 2 and <= 256")
	}

	var tagAlongSeqID *base.ChainID
	feeAmount := glb.GetTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = glb.GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		sd, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if sd.MinimumFee() > feeAmount {
			feeAmount = sd.MinimumFee()
		}
	}
	walletData := glb.GetWalletData()

	// Wallet-derived "now" — wall-clock mapped through the genesis +
	// tick-duration constants. Singleton-free equivalent of
	// ledger.TimeNow().Slot. Used as both the spendable-filter slot
	// (server-side Δ check) and the tx timestamp slot.
	consts := glb.GetLedgerConstants()
	targetSlot := consts.LedgerTimeFromClockTime(time.Now()).Slot

	// Peek at what's claimable so we can show a useful summary up front.
	walletOutputs, lrbid, totalAmount, err := glb.GetClient().GetSpendableOutputs(walletData.Account, client.SpendableOutputsParams{
		IncludeSendWithDeadline: true,
		TargetSlot:              targetSlot,
		MaxOutputs:              maxNumberOfInputs,
	})
	glb.AssertNoError(err)

	// Sort descending by amount and cap (mirrors the legacy behaviour).
	sort.Slice(walletOutputs, func(i, j int) bool {
		return walletOutputs[i].Output.TokenBalance() > walletOutputs[j].Output.TokenBalance()
	})
	if len(walletOutputs) > maxNumberOfInputs {
		walletOutputs = slices.Clone(walletOutputs[:maxNumberOfInputs])
	}

	glb.PrintLRB(lrbid)
	if len(walletOutputs) <= 1 {
		glb.Infof("nothing to compact (only %d claimable output)", len(walletOutputs))
		os.Exit(0)
	}

	// Quick breakdown so the user sees what's being swept — uses the
	// wallet library's bytecode parser, no ledger singleton.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderID(walletData.Account)
	var sigCount, swdMasterCount, swdTargetCount int
	for _, o := range walletOutputs {
		ivBin := o.Output.MustConstraintAt(1)
		lockBin := o.Output.MustConstraintAt(2)
		switch glb.ClassifyLock(lib, ivBin, lockBin, walletHolderID) {
		case glb.LockKindSig:
			sigCount++
		case glb.LockKindSWDMaster:
			swdMasterCount++
		case glb.LockKindSWDTargetSig:
			swdTargetCount++
		}
	}
	glb.Infof("claiming %d UTXO(s) into one sigLock back to %s",
		len(walletOutputs), walletData.Account.String())
	glb.Infof("  sigLock:                 %d", sigCount)
	glb.Infof("  sendWithDeadline master: %d (reclaim path)", swdMasterCount)
	glb.Infof("  sendWithDeadline target: %d (accept path, sigLock target)", swdTargetCount)
	glb.Infof("  total claimable:         %d tokens", totalAmount)

	// Attachment-cost-budget gate. Per-tx cost = NumInputs + NumProducedOutputs
	// (transaction/tx.go AttachmentCost). The network's budget covers
	// pastCone-cost + this tx; we can't know pastCone-cost client-side,
	// but we CAN bound self-cost. Two outputs (compacted + tag-along).
	numProduced := 2
	selfCost := len(walletOutputs) + numProduced
	budget := consts.AttachmentCostBudget
	glb.Assertf(selfCost <= budget,
		"compact tx self-cost %d would exceed the network's attachment cost budget %d. "+
			"Pass a lower max-inputs cap (current: %d).",
		selfCost, budget, maxNumberOfInputs)
	if selfCost*2 > budget {
		glb.Infof("WARNING: compact tx self-cost is %d, budget is %d.", selfCost, budget)
		glb.Infof("  The remaining %d-cost headroom is shared with the past-cone of each "+
			"non-rooted predecessor (its issuing tx and any of ITS predecessors that are not "+
			"yet in a branch state). On a freshly-funded wallet this can exceed the budget "+
			"and the attacher will reject the tx. Consider running with a smaller cap.", budget-selfCost)
	}

	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	prompt := fmt.Sprintf("compacting will cost %d in tag-along fees paid to %s. Proceed?",
		feeAmount, tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txBytes, txid, consumed, err := makeClaimingCompactTransaction(
		walletData.PrivateKey, tagAlongSeqID, feeAmount, targetSlot, maxNumberOfInputs)
	glb.AssertNoError(err)
	glb.Assertf(txBytes != nil, "something wrong: empty compact tx")

	glb.Infof("submitting compacting tx %s with %d inputs (%d bytes)...",
		txid.StringShort(), len(consumed), len(txBytes))
	if err := glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		os.Exit(1)
	}

	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}
}

// makeClaimingCompactTransaction builds (but does NOT submit) the
// claim-sweep tx: it consumes all spendable wallet UTXOs at targetSlot
// — sigLock-owned outputs plus sendWithDeadline outputs the wallet can
// currently claim (master-reclaim and sigLock target-accept paths) —
// into a single sigLock output back to the wallet (minus the
// tag-along fee).
//
// All inputs use the signature unlock (0xff) because:
//   - on a plain sigLock input it satisfies the holder check;
//   - on a sendWithDeadline input the consumed-side dispatch lands in
//     `_sigLock($master)` (reclaim) or `_sigLock($target)` (accept);
//     both fall through `unlockedByReference` (which fails because
//     the SWD lock bytecode ≠ sigLock bytecode) onto the same
//     signature check, which matches the wallet's holderID.
//
// Built entirely on txbuildercore + the wallet helpers — no ledger
// singleton dependency.
func makeClaimingCompactTransaction(
	walletPrivateKey ed25519.PrivateKey,
	tagAlongSeqID *base.ChainID,
	tagAlongFee uint64,
	targetSlot uint32,
	maxInputs int,
) (txBytes []byte, txid base.TransactionID, consumed [][]byte, err error) {
	walletAccount := ledger.SigLockFromED25519PrivateKey(walletPrivateKey)
	walletHolderID := base.HolderID(walletAccount)

	walletOutputs, _, inTotal, err := glb.GetClient().GetSpendableOutputs(walletAccount, client.SpendableOutputsParams{
		IncludeSendWithDeadline: true,
		TargetSlot:              targetSlot,
		MaxOutputs:              maxInputs,
	})
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	if len(walletOutputs) <= 1 {
		return nil, base.TransactionID{}, nil, nil
	}
	if inTotal < tagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("not enough balance for the tag-along fee")
	}
	if tagAlongFee > 0 && tagAlongSeqID == nil {
		return nil, base.TransactionID{}, nil, fmt.Errorf("tag-along sequencer not specified")
	}

	lib := glb.GetTxLibrary()
	txb := txbuildercore.New(0)

	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		txb.PutSignatureUnlock(byte(i))
	}

	mainOut, err := txbuildercore.NewSigLockOutput(lib, inTotal-tagAlongFee, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(mainOut.Bytes())

	if tagAlongFee > 0 {
		taOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, *tagAlongSeqID, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, nil, err
		}
		txb.ProduceOutput(taOut.Bytes())
	}

	txb.SetTimestamp(base.T(targetSlot, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	return txBytes, txid, consumed, nil
}
