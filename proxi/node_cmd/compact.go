package node_cmd

import (
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
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

const (
	defaultMaxNumberOfInputs = 100
)

func initCompactOutputsCmd() *cobra.Command {
	compactCmd := &cobra.Command{
		Use:   "compact [<max number of inputs. Default 100, maximum allowed 256>]",
		Short: `claim+compact all consumable wallet outputs into one ED25519 output`,
		Long: `Sweep all unlockable outputs for this wallet — pure sigLock outputs,
sendWithDeadline outputs the wallet can currently claim, and tag-along
fees the target sequencer never took — into one ED25519 sigLock output
back to the wallet (minus the tag-along fee).

The sendWithDeadline outputs consumed are those where:
  - the wallet is master AND Δ ≥ acceptanceSlots (master-reclaim path), OR
  - the wallet is the sigLock target AND Δ < acceptanceSlots
    (target-accept path).

The tag-along outputs consumed are those where the wallet is the sender
and Δ ≥ tag_along_slots — the fee the wallet prepaid, which the target
sequencer no longer has an exclusive claim on. Past
tag_along_reclaim_slots such a fee is claimable by anyone as well, but it
is still the wallet's own output, so compact keeps sweeping it.

Δ is measured at the wall-clock target slot. chainLock-target
acceptance and the tag-along target side are NOT included — both
require a chain input in the same tx, handled by a separate flow.

Compact only ever claims outputs this wallet has a role in (holder,
master, sender, target). Outputs abandoned by OTHERS that have fallen
into a public window are not its business; sweeping those is a separate
cleanup flow.

Two kinds of claimable outputs are skipped (the rest still compact):
  - sendWithDeadline outputs the wallet accepts as TARGET that carry
    returnToSender: accepting one obliges the taker to pay a return
    receipt to the master in the same tx, which compact does not build.
    Reclaims are unaffected — returnToSender is a noop when the master
    signs, so the wallet's own sendWithDeadline outputs compact normally
    whether or not they carry it.
  - outputs with an unrecognized structure are refused (not consumed);
    re-run with -v to list them.`,
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

	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	feeAmount := glb.GetTagAlongFee()
	if seqMinFee > feeAmount {
		// assume fee asked by the sequencer
		feeAmount = seqMinFee
	}

	walletData := glb.GetWalletData()

	// Wallet-derived "now" — wall-clock mapped through the genesis +
	// tick-duration constants. Singleton-free equivalent of
	// ledger.TimeNow().Slot. Used as both the spendable-filter slot
	// (server-side Δ check) and the tx timestamp slot.
	consts := glb.GetLedgerConstants()
	targetSlot := glb.GetLedgerTimeNow().Slot

	// Peek at what's claimable so we can show a useful summary up front.
	walletOutputs, lrbid, _, err := glb.GetClient().GetSpendableOutputs(walletData.Account, client.SpendableOutputsParams{
		IncludeConditionalLocks: true,
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

	// Classify each candidate at targetSlot with the shared spendable
	// classifier (no ledger singleton). Only SpendSimple outputs can go
	// into a plain sweep; the rest are surfaced and skipped:
	//   - SpendNeedsReturn: SWD-target carrying returnToSender — claiming
	//     needs a return receipt to the master, which compact can't build;
	//   - SpendUnknown: unrecognized structure — refuse to consume.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	var simple, needsReturn, unknown []*ledger.OutputWithID
	for _, o := range walletOutputs {
		cls, err := txbuildercore.ClassifySpendable(lib, o.Output.Bytes(), o.ID.Slot(), walletHolderID, targetSlot, consts.TagAlongSlots)
		glb.AssertNoError(err)
		switch cls {
		case txbuildercore.SpendSimple:
			simple = append(simple, o)
		case txbuildercore.SpendNeedsReturn:
			needsReturn = append(needsReturn, o)
		case txbuildercore.SpendUnknown:
			unknown = append(unknown, o)
		}
	}

	if len(needsReturn) > 0 {
		glb.Infof("skipping %d sendWithDeadline output(s) accepted as target that carry returnToSender — accepting them requires paying a return receipt to the master, which compact does not build.", len(needsReturn))
		glb.Infof("  reclaims of the wallet's own sendWithDeadline outputs are unaffected.")
	}
	if len(unknown) > 0 {
		glb.Infof("refusing %d output(s) with unrecognized structure (not consumed).", len(unknown))
		if glb.IsVerbose() {
			for _, o := range unknown {
				glb.Verbosef("  unknown: %s amount=%s elements=%d", o.ID.StringShort(), util.Th(o.Output.TokenBalance()), o.Output.NumElements())
			}
		} else {
			glb.Infof("  re-run with -v to list them.")
		}
	}

	if len(simple) <= 1 {
		glb.Infof("nothing to compact (%d simply-claimable output(s))", len(simple))
		os.Exit(0)
	}

	// Breakdown of the set actually being swept.
	var sigCount, swdMasterCount, swdTargetCount, tagAlongCount, tagAlongPublic int
	simpleTotal := uint64(0)
	for _, o := range simple {
		simpleTotal += o.Output.TokenBalance()
		lockType, err := lib.ClassifyLock(o.Output.Bytes(), walletHolderID)
		glb.AssertNoError(err)
		switch lockType {
		case txbuildercore.LockKindSig:
			sigCount++
		case txbuildercore.LockKindSWDMaster:
			swdMasterCount++
		case txbuildercore.LockKindSWDTargetSig:
			swdTargetCount++
		case txbuildercore.LockKindTagAlongSender:
			tagAlongCount++
			if targetSlot-o.ID.Slot() >= consts.TagAlongReclaimSlots {
				tagAlongPublic++
			}
		default:
		}
	}
	glb.Infof("claiming %d UTXO(s) into one sigLock back to %s",
		len(simple), walletData.Account.String())
	glb.Infof("  sigLock:                 %d", sigCount)
	glb.Infof("  sendWithDeadline master: %d (reclaim path)", swdMasterCount)
	glb.Infof("  sendWithDeadline target: %d (accept path, sigLock target)", swdTargetCount)
	glb.Infof("  tag-along sender:        %d (reclaim path, fee never taken by the sequencer)", tagAlongCount)
	if tagAlongPublic > 0 {
		glb.Infof("    of which %d are past tag_along_reclaim_slots and claimable by anyone — reclaim them promptly", tagAlongPublic)
	}
	glb.Infof("  total claimable:         %s tokens", util.Th(simpleTotal))

	// Attachment-cost-budget gate. Per-tx cost = NumInputs + NumProducedOutputs
	// (transaction/tx.go AttachmentCost). The network's budget covers
	// pastCone-cost + this tx; we can't know pastCone-cost client-side,
	// but we CAN bound self-cost. Two outputs (compacted + tag-along).
	numProduced := 2
	selfCost := len(simple) + numProduced
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

	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(lib, consts, txbuildercore.CompactParams{
		Inputs:           compactInputs(simple),
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    *tagAlongSeqID,
		TagAlongFee:      feeAmount,
		TargetSlot:       targetSlot,
	})
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

// compactInputs projects fetched wallet UTXOs onto the bytes-only shape the
// shared compact builder takes.
func compactInputs(outs []*ledger.OutputWithID) []txbuildercore.CompactInput {
	ret := make([]txbuildercore.CompactInput, len(outs))
	for i, o := range outs {
		ret[i] = txbuildercore.CompactInput{OutputBytes: o.Output.Bytes(), ID: o.ID}
	}
	return ret
}
