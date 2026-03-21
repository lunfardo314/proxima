package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

// e1 is a proposer strategy which endorses one other sequencer

const TraceTagEndorse1Proposer = "propose-endorse1"

// disabled in sequencer2: replaced by f0 (factory proposer)
// func init() {
// 	registerProposerStrategy(&proposerStrategy{
// 		Name:             "endorse1",
// 		ShortName:        "e1",
// 		GenerateProposal: endorse1ProposeGenerator,
// 	})
// }

func endorse1ProposeGenerator(p *proposer) (*proposal, bool) {
	if p.targetTs.IsSlotBoundary() {
		// the proposer does not generate branch transactions
		return nil, true
	}
	// choose an extend-endorse pair with optimization. If that pair was chosen in the past and newOutputs didn't arrive
	// since last check, use that pair to create a new attacher (if not conflicting)
	newOutputsArrived := p.Backlog().ArrivedOutputsSince(p.taskData.slotData.lastTimeBacklogCheckedE1)
	p.taskData.slotData.lastTimeBacklogCheckedE1 = time.Now()
	a := p.ChooseFirstExtendEndorsePair(false, func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) bool {
		if newOutputsArrived {
			// use pair with new tag-along outputs
			return true
		}
		alreadyChecked, _ := p.taskData.slotData.wasCombinationChecked(extend, endorse)
		return !alreadyChecked
	})

	if a == nil {
		p.Tracef(TraceTagEndorse1Proposer, "propose: ChooseFirstExtendEndorsePair returned nil in %s", p.Name)
		return nil, false
	}
	if !a.Completed() {
		if !a.IsClosed() {
			//endorsing := a.Endorsing()[0]
			//extending := a.Extending()
			//p.Tracef(TraceRunTagTask, "propose [extend=%s, endorsing=%s] not complete 1 in %s",
			//	extending.IDStringShort, endorsing.IDShortString, p.Name)
			a.Close()
		}
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
			//endorsing := ret.Endorsing()[0]
			//extending := ret.Extending()
			//p.Tracef(TraceRunTagTask, "propose [extend=%s, endorsing=%s] not complete 2", extending.IDStringShort, endorsing.IDShortString)
			ret.Close()
		}
		return nil, false
	}

	return ret, false
}
