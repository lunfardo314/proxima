package proposer_endorse2

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
)

// endorse2 proposer generates sequencer transactions by endorsing TWO heaviest other sequencer
// and then extending own milestone to please the endorsement target (to be non-conflicting)

const (
	Endorse2ProposerName      = "endorse2"
	Endorse2ProposerShortName = "e2"
	TraceTag                  = "propose-endorse2"
)

type Endorse2Proposer struct {
	proposer_generic.TaskGeneric
}

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name:      Endorse2ProposerName,
		ShortName: Endorse2ProposerShortName,
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
	// first do the same as endorse1
	a := b.ChooseExtendEndorsePair(b.Name, b.TargetTs)
	if a == nil {
		b.Tracef(TraceTag, "propose: ChooseExtendEndorsePair returned nil")
		return nil
	}
	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		b.Tracef(TraceTag, "proposal [extend=%s, endorsing=%s] not complete 1", extending.IDShortString, endorsing.IDShortString)
		return nil
	}

	// then try to add one endorsement more
	addedSecond := false
	endorsing0 := a.Endorsing()[0]
	for _, endorsementCandidate := range b.Backlog().CandidatesToEndorseSorted(b.TargetTs) {
		if endorsementCandidate == endorsing0 {
			continue
		}
		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			addedSecond = true
			break
		}
		b.Tracef(TraceTag, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if !addedSecond {
		// no need to repeat job of endorse1
		return nil
	}

	b.AttachTagAlongInputs(a)

	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		b.Tracef(TraceTag, "proposal [extend=%s, endorsing=%s] not complete 2", extending.IDShortString, endorsing.IDShortString)
		return nil
	}

	// sometimes fails
	// b.Assertf(a.Completed(), "incremental attacher %s is not complete", a.Name())

	a.AdjustCoverage()
	return a
}
