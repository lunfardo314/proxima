package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const BaseProposerName = "base"

type baseProposer struct {
	proposerTaskGeneric
}

func init() {
	registerProposingStrategy(BaseProposerName, func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask {
		ret := &baseProposer{newProposerGeneric(mf, targetTs, BaseProposerName)}

		//if !targetTs.IsSlotBoundary() {
		//	ret.setTraceNAhead(100)
		//}
		return ret
	})
}

func (b *baseProposer) run() {
	startTime := time.Now()
	var tx *transaction.Transaction
	var forceExit bool
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		b.startProposingTime()
		tx, forceExit = b.proposeBase()

		if forceExit {
			break
		}
		if tx != nil {
			b.trace("generated %s", tx.IDShort())
			b.assessAndAcceptProposal(tx, startTime, b.name())
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (b *baseProposer) proposeBase() (*transaction.Transaction, bool) {
	latestMilestone := b.factory.getLatestMilestone()
	if latestMilestone.VID == nil {
		// startup situation
		if !b.targetTs.IsSlotBoundary() {
			// can only start up with branch target
			b.trace(" no latest own milestones to extend has been found. Postpone until branch target")
			return nil, true
		}
		// start-up: find own output and stem in the state and create branch with it
		seqOut, stemOut, found := b.factory.tangle.GetSequencerBootstrapOutputs(b.factory.tipPool.chainID)
		if !found {
			b.factory.log.Errorf("cannot find bootstrap outputs for the sequencer")
			return nil, true
		}
		// create branch. In case it is the only sequencer around, the branch will survive
		// and will help to bootstrap other sequencers. Otherwise, it will be orphaned most likely
		return b.makeMilestone(&seqOut, &stemOut, nil, nil), false
	}
	// own latest milestone exists
	if !b.targetTs.IsSlotBoundary() && latestMilestone.TimeSlot() != b.targetTs.TimeSlot() {
		b.trace("proposeBase.force exit: cross-slot %s", latestMilestone.IDShort())
		return nil, true
	}

	if b.targetTs.TimeTick() == 0 {
		b.trace("making branch, extending %s", latestMilestone.IDShort())
		// generate branch, no fee outputs are consumed
		baseStem := latestMilestone.VID.BaseStemOutput()
		if baseStem == nil {
			// base stem is not available for a milestone which is virtual and non-branch
			b.factory.log.Errorf("proposeBase.force exit: stem not available, %s cannot be extended to a branch", latestMilestone.IDShort())
			return nil, true
		}
		// create branch
		return b.makeMilestone(&latestMilestone, baseStem, nil, nil), false
	}
	// non-branch
	targetDelta, conflict, _ := latestMilestone.VID.StartNextSequencerMilestoneDelta()

	util.Assertf(conflict == nil, "conflict == nil")
	util.Assertf(targetDelta != nil, "latest milestone is orphaned: %s", latestMilestone.VID.IDShort())
	feeOutputsToConsume := b.factory.selectFeeInputs(targetDelta, b.targetTs)

	b.trace("making ordinary milestone")
	return b.makeMilestone(&latestMilestone, nil, feeOutputsToConsume, nil), false
}
