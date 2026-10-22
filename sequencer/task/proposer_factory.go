// Factory proposer: reads skeletons from the TransactionSkeletonFactory, inserts tag-along
// and delegation inputs, then proposes the result.
//
// The factory runs as a persistent goroutine at the sequencer level, continuously producing
// skeletons with non-decreasing coverage. The factory proposer drains the factory output,
// keeping the best skeleton. It computes the effective timestamp from the skeleton's lower
// bound and the target, inserts inputs, and finalizes the proposal.
//
// Timestamp logic:
//   - lowerBound = skeleton.TimestampLowerBound() (after inputs are inserted)
//   - effectiveTs = max(targetTs, lowerBound)
//   - both must be on the same slot; if not, the skeleton is discarded

package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

const TraceTagFactoryProposer = "propose-f0"

// tryFactoryProposal drains the best skeleton from the factory and builds a proposal.
// Returns nil if no usable skeleton is available.
func (t *taskData) tryFactoryProposal() *finalProposal {
	f := t.SkeletonFactory()
	if f == nil {
		return nil
	}

	// drain all available skeletons from factory, keep the best one
	var bestSkeleton *attacher.IncrementalAttacher
	var bestCoverage uint64
	ownLatest := t.OwnLatestMilestoneOutput().VID

	for {
		select {
		case sk, ok := <-f.OutCh():
			if !ok {
				goto done
			}
			if extendsSupersededOwnMilestone(sk.IncrementalAttacher, ownLatest, t.targetTs.Slot) ||
				reanchorsInPreBranchZone(sk.IncrementalAttacher, t.targetTs) {
				sk.Close()
				continue
			}
			if sk.Coverage >= bestCoverage {
				if bestSkeleton != nil {
					bestSkeleton.Close()
				}
				bestSkeleton = sk.IncrementalAttacher
				bestCoverage = sk.Coverage
			} else {
				sk.Close()
			}

		case <-t.ctx.Done():
			// target deadline reached — use what we have
			goto done

		default:
			// no more skeletons available right now
			goto done
		}
	}

done:
	if bestSkeleton == nil {
		return nil
	}

	if !bestSkeleton.Completed() {
		bestSkeleton.Close()
		return nil
	}

	// compute the effective timestamp: max(targetTs, skeleton lower bound)
	lowerBound := bestSkeleton.TimestampLowerBound()

	// check slot consistency: skeleton and target must be on the same slot
	if lowerBound.Slot != t.targetTs.Slot {
		t.Tracef(TraceTagFactoryProposer, "skeleton slot %d != target slot %d, discarding",
			lowerBound.Slot, t.targetTs.Slot)
		bestSkeleton.Close()
		return nil
	}

	// endorsements must be in the target slot too: a skeleton built for the previous slot can
	// extend an output of this one and pass the check above, and the ledger refuses
	// cross-slot endorsements
	for _, e := range bestSkeleton.Endorsing() {
		if e.Slot() != t.targetTs.Slot {
			t.Tracef(TraceTagFactoryProposer, "skeleton endorses %s outside target slot %d, discarding",
				e.IDShortString(), t.targetTs.Slot)
			bestSkeleton.Close()
			return nil
		}
	}

	effectiveTs := base.MaximumTime(t.targetTs, lowerBound)

	t.Tracef(TraceTagFactoryProposer, "skeleton %s, coverage=%d, endorsements=%d, lowerBound=%s, effectiveTs=%s",
		bestSkeleton.Name(), bestCoverage, len(bestSkeleton.Endorsing()), lowerBound.String(), effectiveTs.String())

	prop, err := t.newProposalWithTimestamp(bestSkeleton, effectiveTs)
	if err != nil {
		t.Tracef(TraceTagFactoryProposer, "failed to create proposal: %v", err)
		return nil
	}

	// insert tag-along and delegation inputs unless in pre-branch consolidation zone
	if !ledger.L(effectiveTs.Slot).IsPreBranchConsolidationTimestamp(effectiveTs) {
		prop.insertInputs()
	}

	// after inserting inputs, recompute the lower bound — new inputs may push it forward
	newLowerBound := prop.TimestampLowerBound()
	if newLowerBound.Slot != effectiveTs.Slot {
		t.Tracef(TraceTagFactoryProposer, "after inputs: lower bound slot %d != effective slot %d, discarding",
			newLowerBound.Slot, effectiveTs.Slot)
		prop.Close()
		return nil
	}

	// update effective timestamp if inputs pushed the lower bound forward
	if newLowerBound.After(effectiveTs) {
		effectiveTs = newLowerBound
		prop.SeqTxBuilder.SetTimestamp(effectiveTs)
	}

	// store effective timestamp on the proposal so finalize() uses it
	prop.effectiveTs = effectiveTs

	if !prop.Completed() {
		prop.Close()
		return nil
	}

	fp, err := prop.finalize("factory")
	if err != nil {
		t.logFinalizeFailure("tryFactoryProposal-"+t.Name, err)
		return nil
	}
	return fp
}

// extendsSupersededOwnMilestone tells whether a skeleton extends an own milestone of the target
// slot which a newer own milestone has already extended. Skeletons wait in the factory's output
// channel, and one built on the previous head can still be the heaviest: taking it gives that
// head a second successor, a fork of the own chain which makes the two branches of it conflict
// in every past cone holding both. Extending an own output of an earlier slot is left alone:
// that is how the sequencer re-anchors when it moves to another lineage.
func extendsSupersededOwnMilestone(sk *attacher.IncrementalAttacher, ownLatest *vertex.WrappedTx, targetSlot uint32) bool {
	if ownLatest == nil {
		return false
	}
	ext := sk.Extending().VID
	return ext != nil && ext != ownLatest && ext.Slot() == targetSlot
}

// reanchorsInPreBranchZone tells whether a skeleton extends an own output of an earlier slot at
// a target where the ledger requires a chain predecessor of the same slot (the pre-branch
// consolidation zone, from library upgrade 2 on): such a milestone would be refused.
func reanchorsInPreBranchZone(sk *attacher.IncrementalAttacher, targetTs base.LedgerTime) bool {
	if !ledger.L(targetTs.Slot).RequiresSameSlotChainPredecessor(targetTs) {
		return false
	}
	ext := sk.Extending().VID
	return ext == nil || ext.Slot() != targetTs.Slot
}
