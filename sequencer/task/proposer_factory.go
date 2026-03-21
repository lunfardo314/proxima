// f0 proposer: reads skeletons from the TransactionSkeletonFactory, inserts tag-along
// and delegation inputs, then proposes the result. Replaces e1/e2/e3/r2/r3 strategies.
//
// The factory runs as a persistent goroutine at the sequencer level, continuously producing
// skeletons with non-decreasing coverage. The f0 proposer drains the factory output,
// keeping the best skeleton. When the target deadline arrives, it takes the best skeleton,
// computes the effective timestamp from the skeleton's lower bound and the target,
// inserts inputs, and proposes it.
//
// Timestamp logic:
//   - lowerBound = skeleton.TimestampLowerBound() (after inputs are inserted)
//   - effectiveTs = max(targetTs, lowerBound)
//   - both must be on the same slot; if not, the skeleton is discarded

package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

const TraceTagFactoryProposer = "propose-f0"

func init() {
	registerProposerStrategy(&proposerStrategy{
		Name:             "factory0",
		ShortName:        "f0",
		GenerateProposal: factoryProposalGenerator,
	})
}

func factoryProposalGenerator(p *proposer) (*proposal, bool) {
	if p.targetTs.IsSlotBoundary() {
		// f0 does not generate branch transactions (b0 handles those)
		return nil, true
	}

	f := p.SkeletonFactory()
	if f == nil {
		return nil, true
	}

	// drain all available skeletons from factory, keep the best one
	var bestSkeleton *attacher.IncrementalAttacher
	var bestCoverage uint64

	for {
		select {
		case sk, ok := <-f.OutCh():
			if !ok {
				goto done
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

		case <-p.ctx.Done():
			// target deadline reached — use what we have
			goto done

		default:
			// no more skeletons available right now
			goto done
		}
	}

done:
	if bestSkeleton == nil {
		return nil, false
	}

	if !bestSkeleton.Completed() {
		bestSkeleton.Close()
		return nil, false
	}

	// compute the effective timestamp: max(targetTs, skeleton lower bound)
	lowerBound := bestSkeleton.TimestampLowerBound()

	// check slot consistency: skeleton and target must be on the same slot
	if lowerBound.Slot != p.targetTs.Slot {
		p.Tracef(TraceTagFactoryProposer, "skeleton slot %d != target slot %d, discarding",
			lowerBound.Slot, p.targetTs.Slot)
		bestSkeleton.Close()
		return nil, false
	}

	effectiveTs := base.MaximumTime(p.targetTs, lowerBound)

	p.Tracef(TraceTagFactoryProposer, "skeleton %s, coverage=%d, endorsements=%d, lowerBound=%s, effectiveTs=%s",
		bestSkeleton.Name(), bestCoverage, len(bestSkeleton.Endorsing()), lowerBound.String(), effectiveTs.String())

	ret, err := p.newProposalWithTimestamp(bestSkeleton, effectiveTs)
	if err != nil {
		p.Tracef(TraceTagFactoryProposer, "failed to create proposal: %v", err)
		return nil, false
	}

	// insert tag-along and delegation inputs unless in pre-branch consolidation zone
	if !ledger.L(effectiveTs.Slot).IsPreBranchConsolidationTimestamp(effectiveTs) {
		ret.insertInputs()
	}

	// after inserting inputs, recompute the lower bound — new inputs may push it forward
	newLowerBound := ret.TimestampLowerBound()
	if newLowerBound.Slot != effectiveTs.Slot {
		p.Tracef(TraceTagFactoryProposer, "after inputs: lower bound slot %d != effective slot %d, discarding",
			newLowerBound.Slot, effectiveTs.Slot)
		ret.Close()
		return nil, false
	}

	// update effective timestamp if inputs pushed the lower bound forward
	if newLowerBound.After(effectiveTs) {
		effectiveTs = newLowerBound
		ret.SeqTxBuilder.TransactionData.Timestamp = effectiveTs
	}

	// store effective timestamp on the proposal so propose() uses it
	ret.effectiveTs = effectiveTs

	if !ret.Completed() {
		ret.Close()
		return nil, false
	}

	// force exit: f0 produces one proposal per target (the best available skeleton)
	return ret, true
}
