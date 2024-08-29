package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const TraceTagBaseProposer = "propose-base"

func init() {
	registerProposerStrategy(&Strategy{
		Name:             "base",
		ShortName:        "b0",
		GenerateProposal: baseProposeGenerator,
	})
}

func baseProposeGenerator(p *Proposer) (*attacher.IncrementalAttacher, bool) {
	extend := p.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		p.Log().Warnf("BaseProposer-%s: can't find own milestone output", p.Name)
		return nil, true
	}
	if !ledger.ValidSequencerPace(extend.Timestamp(), p.targetTs) {
		// it means proposer is obsolete, abandon it
		p.Tracef(TraceTagBaseProposer, "force exit in %s: own latest milestone and target ledger time does not make valid pace %s",
			p.Name, extend.IDShortString)
		return nil, true
	}

	p.Tracef(TraceTagBaseProposer, "%s extending %s", p.Name, extend.IDShortString)
	// own latest milestone exists
	if !p.targetTs.IsSlotBoundary() {
		// target is not a branch target
		p.Tracef(TraceTagBaseProposer, "%s target is not a branch target", p.Name)
		if extend.Slot() != p.targetTs.Slot() {
			p.Tracef(TraceTagBaseProposer, "%s force exit: cross-slot %s", p.Name, extend.IDShortString)
			return nil, true
		}
		p.Tracef(TraceTagBaseProposer, "%s target is not a branch and it is on the same slot", p.Name)
		if !extend.VID.IsSequencerMilestone() {
			p.Tracef(TraceTagBaseProposer, "%s force exit: not-sequencer %s", p.Name, extend.IDShortString)
			return nil, true
		}
	}
	p.Tracef(TraceTagBaseProposer, "%s predecessor %s is sequencer milestone with coverage %s",
		p.Name, extend.IDShortString, extend.VID.GetLedgerCoverageString)

	a, err := attacher.NewIncrementalAttacher(p.Name, p.Environment, p.targetTs, extend)
	if err != nil {
		p.Tracef(TraceTagBaseProposer, "%s can't create attacher: '%v'", p.Name, err)
		return nil, true
	}
	p.Tracef(TraceTagBaseProposer, "%s created attacher with baseline %s, cov: %s",
		p.Name, a.BaselineBranch().IDShortString, func() string { return util.Th(a.LedgerCoverage()) },
	)
	if p.targetTs.Tick() != 0 {
		p.Tracef(TraceTagBaseProposer, "%s making non-branch, extending %s, collecting and inserting tag-along inputs", p.Name, extend.IDShortString)
		numInserted := p.InsertTagAlongInputs(a)
		p.Tracef(TraceTagBaseProposer, "%s inserted %d tag-along inputs", p.Name, numInserted)
	} else {
		p.Tracef(TraceTagBaseProposer, "%s making branch, no tag-along, extending %s cov: %s, attacher %s cov: %s",
			p.Name,
			extend.IDShortString, func() string { return util.Th(extend.VID.GetLedgerCoverage()) },
			a.Name(), func() string { return util.Th(a.LedgerCoverage()) },
		)
	}
	a.AdjustCoverage()

	// remove this part because it is checked at the task level
	//bestCoverageInSlot := p.BestCoverageInTheSlot(p.targetTs)
	//lc := a.LedgerCoverage()
	//if lc <= bestCoverageInSlot {
	//	p.Tracef(TraceTagBaseProposer, "%s abandoning milestone proposal with coverage %s: best milestone in slot has bigger coverage %s",
	//		p.Name, func() string { return util.Th(lc) }, func() string { return util.Th(bestCoverageInSlot) },
	//	)
	//	a.Close()
	//	return nil, true
	//}
	return a, false
}
