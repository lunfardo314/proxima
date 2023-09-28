package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer just consumes fee outputs. It also generates branches

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
	lastMs := b.factory.getLastMilestone()
	if !lastMs.VID.IsSequencerMilestone() {
		b.trace("exit. Cannot extend non-sequencer milestone %s", lastMs.IDShort())
		return
	}
	startTime := time.Now()
	var tx *transaction.Transaction
	var forceExit bool
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
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
	latestMilestone := b.factory.getLastMilestone()
	if !latestMilestone.VID.IsSequencerMilestone() {
		return nil, false
	}
	if b.targetTs.TimeTick() != 0 && latestMilestone.TimeSlot() != b.targetTs.TimeSlot() {
		// cross slot. Skip
		return nil, true
	}

	if b.targetTs.TimeTick() == 0 {
		b.trace("making branch")
		// generate branch, no fee outputs are consumed
		baseStem := latestMilestone.VID.BaseStemOutput()
		if baseStem == nil {
			// base stem is not available for a milestone which is virtual and non-branch
			b.trace("%s cannot be extended to branch", latestMilestone.IDShort())
			return nil, false
		}
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
