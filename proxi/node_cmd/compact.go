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

	// manage tag along data

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	sd, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)

	feeAmount := glb.GetTagAlongFee()
	if sd.MinimumFee() > feeAmount {
		// assume fee asked by the sequencer
		feeAmount = sd.MinimumFee()
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

	// Sort descending by amount and cap (mirrors the legacy behavior).
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
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	var sigCount, swdMasterCount, swdTargetCount int
	for _, o := range walletOutputs {
		lockType, err := lib.ClassifyLock(o.Bytes(), walletHolderID)
		glb.AssertNoError(err)
		switch lockType {
		case txbuildercore.LockKindSig:
			sigCount++
		case txbuildercore.LockKindSWDMaster:
			swdMasterCount++
		case txbuildercore.LockKindSWDTargetSig:
			swdTargetCount++
		default:
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
		walletData.PrivateKey, walletOutputs,
		*tagAlongSeqID, feeAmount, targetSlot)
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

// makeClaimingCompactTransaction is the pure wasm-wallet compose
// helper for `proxi node compact`: it consumes the supplied spendable
// wallet UTXOs (sigLock-owned + claim-eligible sendWithDeadline) into
// a single sigLock output back to the wallet (minus the tag-along
// fee). No I/O; no ledger.L() singleton; no ledger/txbuilder sugar.
// Intended as the reference template for other proxi tx-construction
// sites.
//
// Input unlock pattern: PutSignatureUnlock(0) on input 0 (carries
// the tx signature) + PutUnlockReference(i, ConstraintIndexLock, 0)
// on the rest. The reference path makes `_sigLock` succeed in
// `unlockedByReference` for the homogeneous sigLock inputs — same
// lock bytecode + same holderID — skipping one
// txHolderID(txSignatureData) per referenced input.
//
// For SWD inputs the reference path's lock-bytecode-equality check
// fails (SWD ≠ sigLock), so `_sigLock` falls through to the holder
// check against the tx signer — same outcome as if we had used a
// signature unlock on that input. Net effect: pure savings on the
// homogeneous sigLock portion, neutral on the mixed SWD portion.
//
// Inputs:
//   - walletPrivateKey: signs the tx.
//   - walletOutputs:    pre-fetched spendable set; the caller is
//     responsible for the GetSpendableOutputs call
//     so the UX summary and the build see the
//     same snapshot.
//   - tagAlongSeqID / tagAlongFee: tag-along target + amount. The
//     fee output is always produced; the caller is expected to
//     enforce fee > 0.
//   - targetSlot:       tx timestamp slot. MUST match the slot used
//     for the spendable filter (the SWD Δ check
//     needs them to agree).
func makeClaimingCompactTransaction(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	targetSlot uint32,
) (txBytes []byte, txid base.TransactionID, consumed [][]byte, err error) {

	lib := glb.GetTxLibrary()
	txb := txbuildercore.New(0)

	inTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, nil, err
			}
		}
		inTotal += in.Output.TokenBalance()
	}
	if inTotal < tagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("not enough balance for the tag-along fee")
	}

	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	mainOut, err := txbuildercore.NewSigLockOutput(lib, inTotal-tagAlongFee, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(mainOut.Bytes())

	taOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(taOut.Bytes())

	txb.SetTimestamp(base.T(targetSlot, 10))
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	return txBytes, txid, consumed, nil
}
