package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

// r3 is a proposer strategy which proposes transactions with 3 endorsements chosen by selecting endorsement targets at random

const TraceTagEndorse3RndProposer = "propose-endorse3rnd"

// disabled in sequencer2: replaced by f0 (factory proposer)
// func init() {
// 	registerProposerStrategy(&proposerStrategy{
// 		Name:             "endorse3rnd",
// 		ShortName:        "r3",
// 		GenerateProposal: endorse3RndProposeGenerator,
// 	})
// }

func endorse3RndProposeGenerator(p *proposer) (*proposal, bool) {
	if p.targetTs.IsSlotBoundary() {
		// the proposer does not generate branch transactions
		return nil, true
	}

	// CheckConflicts all pairs, in descending order
	a := p.ChooseFirstExtendEndorsePair(true, func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) bool {
		checked, consistent := p.taskData.slotData.wasCombinationChecked(extend, endorse)
		return !checked || consistent
	})
	if a == nil {
		p.Tracef(TraceTagEndorse3RndProposer, "propose: ChooseFirstExtendEndorsePair returned nil")
		return nil, false
	}
	endorsing0 := a.Endorsing()[0]
	extending := a.Extending()
	if !a.Completed() {
		a.Close()
		p.Tracef(TraceTagEndorse3RndProposer, "finalProposal [extend=%s, endorsing=%s] not complete 1", extending.IDStringShort, endorsing0.IDShortString)
		return nil, false
	}

	newOutputArrived := p.Backlog().ArrivedOutputsSince(p.slotData.lastTimeBacklogCheckedE3)
	p.slotData.withWriteLock(func() {
		p.slotData.lastTimeBacklogCheckedE3 = time.Now()
	})

	// then try to add one endorsement more
	var endorsing1 *vertex.WrappedTx

	for _, endorsementCandidate := range p.Backlog().CandidatesToEndorseShuffled(p.targetTs) {
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
			checked, _ := p.taskData.slotData.wasCombinationChecked(extending, endorsing0, endorsementCandidate)
			if checked {
				continue
			}
		}
		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			p.taskData.slotData.markCombinationChecked(true, extending, endorsing0, endorsementCandidate)
			endorsing1 = endorsementCandidate
			break //>>>> second endorsement
		} else {
			p.taskData.slotData.markCombinationChecked(false, extending, endorsing0, endorsementCandidate)
		}
		p.Tracef(TraceTagEndorse3RndProposer, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if endorsing1 == nil {
		// no need to repeat the job of endorse1
		a.Close()
		return nil, false
	}
	// try to add the 3rd endorsement
	var endorsing2 *vertex.WrappedTx

	for _, endorsementCandidate := range p.Backlog().CandidatesToEndorseShuffled(p.targetTs) {
		select {
		case <-p.ctx.Done():
			a.Close()
			return nil, true
		default:
		}
		if endorsementCandidate == endorsing0 || endorsementCandidate == endorsing1 {
			continue
		}
		if !newOutputArrived {
			checked, _ := p.taskData.slotData.wasCombinationChecked(extending, endorsing0, endorsing1, endorsementCandidate)
			if checked {
				continue
			}
		}
		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			p.taskData.slotData.markCombinationChecked(true, extending, endorsing0, endorsing1, endorsementCandidate)
			endorsing2 = endorsementCandidate
			break //>>>> third endorsement
		} else {
			p.taskData.slotData.markCombinationChecked(false, extending, endorsing0, endorsing1, endorsementCandidate)
		}
		p.Tracef(TraceTagEndorse3RndProposer, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if endorsing2 == nil {
		// no need to repeat the job of endorse2
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
		if !a.IsClosed() {
			endorsing0 = ret.Endorsing()[0]
			endorsing1 = ret.Endorsing()[1]
			endorsing2 = ret.Endorsing()[2]
			extending = ret.Extending()
			p.Tracef(TraceTagEndorse3RndProposer, "finalProposal [extend=%s, endorsing=%s, %s, %s] not complete 2",
				extending.IDStringShort, endorsing0.IDShortString, endorsing1.IDShortString, endorsing2.IDShortString)
			ret.Close()
		}
		return nil, false
	}
	return ret, false
}
