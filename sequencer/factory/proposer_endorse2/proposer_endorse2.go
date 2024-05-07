package proposer_endorse2

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
)

// endorse2 proposer generates sequencer transactions by endorsing two heaviest other sequencer
// and then extending own milestone to please the endorsement target (to be non-conflicting)

const (
	Endorse2ProposerName = "endorse2"
	TraceTag             = "propose-endorse2"
)

type Endorse2Proposer struct {
	proposer_generic.TaskGeneric
}

// TODO not finished

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name: Endorse2ProposerName,
		Constructor: func(generic *proposer_generic.TaskGeneric) proposer_generic.Task {
			if generic.TargetTs.Tick() == 0 {
				// endorse strategy ia not applicable for genereting branches
				return nil
			}
			ret := &Endorse2Proposer{TaskGeneric: *generic}
			ret.WithProposalGenerator(func() (*attacher.IncrementalAttacher, bool) {
				return ret.propose(), false
			})
			return ret
		},
	}
}

func (b *Endorse2Proposer) propose() *attacher.IncrementalAttacher {
	a := b.ChooseExtendEndorsePair(b.Name, b.TargetTs)
	if a == nil {
		b.Tracef(TraceTag, "propose: ChooseExtendEndorsePair returned nil")
		return nil
	}
	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		b.Tracef(TraceTag, "proposal [extend=%s, endorsing=%s] not complete", extending.IDShortString, endorsing.IDShortString)
		return nil
	}

	for _, endorsementCandidate := range b.Backlog().CandidatesToEndorseSorted(b.TargetTs) {
		if endorsementCandidate == a.Endorsing()[0] {
			continue
		}
		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			break
		}
		b.Tracef(TraceTag, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}

	b.AttachTagAlongInputs(a)
	b.Assertf(a.Completed(), "incremental attacher %s is not complete", a.Name())
	a.AdjustCoverage()
	return a
}
