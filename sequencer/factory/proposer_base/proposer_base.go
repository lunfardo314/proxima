package proposer_base

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
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
	extend := b.OwnLatestMilestoneOutput()

	b.Tracef(TraceTag, "%s extending %s, coverage: %s", b.Name, extend.IDShortString, extend.VID.GetLedgerCoverage().String())
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		// target is not a branch target
		b.Tracef(TraceTag, "%s target is not a branch target", b.Name)
		if extend.Slot() != b.TargetTs.Slot() {
			b.Tracef(TraceTag, "%s force exit: cross-slot %s", b.Name, extend.IDShortString)
			return nil, true
		}
		b.Tracef(TraceTag, "%s target is not a branch and it is on the same slot", b.Name)
		if !extend.VID.IsSequencerMilestone() {
			b.Tracef(TraceTag, "%s force exit: not-sequencer %s", b.Name, extend.IDShortString)
			return nil, true
		}
	}
	b.Tracef(TraceTag, "%s predecessor %s is sequencer", b.Name, extend.IDShortString)

	a, err := attacher.NewIncrementalAttacher(b.Name, b, b.TargetTs, extend)
	if err != nil {
		b.Tracef(TraceTag, "%s can't create attacher: '%v'", b.Name, err)
		return nil, true
	}
	b.Tracef(TraceTag, "%s created attacher with baseline %s", b.Name, a.BaselineBranch().IDShortString)

	if b.TargetTs.Tick() != 0 {
		b.Tracef(TraceTag, "%s making non-branch, extending %s, collecting and inserting tag-along inputs", b.Name, extend.IDShortString)
		numInserted := b.AttachTagAlongInputs(a)
		b.Tracef(TraceTag, "%s inserted %d tag-along inputs", b.Name, numInserted)
	} else {
		b.Tracef(TraceTag, "%s making branch, no tag-along, extending %s cov: %s, attacher %s cov: %s",
			b.Name, extend.IDShortString, extend.VID.GetLedgerCoverage().String(), a.Name(), util.Ref(a.LedgerCoverage()).String())
	}
	return a, false
}
