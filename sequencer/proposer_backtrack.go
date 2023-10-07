package sequencer

import (
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
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

	endorseSeqID := b.endorse.MustSequencerID()
	b.trace("RUN: preliminary extension choices in the endorsement target %s (ms %s):\n%s",
		endorseSeqID.VeryShort(), b.endorse.IDShort(), milestoneSliceString(b.extensionChoices))

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

	endorseSeqID := b.endorse.MustSequencerID()
	b.trace("trying to extend %s with endorsement target %s (ms %s)",
		extend.IDShort(), endorseSeqID.VeryShort(), b.endorse.IDShort())

	targetDelta, conflict, consumer := b.endorse.StartNextSequencerMilestoneDelta(extend.VID)
	if conflict != nil {
		b.trace("CANNOT extend %s with endorsement target %s due to %s (consumer %s)",
			extend.IDShort(), b.endorse.IDShort(), conflict.DecodeID().Short(), consumer.IDShort())
		return nil
	}
	if targetDelta == nil {
		b.trace("CANNOT generate candidate: %s or %s has been orphaned", extend.IDShort(), b.endorse.IDShort())
		return nil
	}

	if !targetDelta.CanBeConsumed(extend, func() (ret multistate.SugaredStateReader) {
		var ok bool
		bb := targetDelta.BaselineBranch()
		if bb != nil {
			ret, ok = b.factory.tangle.StateReaderOfSequencerMilestone(bb)
			util.Assertf(ok, "can't get state for the baseline branch %s", bb.IDShort())
		} else {
			// TODO if d belongs to a branch, then we must check the branch. Ugly solution, refactor
			ret = b.factory.tangle.MustGetBranchState(b.endorse)
		}
		return
	}) {
		// past cones are not conflicting but the output itself is already consumed
		b.trace("CANNOT extend %s (is already consumed) with endorsement target %s (ms %s)",
			extend.IDShort(), endorseSeqID.VeryShort(), b.endorse.IDShort())
		return nil
	}

	b.trace("CAN extend %s with endorsement target %s", extend.IDShort(), b.endorse.IDShort())

	feeOutputsToConsume := b.factory.selectFeeInputs(targetDelta, b.targetTs)
	return b.makeMilestone(&extend, nil, feeOutputsToConsume, util.List(b.endorse))

}

func (b *backtrackProposer) calcExtensionChoices() {
	for {
		endorsable := b.factory.tipPool.preSelectAndSortEndorsableMilestones(b.targetTs)
		b.trace("preselected %d milestones", len(endorsable))
		// assumed order is descending ledger coverage
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
