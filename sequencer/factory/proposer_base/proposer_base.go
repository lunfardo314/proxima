package proposer_base

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const (
	BaseProposerName = "base"
	TraceTag         = "propose-base"
)

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

	b.Tracef(TraceTag, "extending %s", extend.IDShortString)
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		// target is not a branch target
		b.Tracef(TraceTag, "target is not a branch target")
		if extend.Slot() != b.TargetTs.Slot() {
			b.Tracef(TraceTag, "force exit: cross-slot %s", extend.IDShortString)
			return nil, true
		}
		b.Tracef(TraceTag, "target is not a branch and it is on the same slot")
		if !extend.IsSequencerMilestone() {
			b.Tracef(TraceTag, "force exit: not-sequencer %s", extend.IDShortString)
			return nil, true
		}
	}
	b.Tracef(TraceTag, "predecessor %s is sequencer", extend.IDShortString)

	a, err := attacher.NewIncrementalAttacher(b.Name, b, b.TargetTs, extend)
	if err != nil {
		b.Log().Warnf("proposer 'base' %s: can't create attacher: '%v'", b.Name, err)
		return nil, true
	}
	b.Tracef(TraceTag, "created attacher with baseline %s", a.BaselineBranch().IDShortString)

	if b.TargetTs.Tick() != 0 {
		b.Tracef(TraceTag, "making non-branch, extending %s, collecting and inserting tag-along inputs", extend.IDShortString)
		numInserted := b.AttachTagAlongInputs(a)
		b.Tracef(TraceTag, "inserted %d tag-along inputs", numInserted)
	} else {
		b.Tracef(TraceTag, "making branch, extending %s, no tag-along", extend.IDShortString)
	}
	return a, false
}
