package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
)

const TraceTagEndorse2Proposer = "propose-endorse2"

func init() {
	registerProposerStrategy(&Strategy{
		Name:             "endorse2",
		ShortName:        "e2",
		GenerateProposal: endorse2ProposeGenerator,
	})
}

func endorse2ProposeGenerator(p *Proposer) (*attacher.IncrementalAttacher, bool) {
	if p.targetTs.IsSlotBoundary() {
		// the proposer does not generate branch transactions
		return nil, true
	}
	// e2 proposer optimizations: if backlog didn't change, no reason to generate another proposal
	noChanges := false
	p.Task.slotData.withWriteLock(func() {
		noChanges = !p.Backlog().ChangedSince(p.Task.slotData.lastTimeBacklogCheckedE2)
		p.Task.slotData.lastTimeBacklogCheckedE2 = time.Now()
	})
	if noChanges {
		return nil, true
	}
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
		select {
		case <-p.ctx.Done():
			a.Close()
			return nil, true
		default:
		}
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

	p.InsertTagAlongInputs(a)

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
