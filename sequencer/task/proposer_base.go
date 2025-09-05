package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const (
	TraceTagBaseProposer     = "propose-base"
	TraceTagBaseProposerExit = "propose-base-exit"
)

func init() {
	registerProposerStrategy(&proposerStrategy{
		Name:             "base",
		ShortName:        "b0",
		GenerateProposal: baseProposeGenerator,
	})
}

func baseProposeGenerator(p *proposer) (*attacher.IncrementalAttacher, bool) {
	p.Tracef(TraceTagBaseProposerExit, "IN base proposer %s", p.Name)

	extend := p.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		p.Log().Warnf("BaseProposer-%s: can't find own milestone output", p.Name)
		return nil, true
	}
	if p.targetTs.IsSlotBoundary() && !extend.VID.IsBranchTransaction() && extend.VID.Slot()+1 != p.targetTs.Slot {
		// the latest output is beyond reach for the branch as the next transaction
		p.Tracef(TraceTagBaseProposerExit, "OUT base proposer %s: latest output is beyond reach: %s", p.Name, extend.IDStringShort())
		return nil, true
	}

	if !ledger.ValidSequencerPace(extend.Timestamp(), p.targetTs) {
		// it means the proposer is obsolete, abandon it
		p.Tracef(TraceTagBaseProposerExit, "force exit in %s: own latest milestone and target ledger time does not make valid pace %s",
			p.Name, extend.IDStringShort)
		return nil, true
	}

	p.Tracef(TraceTagBaseProposer, "%s extending %s", p.Name, extend.IDStringShort)
	// own latest milestone exists
	if !p.targetTs.IsSlotBoundary() {
		// the target is not a branch target
		p.Tracef(TraceTagBaseProposer, "%s target is not a branch target", p.Name)
		if extend.Slot() != p.targetTs.Slot {
			p.Tracef(TraceTagBaseProposerExit, "%s force exit: cross-slot %s", p.Name, extend.IDStringShort)
			return nil, true
		}
		p.Tracef(TraceTagBaseProposer, "%s target is not a branch and it is on the same slot", p.Name)
		if !extend.VID.IsSequencerMilestone() {
			p.Tracef(TraceTagBaseProposerExit, "%s force exit: not-sequencer %s", p.Name, extend.IDStringShort)
			return nil, true
		}
		// proposer optimization: if backlog and extended output didn't change since last target,
		// makes no sense to continue with proposals.
		noChanges := p.slotData.lastExtendedOutputIDB0 == extend.DecodeID() &&
			!p.Backlog().ArrivedOutputsSince(p.slotData.lastTimeBacklogCheckedB0)
		p.slotData.lastTimeBacklogCheckedB0 = time.Now()
		if noChanges {
			p.Tracef(TraceTagBaseProposerExit, "%s 'no changes extend' = %s", p.Name, extend.IDStringShort)
			return nil, true
		}
	}

	p.Tracef(TraceTagBaseProposer, "%s predecessor %s is sequencer milestone with coverage %s",
		p.Name, extend.IDStringShort, extend.VID.GetLedgerCoverageString)

	a, err := attacher.NewIncrementalAttacher(p.Name, p.environment, p.targetTs, extend)
	if err != nil {
		p.Tracef(TraceTagBaseProposerExit, "%s can't create attacher: '%v'", p.Name, err)
		return nil, true
	}
	p.Tracef(TraceTagBaseProposer, "%s created attacher with baseline %s, cov: %s",
		p.Name, a.BaselineBranch().StringShort, func() string { return util.Th(a.FinalLedgerCoverage(p.targetTs)) },
	)
	if p.targetTs.IsSlotBoundary() {
		p.Tracef(TraceTagBaseProposer, "%s making branch, no tag-along, extending %s cov: %s, attacher %s cov: %s",
			p.Name,
			extend.IDStringShort, func() string { return util.Th(extend.VID.GetLedgerCoverage()) },
			a.Name(), func() string { return util.Th(a.FinalLedgerCoverage(p.targetTs)) },
		)
	} else {
		p.Tracef(TraceTagBaseProposer, "%s making non-branch, extending %s, collecting and inserting tag-along inputs", p.Name, extend.IDStringShort)

		p.insertInputs(a)
	}

	p.slotData.lastExtendedOutputIDB0 = extend.DecodeID()
	// only need one proposal when extending a branch
	stopProposing := extend.VID.IsBranchTransaction()
	p.Tracef(TraceTagBaseProposerExit, "exit with finalProposal in %s: extend = %s",
		p.Name, extend.IDStringShort)
	return a, stopProposing
}
