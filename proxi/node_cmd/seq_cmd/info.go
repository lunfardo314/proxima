package seq_cmd

import (
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"golang.org/x/exp/maps"
)

func initSeqInfoCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:   "info",
		Short: `displays sequencer info: full sequencer data plus the delegations targeting it`,
		Args:  cobra.NoArgs,
		Run:   runSeqInfoCmd,
	}

	glb.AddFlagTarget(seqSendCmd)

	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

// delegationItem pairs a parsed delegation view with the underlying
// raw balance, for the targets-of-this-sequencer summary.
type delegationItem struct {
	view    *txbuildercore.DelegationOutputView
	balance uint64
}

func runSeqInfoCmd(_ *cobra.Command, _ []string) {
	seqID := sequencerInQuestion()

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	clnt := glb.GetClient()
	chains, _, err := clnt.GetAllChains()
	glb.AssertNoError(err)

	var seqUTXO *ledger.OutputWithChainID
	delegations := make([]delegationItem, 0)

	for _, ch := range chains {
		if ch.ChainID == seqID {
			seqUTXO = ch
			continue
		}
		view, ok, err := lib.ParseDelegationOutput(ch.Output.Output, ch.ID)
		if err != nil || !ok {
			continue
		}
		if view.Target == seqID {
			delegations = append(delegations, delegationItem{view: view, balance: ch.Output.TokenBalance()})
		}
	}
	glb.Assertf(seqUTXO != nil, "can't find chain output with ID %s", seqID.String())

	// Sequencer chain output summary. Singleton-free: lock symbol via
	// the wallet library, sequencer data via the singleton-free
	// ledger.ParseSequencerData (verified pure byte parse).
	printSequencerOutputSummary(lib, seqUTXO)
	printSequencerData(seqUTXO)

	if len(delegations) == 0 {
		glb.Infof("\nno delegations to display")
		return
	}
	sort.Slice(delegations, func(i, j int) bool {
		return delegations[i].balance > delegations[j].balance
	})
	currentSlot := glb.GetLedgerTimeNow().Slot
	glb.Infof("\ncurrent slot %d", currentSlot)
	glb.Infof("\n---- delegations (%d) ----", len(delegations))

	unfreezeBySlot := make(map[uint32]int)
	revocable := 0
	for _, d := range delegations {
		glb.Infof("   %s  %20s  %s  maxFreeze: %d  master: %s",
			d.view.ChainID.String(), util.Th(d.balance),
			glb.DelegationStatusString(d.view, currentSlot, consts),
			consts.DelegationMaxFrozenEpochs,
			ledger.SigLock(d.view.MasterID).String())
		if d.view.IsInFrozenSlot(currentSlot, consts) {
			unfreezeBySlot[d.view.UnfreezeSlot(consts)]++
		}
		if d.view.IsInSafeRevocationWindow(currentSlot, consts) {
			revocable++
		}
	}

	slots := maps.Keys(unfreezeBySlot)
	sort.Slice(slots, func(i, j int) bool {
		return slots[i] < slots[j]
	})
	glb.Infof("\n---- unfreezes by slot ----")
	// epochSlots is identical across every delegation targeting this
	// sequencer (inlined and immutable). Pull it from the first
	// delegation; fall back to the wallet's Constants default if
	// there are none.
	epochSlots := consts.DelegationEpochSlots
	for _, s := range slots {
		epoch := consts.EpochFromSlotDirect(seqID, s, epochSlots)
		glb.Infof("   %d: %d (epoch %d)", s, unfreezeBySlot[s], epoch)
	}
	glb.Infof("number of unlockable by master: %d", revocable)
}

// printSequencerOutputSummary writes a wallet-side summary of the
// sequencer's chain output: chain metadata + balance + the controller
// lock symbol. Replaces the singleton-bound seqUTXO.LinesHR dump.
func printSequencerOutputSummary(lib *txbuildercore.Library[any], seqUTXO *ledger.OutputWithChainID) {
	glb.Infof("\n---- the chain output ----")
	glb.Infof("    chain ID:        %s", seqUTXO.ChainID.String())
	glb.Infof("    output ID:       %s", seqUTXO.ID.String())
	glb.Infof("    balance:         %s", util.Th(seqUTXO.Output.TokenBalance()))
	// Lock symbol via the wallet library — singleton-free.
	if lockBin, err := seqUTXO.Output.ConstraintAt(ledger.ConstraintIndexLock); err == nil {
		if sym, _, _, err := lib.ParseBytecodeOneLevel(lockBin); err == nil {
			glb.Infof("    controller lock: %s", sym)
		}
	}
	if cc, err := lib.ParseChainConstraint(seqUTXO.Output.MustConstraintAt(ledger.ConstraintIndexChain)); err == nil {
		glb.Infof("    origin slot:     %d", cc.OriginSlot)
		glb.Infof("    transitions:     %d", cc.TransitionCounter)
		glb.Infof("    branches:        %d", cc.BranchCounter)
		totalInflation := cc.CumulativeChainInflation + cc.CumulativeBranchBonus
		glb.Infof("    cum. inflation:  %s (chain: %s, branch bonus: %s)",
			util.Th(totalInflation), util.Th(cc.CumulativeChainInflation), util.Th(cc.CumulativeBranchBonus))
	}
}

// printSequencerData lists every key of the sequencer data carried by the
// chain output, the two an operator acts on first — the sequencer cut and the
// minimum fee — at the top. Keys are listed even when unset, so the reader sees
// the whole set rather than only what happens to be non-default; keys this
// build does not recognise are listed verbatim after the known ones.
func printSequencerData(seqUTXO *ledger.OutputWithChainID) {
	glb.Infof("\n---- sequencer data ----")
	if !seqUTXO.ID.IsSequencerTransaction() {
		glb.Infof("    (not a sequencer chain)")
		return
	}
	sd, err := ledger.ParseSequencerData(seqUTXO.Output)
	if err != nil {
		glb.Infof("    (cannot parse sequencer data: %v)", err)
		return
	}
	cut := sd.InflationProfitMarginPromille()
	glb.Infof("    profit_cut     sequencer (inflation) cut: %d promille (%.1f%%)", cut, float64(cut)/10)
	glb.Infof("    fee            minimum tag-along fee:     %s", util.Th(sd.MinimumFee()))
	glb.Infof("    name           sequencer name:            '%s'", sd.Name())
	glb.Infof("    greedy         greedy:                    %v", sd.IsGreedy())
	glb.Infof("    pace           pace, ticks:               %d", sd.Pace())
	glb.Infof("    freeze_bounds  enforce freeze bounds:     %v", sd.IsFreezeBoundsEnforced())

	unknown := sd.UnknownKeys()
	if len(unknown) == 0 {
		return
	}
	keys := maps.Keys(unknown)
	sort.Strings(keys)
	glb.Infof("    keys not recognised by this build:")
	for _, k := range keys {
		glb.Infof("    %-14s %s", k, string(unknown[k]))
	}
}
