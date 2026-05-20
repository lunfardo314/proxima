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

Δ is measured at the current LRB slot. chainLock-target acceptance is
NOT included — it requires a chain input in the same tx, handled by a
separate flow.`,
		Args: cobra.MaximumNArgs(1),
		Run:  runCompactCmd,
	}
	compactCmd.InitDefaultHelpCmd()
	return compactCmd
}

func runCompactCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

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

	// Targeting "now" is the safest choice for a sweep: target-accept
	// windows must be open, master-reclaim windows must be open. The
	// client filter applies the same Δ rule the on-chain constraint
	// enforces at tx validation.
	targetSlot := ledger.TimeNow().Slot

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

	// Quick breakdown so the user sees what's being swept.
	var (
		sigCount, swdMasterCount, swdTargetCount int
	)
	for _, o := range walletOutputs {
		switch l := o.Output.Lock().(type) {
		case ledger.SigLock:
			_ = l
			sigCount++
		case *ledger.SendWithDeadlineLock:
			if l.MasterID == base.HolderID(walletData.Account) {
				swdMasterCount++
			} else {
				swdTargetCount++
			}
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
	budget := ledger.L(targetSlot).AttachmentCostBudget
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

	tx, err := glb.MakeClaimingCompactTransaction(
		walletData.PrivateKey, tagAlongSeqID, feeAmount, targetSlot, maxNumberOfInputs)
	if tx != nil {
		glb.Verbosef("------- the compacting transaction -------- \n%s\n--------------------------", tx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(tx != nil, "something wrong: transaction context is nil")
	txBytes := tx.Bytes()
	glb.Infof("submitting compacting tx %s with %d inputs (%d bytes)...",
		tx.IDShortString(), tx.NumInputs(), len(txBytes))
	err = glb.GetClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if !glb.NoWait() {
		glb.TrackTxInclusion(tx.ID(), time.Second)
	}
}
