package proposer_base

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
)

// Base proposer generates branches and bootstraps sequencer when no other sequencers are around

const (
	BaseProposerName      = "base"
	BaseProposerShortName = "b0"
	TraceTag              = "propose-base"
)

type BaseProposer struct {
	proposer_generic.TaskGeneric
}

func Strategy() *proposer_generic.Strategy {
	return &proposer_generic.Strategy{
		Name:      BaseProposerName,
		ShortName: BaseProposerShortName,
		Constructor: func(generic *proposer_generic.TaskGeneric) proposer_generic.Task {
			ret := &BaseProposer{TaskGeneric: *generic}
			ret.WithProposalGenerator(func() (*attacher.IncrementalAttacher, bool) {
				return ret.propose()
			})
			return ret
		},
	}
}

func (b *BaseProposer) propose() (*attacher.IncrementalAttacher, bool) {
	extend := b.OwnLatestMilestoneOutput()
	if !ledger.ValidSequencerPace(extend.Timestamp(), b.TargetTs) {
		// it means proposer is obsolete, abandon it
		b.Tracef(TraceTag, "%s force exit: own lates milestone and target ts does not make valid pace %s", b.Name, extend.IDShortString)
		return nil, true
	}

	b.Tracef(TraceTag, "%s extending %s", b.Name, extend.IDShortString)
	// own latest milestone exists
	if !b.TargetTs.IsSlotBoundary() {
		// target is not a branch target
		b.Tracef(TraceTag, "%s target is not a branch target", b.Name)
		if extend.Slot() != b.TargetTs.Slot() {
			b.Tracef(TraceTag, "%s force exit: cross-slot %s", b.Name, extend.IDShortString)
			return nil, true
		}
		b.Tracef(TraceTag, "%s target is not a branch and it is on the same slot", b.Name)
		if !extend.VID.IsSequencerMilestone() {
			b.Tracef(TraceTag, "%s force exit: not-sequencer %s", b.Name, extend.IDShortString)
			return nil, true
		}
	}
	b.Tracef(TraceTag, "%s predecessor %s is sequencer milestone with coverage %s",
		b.Name, extend.IDShortString, extend.VID.GetLedgerCoverageString)

	a, err := attacher.NewIncrementalAttacher(b.Name, b, b.TargetTs, extend)
	if err != nil {
		b.Tracef(TraceTag, "%s can't create attacher: '%v'", b.Name, err)
		return nil, true
	}
	b.Tracef(TraceTag, "%s created attacher with baseline %s, cov: %s",
		b.Name, a.BaselineBranch().IDShortString, func() string { return util.Th(a.LedgerCoverage()) },
	)
	if b.TargetTs.Tick() != 0 {
		b.Tracef(TraceTag, "%s making non-branch, extending %s, collecting and inserting tag-along inputs", b.Name, extend.IDShortString)
		numInserted := b.AttachTagAlongInputs(a)
		b.Tracef(TraceTag, "%s inserted %d tag-along inputs", b.Name, numInserted)
	} else {
		b.Tracef(TraceTag, "%s making branch, no tag-along, extending %s cov: %s, attacher %s cov: %s",
			b.Name,
			extend.IDShortString, func() string { return util.Th(extend.VID.GetLedgerCoverage()) },
			a.Name(), func() string { return util.Th(a.LedgerCoverage()) },
		)
	}
	a.AdjustCoverage()

	bestCoverageInSlot := b.BestCoverageInTheSlot(b.TargetTs)
	lc := a.LedgerCoverage()
	if lc <= bestCoverageInSlot {
		b.Tracef(TraceTag, "%s abandoning milestone proposal with coverage %s: best milestone in slot has bigger coverage %s",
			b.Name, func() string { return util.Th(lc) }, func() string { return util.Th(bestCoverageInSlot) },
		)
		a.UnReferenceAll()
		return nil, true
	}
	return a, false
}
