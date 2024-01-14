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
		latestMs := b.GetLatestOwnMilestone()
		if a, forceExit = b.proposeBase(latestMs.VID); forceExit {
			return
		}
		if a != nil {
			if forceExit = b.Propose(a); forceExit {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *BaseProposer) proposeBase(extend *vertex.WrappedTx) (*attacher.IncrementalAttacher, bool) {
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		if extend.Slot() != b.TargetTs.Slot() {
			// on startup or cross-slot will only produce branches
			b.Tracef("proposer", "proposeBase.force exit: cross-slot %s", extend.IDShortString)
			return nil, true
		}
		if !extend.IsSequencerMilestone() {
			// not cross-slot, but predecessor must be sequencer tx
			b.Tracef("proposer", "proposeBase.force exit: not-sequencer %s", extend.IDShortString)
			return nil, true
		}
	}

	if !b.TargetTs.IsSlotBoundary() && extend.Slot() != b.TargetTs.Slot() {
		// on startup or cross-slot will only produce branches
		b.Tracef("proposer", "proposeBase.force exit: cross-slot %s", extend.IDShortString)
		return nil, true
	}

	a, err := attacher.NewIncrementalAttacher(b.Name(), b, b.TargetTs, extend)
	if err != nil {
		b.Log().Warnf("proposer %s: can't create attacher: '%v'", b.Name(), err)
		return nil, true
	}

	if b.TargetTs.Tick() != 0 {
		b.Tracef("proposer", "proposer task %s: making non-branch, extending %s", b.Name(), extend.IDShortString)
		b.AttachTagAlongInputs(a)
	} else {
		b.Tracef("proposer", "proposer task %s making branch, extending %s", b.Name(), extend.IDShortString)
	}
	return a, false
}
