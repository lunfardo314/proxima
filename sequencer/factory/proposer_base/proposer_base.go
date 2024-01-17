package proposer_base

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const BaseProposerName = "base"

type BaseProposer struct {
	proposer_generic.TaskGeneric
}

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name: BaseProposerName,
		Constructor: func(generic *proposer_generic.TaskGeneric) proposer_generic.Task {
			ret := &BaseProposer{TaskGeneric: *generic}
			ret.WithProposalGenerator(func() (*attacher.IncrementalAttacher, bool) {
				return ret.propose()
			})
			return ret
		},
	}
}

func (b *BaseProposer) propose() (*attacher.IncrementalAttacher, bool) {
	extend := b.OwnLatestMilestone()

	b.TraceLocal("propose: extending %s", extend.IDShortString)
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		// target is not a branch target
		b.TraceLocal("propose: target is not a branch target")
		if extend.Slot() != b.TargetTs.Slot() {
			b.TraceLocal("propose.force exit: cross-slot %s", extend.IDShortString)
			return nil, true
		}
		b.TraceLocal("propose: target is not a branch and it is on the same slot")
		if !extend.IsSequencerMilestone() {
			b.TraceLocal("propose.force exit: not-sequencer %s", extend.IDShortString)
			return nil, true
		}
	}
	b.TraceLocal("propose: predecessor %s is sequencer", extend.IDShortString)

	a, err := attacher.NewIncrementalAttacher(b.Name, b, b.TargetTs, extend)
	if err != nil {
		b.Log().Warnf("proposer %s: can't create attacher: '%v'", b.Name, err)
		return nil, true
	}
	b.TraceLocal("propose: created attacher with baseline %s", a.BaselineBranch().IDShortString)

	if b.TargetTs.Tick() != 0 {
		b.TraceLocal("propose: making non-branch, extending %s, collecting and inserting tag-along inputs", extend.IDShortString)
		numInserted := b.AttachTagAlongInputs(a)
		b.TraceLocal("propose: inserted %d tag-along inputs", numInserted)
	} else {
		b.TraceLocal("propose: making branch, extending %s, no tag-along", extend.IDShortString)
	}
	return a, false
}
