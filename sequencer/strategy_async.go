package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	// targetIntervalTicks is the number of ticks between non-branch milestone targets
	targetIntervalTicks = 12
	// milestoneWatchInterval is how often the background watcher polls the tippool
	milestoneWatchInterval = 20 * time.Millisecond
)

// getNextTargetTime computes the next target timestamp for milestone generation.
//
// Target pacing within a slot:
//
//	branch → post-consolidation (ASAP) → +pace → +pace → ... → branch
func (seq *Sequencer) getNextTargetTime() (base.LedgerTime, bool) {
	if !seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs) {
		return base.NilLedgerTime, false
	}

	nowis := ledger.TimeNow()
	nextBoundary := nowis.NextSlotBoundary()
	libNextSlot := ledger.L(nextBoundary.Slot)

	// near slot end: switch to branch target
	if base.DiffTicks(nextBoundary, nowis) < int64(libNextSlot.PreBranchConsolidationTicks) {
		return nextBoundary, true
	}

	var target base.LedgerTime

	if seq.lastSubmittedTs.IsSlotBoundary() {
		// right after branch: ASAP at post-branch consolidation boundary
		target = seq.lastSubmittedTs.AddTicks(int(libNextSlot.PostBranchConsolidationTicks))
	} else {
		// regular: targetIntervalTicks after last submission
		target = seq.lastSubmittedTs.AddTicks(targetIntervalTicks)
	}

	// ensure pace constraint and target is not in the past
	paceMin := seq.lastSubmittedTs.AddTicks(int(libNextSlot.TransactionPaceSequencer))
	target = base.MaximumTime(target, paceMin)
	target = base.MaximumTime(target, nowis.AddTicks(1))

	// ensure we're not in pre-branch consolidation zone
	if uint8(target.Tick) < libNextSlot.PostBranchConsolidationTicks && target.Slot == nextBoundary.Slot {
		target = base.T(target.Slot, libNextSlot.PostBranchConsolidationTicks)
	}

	// don't overshoot into next slot — produce branch instead
	if !target.Before(nextBoundary) {
		return nextBoundary, true
	}

	return target, true
}

// submitMilestone sends a milestone to the network fire-and-forget and advances lastSubmittedTs optimistically.
func (seq *Sequencer) submitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, targetTs base.LedgerTime) {
	if !seq.decideSubmitMilestone(tx, meta) {
		seq.lastSubmittedTs = targetTs
		return
	}

	// fire-and-forget: send to input queue and advance optimistically
	seq.OwnSequencerMilestoneIn(tx.Bytes(), meta, tx.ID())
	seq.lastSubmittedTs = tx.Timestamp()

	if targetTs.IsSlotBoundary() {
		seq.Log().Infof("SLOT STATS: %s, budget: %d/%d", seq.slotData.Lines().Join(", "), seq.budgetLevel, maxBudgetLevel)
	}
}

// milestoneWatcher polls the tippool for own milestones and calls onMilestoneConfirmed.
func (seq *Sequencer) milestoneWatcher() {
	ticker := time.NewTicker(milestoneWatchInterval)
	defer ticker.Stop()

	var lastSeen base.TransactionID
	for {
		select {
		case <-seq.Ctx().Done():
			return
		case <-ticker.C:
		}
		vid := seq.GetLatestMilestone(seq.sequencerID)
		if vid == nil || vid.ID() == lastSeen {
			continue
		}
		lastSeen = vid.ID()
		seq.onMilestoneConfirmed(vid)
	}
}
