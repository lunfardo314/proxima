package sequencer

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/task"
	"github.com/lunfardo314/proxima/util"
)

const (
	// plateauHoldTicks is the number of ticks the combined coverage signal (factory skeleton +
	// tag-along backlog) must remain stable before submitting.
	// The final coverage includes tag-alongs and freezes inserted by task.Run at submission time.
	// Plateau detection defers submission while endorsements or tag-alongs are still arriving,
	// because both increase coverage.
	plateauHoldTicks = 3

	// targetOffsetTicks controls how far ahead of "now" the milestone's timestamp is set.
	// This determines the minimum spacing between milestones (along with TransactionPaceSequencer).
	// Smaller values = more milestones per slot = faster coverage convergence.
	// The target offset is decoupled from the build budget: task.Run uses a separate
	// buildBudget (wall-clock duration) that can be longer than the target offset.
	targetOffsetTicks = 6

	// milestoneWatchInterval is how often the background watcher polls the tippool
	milestoneWatchInterval = 20 * time.Millisecond
)

// doSequencerSlot runs one iteration of the sequencer loop.
// Polls the factory for coverage plateaus (considering both skeleton coverage and
// tag-along backlog), submits milestones when the combined signal stabilizes,
// and generates a branch at the slot edge.
// Returns false if the sequencer should stop.
func (seq *Sequencer) doSequencerSlot() bool {
	// pause during snapshot
	if seq.IsSnapshotting() {
		seq.log.Infof("sequencer paused: snapshot in progress")
		seq.RepeatSync(2*time.Second, func() bool {
			return seq.IsSnapshotting()
		})
		seq.log.Infof("sequencer resumed: snapshot finished")
	}

	if seq.config.MaxBranches != 0 && seq.branchCount >= seq.config.MaxBranches {
		seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
		return false
	}

	// wait for clock to catch up with last submission
	if !seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs) {
		return false
	}

	tickDuration := ledger.TickDuration()
	holdDuration := time.Duration(plateauHoldTicks) * tickDuration

	// Drain interval: space drain milestones evenly across the slot.
	// drainRate tag-alongs / slot, batchSize per milestone → drainRate/batchSize milestones/slot.
	// Usable ticks ≈ 128 - PostBranch - PreBranch.
	lib0 := ledger.L(0)
	usableTicks := int(base.MaxTickValue) - int(lib0.PostBranchConsolidationTicks) - int(lib0.PreBranchConsolidationTicks)
	if usableTicks < 1 {
		usableTicks = 1
	}
	drainIntervalTicks := usableTicks * seq.config.MaxTagAlongInputs / seq.config.TagAlongDrainRate
	if drainIntervalTicks < 1 {
		drainIntervalTicks = 1
	}
	drainInterval := time.Duration(drainIntervalTicks) * tickDuration

	var lastSeenCoverage uint64
	lastImprovementTime := time.Now()
	lastBacklogCheck := time.Now()
	lastDrainTime := time.Time{} // zero: first drain fires immediately

	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	for {
		select {
		case <-seq.Ctx().Done():
			return false
		case <-ticker.C:
		}

		// check context before accessing ledger (avoids panic during test teardown)
		if seq.Ctx().Err() != nil {
			return false
		}

		nowTs := ledger.TimeNow()
		nextBoundary := nowTs.NextSlotBoundary()
		lib := ledger.L(nextBoundary.Slot)
		currentSlot := nowTs.Slot

		// check for max target ts (testing only)
		if seq.config.MaxTargetTs != base.NilLedgerTime && nowTs.After(seq.config.MaxTargetTs) {
			seq.log.Infof("current time %s is after maximum ts %s -> stopping", nowTs, seq.config.MaxTargetTs)
			return false
		}

		// The factory targets the current slot. Non-branch milestones are built
		// within the current slot; the branch at the slot edge transitions to the next.
		if seq.skeletonFactory != nil {
			seq.skeletonFactory.SetTargetSlot(currentSlot)
		}

		// ensure slotData
		if seq.slotData == nil || seq.slotData.Slot() != currentSlot {
			seq.slotData = task.NewSlotData(currentSlot)
		}

		ticksToSlotEnd := base.DiffTicks(nextBoundary, nowTs)

		// --- Branch time: within PreBranchConsolidationTicks of slot edge ---
		if ticksToSlotEnd < int64(lib.PreBranchConsolidationTicks) {
			// submit if there's an unused skeleton before switching to branch
			if seq.skeletonFactory != nil && seq.skeletonFactory.BestCoverage() > lastSeenCoverage {
				seq.tryBuildAndSubmit()
			}
			return seq.generateAndSubmitBranch(nextBoundary)
		}

		// --- Zone A: post-branch consolidation ---
		// Wait for branches from this slot to propagate via gossip.
		// The factory starts building skeletons at tick 0 (SetTargetSlot above).
		// By tick 12, it should have at least one skeleton with an endorsement
		// (extend own milestone + endorse a peer's branch from this slot).
		// If the branch arrives later, the first endorsed milestone is simply delayed.
		// The bootstrap case (no branches at all) is handled by tryBootProposal.
		if nowTs.Tick < lib.PostBranchConsolidationTicks {
			continue
		}

		// --- Zone B: active polling with plateau detection ---
		if seq.skeletonFactory == nil {
			continue
		}

		bestCoverage := seq.skeletonFactory.BestCoverage()

		// Priority 1: backlog drain — batch tag-alongs into milestones at a controlled rate.
		// Each milestone picks up to MaxTagAlongInputs. Drain submissions are spaced by
		// drainInterval (derived from TagAlongDrainRate and MaxTagAlongInputs) to avoid
		// flooding the DAG with low-payload milestones under sustained spam.
		if seq.backlog.NumOutputsInBuffer() > 0 && time.Since(lastDrainTime) >= drainInterval {
			if seq.tryBuildAndSubmit() {
				lastDrainTime = time.Now()
				lastSeenCoverage = seq.skeletonFactory.BestCoverage()
				lastImprovementTime = time.Now()
				lastBacklogCheck = time.Now()
			}
			continue
		}

		// Priority 2: plateau detection — wait for endorsement coverage to stabilize.
		// Only active when backlog is empty (all tag-alongs consumed).
		backlogChanged := seq.backlog.ArrivedOutputsSince(lastBacklogCheck)

		improved := false
		if bestCoverage > lastSeenCoverage {
			lastSeenCoverage = bestCoverage
			improved = true
		}
		if backlogChanged {
			lastBacklogCheck = time.Now()
			improved = true
		}

		if improved {
			lastImprovementTime = time.Now()
			continue
		}

		// check for plateau
		if time.Since(lastImprovementTime) < holdDuration {
			continue
		}

		// plateau detected: try to submit.
		// The factory provides skeletons with endorsements (coverage improvement).
		// Base extend is the fallback — useful for tag-along drain and bootstrap recovery.
		seq.tryBuildAndSubmit()
		lastSeenCoverage = seq.skeletonFactory.BestCoverage()
		lastImprovementTime = time.Now()
		lastBacklogCheck = time.Now()
	}
}

