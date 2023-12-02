package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

type (
	backtrackProposer2 struct {
		proposerTaskGeneric
	}
)

const (
	BacktrackProposer2Name    = "btrack2"
	EnabledBacktrackProposer2 = true
)

func init() {
	if !EnabledBacktrackProposer2 {
		return
	}
	registerProposingStrategy(BacktrackProposer2Name, func(mf *milestoneFactory, targetTs core.LogicalTime) proposerTask {
		if targetTs.TimeTick() == 0 {
			// doesn't propose branches
			return nil
		}
		ret := &backtrackProposer2{
			proposerTaskGeneric: newProposerGeneric(mf, targetTs, BacktrackProposer2Name),
		}
		//ret.setTraceNAhead(1)
		ret.trace("STARTING")
		return ret
	})
}

func (b *backtrackProposer2) run() {
	startTime := time.Now()
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		endorse, extensionChoices := b.calcExtensionChoices()

		//b.setTraceNAhead(1)
		if len(extensionChoices) > 0 {
			b.trace("calcExtensionChoices: endorse: %s, extension choices:\n%s", endorse.IDShort(), milestoneSliceString(extensionChoices))
			b.startProposingTime()
		} else {
			b.trace("calcExtensionChoices: <empty>")
		}

		for _, extend := range extensionChoices {
			pair := extendEndorsePair{
				extend:  extend.VID,
				endorse: endorse,
			}
			if b.visited.Contains(pair) {
				continue
			}
			if tx := b.generateCandidate(extend, endorse); tx != nil {
				if forceExit := b.assessAndAcceptProposal(tx, extend, startTime, b.name()); forceExit {
					b.trace("exit proposer")
					return
				}
				b.visited.Insert(pair)
				//b.setTraceNAhead(1)
				b.trace("marked visited: extend: %s, endorse: %s", extend.IDShort(), endorse.IDShort())
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *backtrackProposer2) generateCandidate(extend utangle.WrappedOutput, endorse *utangle.WrappedTx) *transaction.Transaction {
	util.Assertf(extend.VID != endorse, "extend.VID != b.endorse")

	b.trace("trying to extend %s with endorsement target %s (ms %s)",
		func() any { return extend.IDShort() },
		func() any { endorseSeqID := endorse.MustSequencerID(); return endorseSeqID.StringVeryShort() },
		func() any { return endorse.IDShort() })

	feeOutputsToConsume, conflict := b.selectInputs(extend, endorse)
	if conflict != nil {
		b.trace("CANNOT extend %s with endorsement target %s due to conflict %s",
			extend.IDShort(), endorse.IDShort(), conflict.DecodeID().StringShort())
		return nil
	}

	return b.makeMilestone(&extend, nil, feeOutputsToConsume, util.List(endorse))
}

func (b *backtrackProposer2) calcExtensionChoices() (endorse *utangle.WrappedTx, extensionChoices []utangle.WrappedOutput) {
	for b.factory.proposal.continueCandidateProposing(b.targetTs) {
		endorsable := b.factory.tipPool.preSelectAndSortEndorsableMilestones(b.targetTs)
		//b.setTraceNAhead(1)
		b.trace("pre-selected %d endorsable milestones", len(endorsable))
		// assumed order is descending ledger coverage
		for _, vid := range endorsable {
			extensionChoices = b.extensionChoicesInEndorsementTargetPastCone(vid)
			if len(extensionChoices) > 0 {
				endorse = vid
				return
			}
		}
		if len(extensionChoices) > 0 {
			return
		}
	}
	return
}
