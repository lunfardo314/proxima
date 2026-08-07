package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// tryBootstrapProposal generates a bootstrap transaction: a non-branch transaction with an
// explicit baseline (the LRB), issued once per slot for as long as the network is not branching.
// This bootstraps the network: when all sequencer start UTXOs are far in the past, there's nothing
// to endorse, so the explicit baseline bypasses endorsement. Once several sequencers produce
// bootstrap transactions, they can endorse each other and coverage starts growing, until it exceeds
// the health threshold and branches become possible again.
// Returns nil when the bootstrap condition is not met (normal operation).

const TraceTagBootstrapProposer = "propose-bootstrap"

// bootstrapMaxTick is the last tick of the slot at which a bootstrap transaction is issued. The
// bootstrap transaction is what the other sequencers consolidate their coverage on, so it must
// leave them most of the slot to do it. Past this tick the proposer stays silent and the sequencer
// takes the early ticks of the next slot instead.
const bootstrapMaxTick = base.TicksPerSlot / 4

// bootstrapLRBLagSlots is how far the latest reliable branch must fall behind the target slot before
// the network counts as stuck and bootstrap transactions are issued — roughly half a minute at the
// default slot duration. Normal operation keeps the LRB within a slot or two of the tip, so this
// only trips once branches have actually stopped, not when the network is merely branching slowly.
// It also keeps the explicit baseline in a past slot, which the ledger requires.
const bootstrapLRBLagSlots = 3

func (t *taskData) tryBootstrapProposal() *finalProposal {
	extend := t.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		t.Log().Warnf("BootstrapProposer-%s: can't find own latest milestone output", t.Name)
		return nil
	}

	// A slot boundary is a branch target, never a bootstrap one. Otherwise the only per-slot limit
	// here is one bootstrap transaction: whether we are in the bootstrap state is decided from the
	// LRB below, not from how old our own milestone is. Using our own milestone as the proxy also
	// suppressed the next bootstrap for a slot, halving the rate at which the other sequencers could
	// consolidate coverage on it — and, if they alternate out of phase, shrinking the set of
	// bootstrap transactions available to consolidate on in any one slot.
	if t.targetTs.IsSlotBoundary() || extend.VID.Slot() >= t.targetTs.Slot {
		t.Tracef(TraceTagBootstrapProposer, "idle phase(%s). target: %s, extend: %s", t.Name, t.targetTs.String, extend.IDStringShort)
		return nil
	}

	if t.targetTs.Tick > bootstrapMaxTick {
		t.Tracef(TraceTagBootstrapProposer, "%s: target tick %d is past the bootstrap zone, waiting for the next slot", t.Name, t.targetTs.Tick)
		return nil
	}

	lrb := t.Branches().FindLatestReliableBranch()
	if lrb == nil {
		t.Log().Warnf("BootstrapProposer-%s: can't find latest reliable branch", t.Name)
		return nil
	}

	// The bootstrap state itself: no reliable branch for the last few slots.
	if lrb.Stem.ID.Slot()+bootstrapLRBLagSlots > t.targetTs.Slot {
		t.Tracef(TraceTagBootstrapProposer, "%s LRB slot %d is less than %d slots behind target slot %d, not in bootstrap condition",
			t.Name, lrb.Stem.ID.Slot(), bootstrapLRBLagSlots, t.targetTs.Slot)
		return nil
	}

	a, err := attacher.NewIncrementalAttacherWithExplicitBaseline(t.Name, t.environment, t.targetTs, extend, lrb.Stem.ID.TransactionID())
	if err != nil {
		t.Tracef(TraceTagBootstrapProposer, "%s can't create attacher: '%v'", t.Name, err)
		return nil
	}
	t.Tracef(TraceTagBootstrapProposer, "%s created attacher with baseline %s, cov: %s",
		t.Name, a.BaselineBranch().StringShort, func() string { return util.Th(a.FinalLedgerCoverage(t.targetTs)) },
	)

	prop, err := t.newProposal(a)
	if err != nil {
		t.Tracef(TraceTagBootstrapProposer, "%s can't create proposal: '%v'", t.Name, err)
		return nil
	}

	// freezes only, on the whole attachment cost budget (insertInputs takes the bootstrap path).
	// The bootstrap transaction has no endorsements and an explicit baseline, so its past cone is
	// nearly empty and almost the entire budget is available — the cheapest place in the slot to
	// re-freeze the delegations which unfroze while the network was down.
	prop.insertInputs()

	fp, err := prop.finalize("bootstrap")
	if err != nil {
		t.logFinalizeFailure("BootstrapProposer-"+t.Name, err)
		return nil
	}
	lrbTxID := lrb.Stem.ID.TransactionID()
	// evidence of the bootstrap is logged when the transaction is actually submitted
	// (a proposal can still lose the coverage comparison in task.Run)
	t.Tracef(TraceTagBootstrapProposer, "%s built: target=%s extend=%s extSlot=%d baselineLRB=%s",
		t.Name, t.targetTs.String(), extend.IDStringShort(), extend.VID.Slot(), lrbTxID.StringShort())
	return fp
}
