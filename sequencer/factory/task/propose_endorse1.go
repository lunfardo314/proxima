package task

import "github.com/lunfardo314/proxima/core/attacher"

const TraceTagEndorse1Proposer = "propose-endorse1"

func init() {
	registerProposerStrategy(&Strategy{
		Name:             "endorse1",
		ShortName:        "e1",
		GenerateProposal: endorse1ProposeGenerator,
	})
}

func endorse1ProposeGenerator(p *Proposer) (*attacher.IncrementalAttacher, bool) {
	a := p.ChooseExtendEndorsePair()
	if a == nil {
		p.Tracef(TraceTagEndorse1Proposer, "propose: ChooseExtendEndorsePair returned nil")
		return nil, false
	}
	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		p.Tracef(TraceTagTask, "proposal [extend=%s, endorsing=%s] not complete 1", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	p.AttachTagAlongInputs(a)

	if !a.Completed() {
		endorsing := a.Endorsing()[0]
		extending := a.Extending()
		p.Tracef(TraceTagTask, "proposal [extend=%s, endorsing=%s] not complete 2", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	a.AdjustCoverage()
	return a, false
}
