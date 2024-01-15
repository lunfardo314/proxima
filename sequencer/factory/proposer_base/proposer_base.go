package proposer_base

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
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
			return &BaseProposer{TaskGeneric: *generic}
		},
	}
}

func (b *BaseProposer) Run() {
	var a *attacher.IncrementalAttacher
	var forceExit bool

	for b.ContinueCandidateProposing(b.TargetTs) {
		latestMs := b.OwnLatestMilestone()
		if a, forceExit = b.propose(latestMs); forceExit {
			return
		}
		if a != nil && a.Completed() {
			b.TraceLocal("Run: completed")
			if forceExit = b.Propose(a); forceExit {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *BaseProposer) propose(extend *vertex.WrappedTx) (*attacher.IncrementalAttacher, bool) {
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		// target is not a branch target
		b.TraceLocal("propose: target is not a branch target")
		if extend.Slot() != b.TargetTs.Slot() {
			b.TraceLocal("propose.force exit: cross-slot %s", extend.IDShortString)
			return nil, true
		}
		b.TraceLocal("propose: target is not a branch on the same slot")
		if !extend.IsSequencerMilestone() {
			b.TraceLocal("propose.force exit: not-sequencer %s", extend.IDShortString)
			return nil, true
		}
	}
	b.TraceLocal("propose: predecessor is sequencer")

	a, err := attacher.NewIncrementalAttacher(b.Name, b, b.TargetTs, extend)
	if err != nil {
		b.Log().Warnf("proposer %s: can't create attacher: '%v'", b.Name, err)
		return nil, true
	}
	b.TraceLocal("propose: created attacher")

	if b.TargetTs.Tick() != 0 {
		b.TraceLocal("propose: making non-branch, extending %s, collecting tag-along inputs", extend.IDShortString)
		b.AttachTagAlongInputs(a)
	} else {
		b.TraceLocal("propose: making branch, extending %s, no tag-along", extend.IDShortString)
	}
	return a, false
}
