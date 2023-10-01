package sequencer

import (
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

// Deprecate
type backtrackProposer struct {
	proposerTaskGeneric
	endorse          *utangle.WrappedTx
	extensionChoices []utangle.WrappedOutput
}

const BacktrackProposerName = "btrack"

func init() {
	registerProposingStrategy(BacktrackProposerName, func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask {
		if targetTs.TimeTick() == 0 {
			// doesn't propose branches
			return nil
		}
		ret := &backtrackProposer{
			proposerTaskGeneric: newProposerGeneric(mf, targetTs, BacktrackProposerName),
		}
		ret.trace("STARTING")
		return ret
	})
}

func milestoneSliceString(path []utangle.WrappedOutput) string {
	ret := make([]string, 0)
	for _, md := range path {
		ret = append(ret, "       "+md.IDShort())
	}
	return strings.Join(ret, "\n")
}

func (b *backtrackProposer) run() {
	startTime := time.Now()
	b.calcExtensionChoices()
	if len(b.extensionChoices) == 0 {
		b.trace("EXIT: didn't find another milestone to endorse")
		return
	}

	b.trace("RUNNING with %s to endorse:", b.endorse.IDShort())
	b.trace("own forks in another ms:\n%s", milestoneSliceString(b.extensionChoices))

	for _, ms := range b.extensionChoices {
		if !b.factory.proposal.continueCandidateProposing(b.targetTs) {
			return
		}

		b.startProposingTime()
		if tx := b.generateCandidate(ms); tx != nil {
			b.assessAndAcceptProposal(tx, startTime, b.name())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *backtrackProposer) generateCandidate(extend utangle.WrappedOutput) *transaction.Transaction {
	util.Assertf(extend.VID != b.endorse, "extend.VID != b.endorse")
	if extend.VID.IsBranchTransaction() && b.endorse.IsBranchTransaction() {
		// cannot extend one branch and endorse another TODO
		return nil
	}

	b.trace("trying extend %s to endorse %s", extend.VID.IDShort(), b.endorse.IDShort())

	targetDelta, conflict, consumer := b.endorse.StartNextSequencerMilestoneDelta(extend.VID)
	if conflict != nil {
		b.trace("CANNOT extend %s to endorse: %s due to %s (consumer %s)",
			extend.VID.IDShort(), b.endorse.IDShort(), conflict.DecodeID().Short(), consumer.IDShort())
		return nil
	}
	if targetDelta == nil {
		b.trace("CANNOT generate candidate: %s or %s has been orphaned", extend.VID.IDShort(), b.endorse.IDShort())
		return nil
	}
	if !targetDelta.CanBeConsumedBySequencer(extend, b.factory.tangle) {
		// past cones are not conflicting but the output itself is already consumed
		b.trace("CANNOT extend %s (is already consumed) to endorse: %s", extend.VID.IDShort(), b.endorse.IDShort())
		return nil
	}

	b.trace("CAN extend %s to endorse: %s", extend.VID.IDShort(), b.endorse.IDShort())

	feeOutputsToConsume := b.factory.selectFeeInputs(targetDelta, b.targetTs)
	return b.makeMilestone(&extend, nil, feeOutputsToConsume, util.List(b.endorse))

}

func (b *backtrackProposer) calcExtensionChoices() {
	for {
		endorsable := b.factory.tipPool.preSelectEndorsableMilestones(b.targetTs)
		b.trace("preselected %d milestones", len(endorsable))
		for _, vid := range endorsable {
			b.extensionChoices = b.factory.ownForksInAnotherSequencerPastCone(vid, b)
			if len(b.extensionChoices) > 0 {
				b.endorse = vid
				break
			}
		}
		if len(b.extensionChoices) > 0 {
			break
		}
		if !b.factory.proposal.continueCandidateProposing(b.targetTs) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
