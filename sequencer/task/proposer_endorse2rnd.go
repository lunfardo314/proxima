package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
)

const TraceTagEndorse2RndProposer = "propose-endorse2rnd"

func init() {
	registerProposerStrategy(&Strategy{
		Name:             "endorse2rnd",
		ShortName:        "r2",
		GenerateProposal: endorse2RndProposeGenerator,
	})
}

func endorse2RndProposeGenerator(p *Proposer) (*attacher.IncrementalAttacher, bool) {
	if p.targetTs.IsSlotBoundary() {
		// the proposer does not generate branch transactions
		return nil, true
	}

	// Check peers in RANDOM order
	a := p.ChooseFirstExtendEndorsePair(true, nil)
	if a == nil {
		p.Tracef(TraceTagEndorse2RndProposer, "propose: ChooseFirstExtendEndorsePair returned nil")
		return nil, false
	}
	endorsing := a.Endorsing()[0]
	extending := a.Extending()
	if !a.Completed() {
		a.Close()
		p.Tracef(TraceTagEndorse2RndProposer, "proposal [extend=%s, endorsing=%s] not complete 1", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	newOutputArrived := p.Backlog().ArrivedOutputsSince(p.slotData.lastTimeBacklogCheckedR2)
	p.slotData.lastTimeBacklogCheckedR2 = time.Now()

	// then try to add one endorsement more
	addedSecond := false
	endorsing0 := a.Endorsing()[0]
	// RANDOM order
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

		triplet := extendEndorseTriplet{
			extend:   extending,
			endorse1: endorsing,
			endorse2: endorsementCandidate,
		}
		if !newOutputArrived {
			checkedInThePast := false
			p.slotData.withWriteLock(func() {
				// optimization: skipping repeating triplets if new outputs didn't arrive meanwhile
				checkedInThePast = p.slotData.alreadyCheckedTriplets.Contains(triplet)
				if !checkedInThePast {
					// assume invariance wrt order of endorsements
					// check triplet with swapped endorsements
					checkedInThePast = p.slotData.alreadyCheckedTriplets.Contains(extendEndorseTriplet{
						extend:   extending,
						endorse1: endorsementCandidate,
						endorse2: endorsing,
					})
				}
			})
			if checkedInThePast {
				continue
			}
		}

		if err := a.InsertEndorsement(endorsementCandidate); err == nil {
			// remember triplet for the next check, same list for e2 and r2
			p.slotData.withWriteLock(func() {
				p.slotData.alreadyCheckedTriplets.Insert(triplet)
			})
			addedSecond = true
			break //>>>> return attacher
		}
		p.Tracef(TraceTagEndorse2RndProposer, "failed to include endorsement target %s", endorsementCandidate.IDShortString)
	}
	if !addedSecond {
		// no need to repeat job of endorse1
		a.Close()
		return nil, false
	}

	p.InsertTagAlongInputs(a)

	if !a.Completed() {
		a.Close()
		endorsing = a.Endorsing()[0]
		extending = a.Extending()
		p.Tracef(TraceTagEndorse2Proposer, "proposal [extend=%s, endorsing=%s] not complete 2", extending.IDShortString, endorsing.IDShortString)
		return nil, false
	}

	return a, false
}
