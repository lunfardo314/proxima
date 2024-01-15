package proposer_endorse1

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const Endorse1ProposerName = "endorse1"

type Endorse1Proposer struct {
	proposer_generic.TaskGeneric
}

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name: Endorse1ProposerName,
		Constructor: func(generic *proposer_generic.TaskGeneric) proposer_generic.Task {
			return &Endorse1Proposer{TaskGeneric: *generic}
		},
	}
}

func (b *Endorse1Proposer) Run() {
	var a *attacher.IncrementalAttacher
	var forceExit bool

	for b.ContinueCandidateProposing(b.TargetTs) {
		a = b.propose()
		if a != nil {
			b.TraceLocal("Run: proposed pair ")
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

func (b *Endorse1Proposer) propose() *attacher.IncrementalAttacher {
	a := b.ChooseExtendEndorsePair(b.Name, b.TargetTs)
	if a == nil {
		b.TraceLocal("propose failed to propose anything")
		return nil
	}
	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		b.TraceLocal("proposal [extend=%s, endorsing=%s] not complete", a.Extending().IDShortString, endorsing.IDShortString)
		return nil
	}
	b.AttachTagAlongInputs(a)
	util.Assertf(a.Completed(), "incremental attacher %s is not complete", a.Name())
	return a
}
