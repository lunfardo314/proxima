package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer just consumes fee outputs. It also generates branches

const BaseProposerName = "BASE"

type baseProposer struct {
	proposerTaskGeneric
}

func init() {
	registerProposingStrategy(BaseProposerName, func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask {
		ret := &baseProposer{newProposerGeneric(mf, targetTs, BaseProposerName)}
		ret.trace("created..")
		return ret
	})
}

func (b *baseProposer) run() {
	startTime := time.Now()
	var tx *state.Transaction
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

func (b *baseProposer) proposeBase() (*state.Transaction, bool) {
	latestMilestone := b.factory.getLastMilestone()
	if b.targetTs.TimeTick() != 0 && latestMilestone.TimeSlot() != b.targetTs.TimeSlot() {
		// cross epoch. Skip
		return nil, true
	}

	if b.targetTs.TimeTick() == 0 {
		// generate branch, no fee outputs are consumed
		return b.makeMilestone(&latestMilestone, latestMilestone.VID.BaseStemOutput(), nil, nil), false
	}
	// non-branch
	targetDelta, conflict, _ := latestMilestone.VID.StartNextSequencerMilestoneDelta()
	util.Assertf(conflict == nil, "conflict == nil")
	util.Assertf(targetDelta != nil, "latest milestone is orphaned: %s", latestMilestone.VID.IDShort())
	feeOutputsToConsume := b.factory.selectFeeInputs(targetDelta, b.targetTs)

	return b.makeMilestone(&latestMilestone, nil, feeOutputsToConsume, nil), false
}
