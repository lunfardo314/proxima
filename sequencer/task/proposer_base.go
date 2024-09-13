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
	if p.targetTs.IsSlotBoundary() && !extend.VID.IsBranchTransaction() && extend.VID.Slot()+1 != p.targetTs.Slot() {
		// latest output is beyond reach for the branch as next transaction
		//p.Log().Warnf("BaseProposer-%s: can't propose branch for target %s because non-branch chain predecessor %s is older than 1 slot",
		//	p.Name, p.targetTs.String(), extend.VID.IDShortString())
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
	// non-branch
	p.Tracef(TraceTagBaseProposer, "%s predecessor %s is sequencer milestone with coverage %s",
		p.Name, extend.IDShortString, extend.VID.GetLedgerCoverageString)

	a, err := attacher.NewIncrementalAttacher(p.Name, p.environment, p.targetTs, extend)
	if err != nil {
		p.Tracef(TraceTagBaseProposer, "%s can't create attacher: '%v'", p.Name, err)
		return nil, true
	}
	p.Tracef(TraceTagBaseProposer, "%s created attacher with baseline %s, cov: %s",
		p.Name, a.BaselineBranch().IDShortString, func() string { return util.Th(a.AccumulatedCoverage()) },
	)
	if p.targetTs.IsSlotBoundary() {
		p.Tracef(TraceTagBaseProposer, "%s making branch, no tag-along, extending %s cov: %s, attacher %s cov: %s",
			p.Name,
			extend.IDShortString, func() string { return util.Th(extend.VID.GetLedgerCoverage()) },
			a.Name(), func() string { return util.Th(a.AccumulatedCoverage()) },
		)
	} else {
		p.Tracef(TraceTagBaseProposer, "%s making non-branch, extending %s, collecting and inserting tag-along inputs", p.Name, extend.IDShortString)

		numInserted := p.InsertTagAlongInputs(a)
		p.Tracef(TraceTagBaseProposer, "%s inserted %d tag-along inputs", p.Name, numInserted)
	}
	a.AdjustCoverage()

	// only need one proposal when extending a branch
	stopProposing := extend.VID.IsBranchTransaction()
	return a, stopProposing
}