// tryBuildAndSubmit computes an effective timestamp from "now", builds a milestone
// via task.Run (which inserts tag-alongs and freezes on top of the skeleton), and submits it.
// Returns true on successful submission.
//
// Timing: targetTs is set targetOffsetTicks ahead of "now". This determines the
// milestone's ledger timestamp (logical clock). The build budget (wall-clock time for
// task.Run) is separate and configured via buildBudget. The target can be close to
// "now" for fast pace, while the builder has enough time for I/O-heavy operations.
func (seq *Sequencer) tryBuildAndSubmit() bool {
	nowTs := ledger.TimeNow()
	lib := ledger.L(nowTs.Slot)
	paceMin := seq.lastSubmittedTs.AddTicks(int(lib.TransactionPaceSequencer))

	var targetTs base.LedgerTime
	if seq.lastSubmittedTs.IsSlotBoundary() {
		// First milestone after branch (seed): target exactly at post-branch consolidation boundary.
		// No offset needed — PostBranchConsolidationTicks already provides the buffer.
		targetTs = base.MaximumTime(base.T(nowTs.Slot, lib.PostBranchConsolidationTicks), paceMin)
	} else {
		// Subsequent milestones: target = max(now + targetOffsetTicks, paceMin).
		// targetOffsetTicks determines the milestone's timestamp freshness.
		// buildBudget (in task.Run) determines how long the builder has to complete.
		targetTs = base.MaximumTime(nowTs.AddTicks(targetOffsetTicks), paceMin)
	}

	// don't overshoot into next slot
	nextBoundary := nowTs.NextSlotBoundary()
	if !targetTs.Before(nextBoundary) {
		return false
	}

	// must not be a slot boundary (branches handled separately)
	if targetTs.IsSlotBoundary() {
		return false
	}

	if !ledger.ValidSequencerPace(seq.lastSubmittedTs, targetTs) {
		return false
	}

	seq.newTargetSet()
	seq.slotData.NewTarget()

	msTx, meta, _, err := seq.generateMilestoneForTarget(targetTs)

	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.slotData.NotGoodEnough()
		return false
	case errors.Is(err, task.ErrNoProposals):
		seq.slotData.NoProposals()
		return false
	case err != nil:
		return false
	}
	util.Assertf(msTx != nil, "msTx != nil")

	meta.TxBytesReceived = util.Ref(time.Now())
	seq.submitMilestone(msTx, meta, targetTs)
	seq.adjustBudget(true)
	return true
}

// generateAndSubmitBranch generates and submits a branch transaction for the slot boundary.
// Does NOT wait for the boundary — the boundary time serves as the context deadline,
// giving ~PreBranchConsolidationTicks of time for branch generation.
// Returns true to continue the sequencer loop, false to stop.
func (seq *Sequencer) generateAndSubmitBranch(branchTs base.LedgerTime) bool {
	if seq.config.MaxTargetTs != base.NilLedgerTime && branchTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("branch target %s is after maximum ts %s -> stopping", branchTs, seq.config.MaxTargetTs)
		return false
	}

	seq.newTargetSet()
	if seq.slotData == nil {
		seq.slotData = task.NewSlotData(branchTs.Slot)
	}
	seq.slotData.NewTarget()

	msTx, meta, _, err := seq.generateMilestoneForTarget(branchTs)

	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.slotData.NotGoodEnough()
	case errors.Is(err, task.ErrNoProposals):
		seq.slotData.NoProposals()
		seq.adjustBudget(false)
	case err != nil:
		seq.adjustBudget(false)
		seq.Log().Warnf("branch generation: %v (budget: %d/%d)", err, seq.budgetLevel, maxBudgetLevel)
	default:
		util.Assertf(msTx != nil, "msTx != nil")
		meta.TxBytesReceived = util.Ref(time.Now())
		seq.submitMilestone(msTx, meta, branchTs)
		seq.adjustBudget(true)
	}

	seq.Log().Infof("SLOT STATS: %s, budget: %d/%d", seq.slotData.Lines().Join(", "), seq.budgetLevel, maxBudgetLevel)
	seq.slotData = nil

	// advance lastSubmittedTs past the branch boundary even on failure,
	// so the next doSequencerSlot iteration starts at the next slot, not this one
	if branchTs.After(seq.lastSubmittedTs) {
		seq.lastSubmittedTs = branchTs
	}
	return true
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
