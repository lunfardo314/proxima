package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

// Deprecate
type backtrackProposer2 struct {
	proposerTaskGeneric
}

const BacktrackProposer2Name = "btrack2"

func init() {
	//registerProposingStrategy(BacktrackProposer2Name, func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask {
	//	if targetTs.TimeTick() == 0 {
	//		// doesn't propose branches
	//		return nil
	//	}
	//	ret := &backtrackProposer2{
	//		proposerTaskGeneric: newProposerGeneric(mf, targetTs, BacktrackProposer1Name),
	//	}
	//	ret.trace("STARTING")
	//	return ret
	//})
}

func (b *backtrackProposer2) run() {
	startTime := time.Now()
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		endorse, extensionChoices := b.calcExtensionChoices()
		if len(extensionChoices) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		util.Assertf(endorse != nil, "endorse != nil")

		b.startProposingTime()
		if tx := b.generateCandidate(extensionChoices[0], endorse); tx != nil {
			b.assessAndAcceptProposal(tx, extensionChoices[0], startTime, b.name())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *backtrackProposer2) generateCandidate(extend utangle.WrappedOutput, endorse *utangle.WrappedTx) *transaction.Transaction {
	util.Assertf(extend.VID != endorse, "extend.VID != b.endorse")

	endorseSeqID := endorse.MustSequencerID()
	b.trace("trying to extend %s with endorsement target %s (ms %s)",
		extend.IDShort(), endorseSeqID.VeryShort(), endorse.IDShort())

	feeOutputsToConsume, conflict := b.selectInputs(extend, endorse)
	if conflict != nil {
		b.trace("CANNOT extend %s with endorsement target %s due to conflict %s",
			extend.IDShort(), endorse.IDShort(), conflict.DecodeID().Short())
		return nil
	}

	return b.makeMilestone(&extend, nil, feeOutputsToConsume, util.List(endorse))
}

func (b *backtrackProposer2) calcExtensionChoices() (endorse *utangle.WrappedTx, extensionChoices []utangle.WrappedOutput) {
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		endorsable := b.factory.tipPool.preSelectAndSortEndorsableMilestones(b.targetTs)
		//b.setTraceNAhead(1)
		b.trace("preselected %d milestones", len(endorsable))
		// assumed order is descending ledger coverage
		for _, vid := range endorsable {
			extensionChoices = b.factory.ownForksInAnotherSequencerPastCone(vid, b)
			if len(extensionChoices) > 0 {
				return
			}
		}
		if len(extensionChoices) > 0 {
			return
		}
	}
	return
}