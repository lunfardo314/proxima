package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

// e2 is a proposer strategy which proposes transactions with 2 endorsements chosen by selecting those with bigger coverage first

const TraceTagEndorse2Proposer = "propose-endorse2"

// disabled in sequencer2: replaced by f0 (factory proposer)
// func init() {
// 	registerProposerStrategy(&proposerStrategy{
// 		Name:             "endorse2",
// 		ShortName:        "e2",
// 		GenerateProposal: endorse2ProposeGenerator,
// 	})
// }

func endorse2ProposeGenerator(p *proposer) (*proposal, bool) {
	if p.targetTs.IsSlotBoundary() {
		// the proposer does not generate branch transactions
		return nil, true
	}

	// CheckConflicts all pairs, in descending order
	a := p.ChooseFirstExtendEndorsePair(false, func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) bool {
		checked, consistent := p.taskData.slotData.wasCombinationChecked(extend, endorse)
		return !checked || consistent
	})
	if a == nil {
		p.Tracef(TraceTagEndorse2Proposer, "propose: ChooseFirstExtendEndorsePair returned nil")
		return nil, false
	}
	endorsing := a.Endorsing()[0]
	extending := a.Extending()
	if !a.Completed() {
		a.Close()
		p.Tracef(TraceTagEndorse2Proposer, "finalProposal [extend=%s, endorsing=%s] not complete 1", extending.IDStringShort, endorsing.IDShortString)
		return nil, false
	}

	newOutputArrived := p.Backlog().ArrivedOutputsSince(p.slotData.lastTimeBacklogCheckedE2)
	p.slotData.lastTimeBacklogCheckedE2 = time.Now()

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
		if !newOutputArrived {
			checked, _ := p.taskData.slotData.wasCombinationChecked(extending, endorsing, endorsementCandidate)
			if checked {
				continue
			}
		}

		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			p.taskData.slotData.markCombinationChecked(true, extending, endorsing, endorsementCandidate)
			addedSecond = true
			break //>>>> return attacher
		} else {
			p.taskData.slotData.markCombinationChecked(false, extending, endorsing, endorsementCandidate)
		}
		p.Tracef(TraceTagEndorse2Proposer, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if !addedSecond {
		// no need to repeat the job of endorse1
		a.Close()
		return nil, false
	}
	ret, err := p.newProposal(a)
	if err != nil {
		p.Tracef(TraceTagEndorse1Proposer, "propose: failed to create proposal in %s: %v", p.Name, err)
		return nil, false
	}
	ret.insertInputs()

	if !ret.Completed() {
		if !ret.IsClosed() {
			endorsing = a.Endorsing()[0]
			extending = a.Extending()
			p.Tracef(TraceTagEndorse2Proposer, "finalProposal [extend=%s, endorsing=%s] not complete 2", extending.IDStringShort, endorsing.IDShortString)
			ret.Close()
		}
		return nil, false
	}

	return ret, false
}
