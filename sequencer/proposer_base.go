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
			b.storeProposalDuration()
			break
		}
		if tx != nil {
			b.trace("generated %s", func() any { return tx.IDShort() })
			b.assessAndAcceptProposal(tx, startTime, b.name())
		}
		b.storeProposalDuration()
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *baseProposer) proposeBase() (*transaction.Transaction, bool) {
	latestMilestone := b.factory.getLatestMilestone()
	// own latest milestone exists
	if !b.targetTs.IsSlotBoundary() && latestMilestone.TimeSlot() != b.targetTs.TimeSlot() {
		// on startup or cross-slot will only produce branches
		b.trace("proposeBase.force exit: cross-slot %s", latestMilestone.IDShort())
		return nil, true
	}

	if b.targetTs.TimeTick() == 0 {
		b.trace("making branch, extending %s", latestMilestone.IDShort())
		// generate branch, no fee outputs are consumed
		baseStem := latestMilestone.VID.BaseStemOutput(b.factory.tangle)
		if baseStem == nil {
			// base stem is not available for a milestone which is virtual and non-branch
			b.factory.log.Errorf("proposeBase.force exit: stem not available, %s cannot be extended to a branch", latestMilestone.IDShort())
			return nil, true
		}
		// create branch
		return b.makeMilestone(&latestMilestone, baseStem, nil, nil), false
	}
	// non-branch

	feeOutputsToConsume, conflict := b.selectFeeInputs(latestMilestone.VID)
	util.Assertf(conflict == nil, "unexpected conflict")

	b.trace("making ordinary milestone")
	return b.makeMilestone(&latestMilestone, nil, feeOutputsToConsume, nil), false
}
