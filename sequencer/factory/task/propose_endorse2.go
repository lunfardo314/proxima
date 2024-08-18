package task

import "github.com/lunfardo314/proxima/core/attacher"

const TraceTagEndorse2Proposer = "propose-endorse2"

func init() {
	registerProposerStrategy(&Strategy{
		Name:             "endorse2",
		ShortName:        "e2",
		GenerateProposal: endorse2ProposeGenerator,
	})
}

func endorse2ProposeGenerator(p *Proposer) (*attacher.IncrementalAttacher, bool) {
	// first do the same as endorse1
	a := p.ChooseExtendEndorsePair()
	if a == nil {
		p.Tracef(TraceTagEndorse2Proposer, "propose: ChooseExtendEndorsePair returned nil")
		return nil, false
	}
	if !a.Completed() {
		a.Close()
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		p.Tracef(TraceTagEndorse2Proposer, "proposal [extend=%s, endorsing=%s] not complete 1", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	// then try to add one endorsement more
	addedSecond := false
	endorsing0 := a.Endorsing()[0]
	for _, endorsementCandidate := range p.Backlog().CandidatesToEndorseSorted(p.targetTs) {
		if endorsementCandidate == endorsing0 {
			continue
		}
		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			addedSecond = true
			break
		}
		p.Tracef(TraceTagEndorse2Proposer, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if !addedSecond {
		// no need to repeat job of endorse1
		a.Close()
		return nil, false
	}

	p.AttachTagAlongInputs(a)

	if !a.Completed() {
		a.Close()
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		p.Tracef(TraceTagEndorse2Proposer, "proposal [extend=%s, endorsing=%s] not complete 2", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	a.AdjustCoverage()
	return a, false
}
