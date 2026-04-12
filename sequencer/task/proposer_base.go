package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const (
	TraceTagBaseProposer     = "propose-base"
	TraceTagBaseProposerExit = "propose-base-exit"
)

// tryBranchProposal generates a branch transaction (slot boundary target).
// Returns nil if branch cannot be produced.
func (t *taskData) tryBranchProposal() *finalProposal {
	t.Tracef(TraceTagBaseProposer, "IN tryBranchProposal %s", t.Name)

	extend := t.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		t.Log().Warnf("tryBranchProposal-%s: can't find own milestone output", t.Name)
		return nil
	}
	if !extend.VID.IsBranchTransaction() && extend.VID.Slot()+1 != t.targetTs.Slot {
		// the latest output is beyond reach for the branch as the next transaction
		t.Tracef(TraceTagBaseProposerExit, "OUT tryBranchProposal %s: latest output is beyond reach: %s", t.Name, extend.IDStringShort())
		return nil
	}

	if !ledger.ValidSequencerPace(extend.Timestamp(), t.targetTs) {
		t.Tracef(TraceTagBaseProposerExit, "tryBranchProposal %s: invalid pace from %s", t.Name, extend.IDStringShort)
		return nil
	}

	a, err := attacher.NewIncrementalAttacher(t.Name, t.environment, t.targetTs, extend)
	if err != nil {
		t.Tracef(TraceTagBaseProposerExit, "tryBranchProposal %s: can't create attacher: '%v'", t.Name, err)
		return nil
	}

	prop, err := t.newProposal(a)
	if err != nil {
		t.Tracef(TraceTagBaseProposerExit, "tryBranchProposal %s: can't create proposal: '%v'", t.Name, err)
		return nil
	}

	// branch coverage bounds check (bootstrap chain is exempt)
	if t.SequencerID() != base.BoostrapSequencerID {
		lib := prop.SeqTxBuilder.Library
		coverage := prop.SeqTxBuilder.CurrentBranchCoverage()
		lower := lib.BranchCoverageLowerBound(t.targetTs.Slot)
		upper := lib.BranchCoverageUpperBound(t.targetTs.Slot)
		if coverage < lower || coverage > upper {
			if !t.slotData.coverageBoundsWarned {
				t.slotData.coverageBoundsWarned = true
				t.Log().Warnf("tryBranchProposal-%s: branch coverage %s out of bounds [%s, %s] at slot %d, skipping branch",
					t.Name, util.Th(coverage), util.Th(lower), util.Th(upper), t.targetTs.Slot)
			}
			prop.Close()
			return nil
		}
	}

	t.Tracef(TraceTagBaseProposer, "tryBranchProposal %s: making branch, extending %s cov: %s, attacher %s cov: %s",
		t.Name,
		extend.IDStringShort, func() string { return util.Th(extend.VID.GetLedgerCoverage()) },
		a.Name(), func() string { return util.Th(a.FinalLedgerCoverage(t.targetTs)) },
	)

	// branches don't get tag-along or delegation inputs
	fp, err := prop.finalize("branch")
	if err != nil {
		t.Log().Warnf("tryBranchProposal-%s: finalize failed: %v", t.Name, err)
		return nil
	}
	return fp
}

// tryBaseExtendProposal generates a non-branch transaction by extending the own latest milestone
// without endorsements. This is the fallback when the factory has no skeleton.
// Returns nil if the extend is not possible or would not improve coverage.
func (t *taskData) tryBaseExtendProposal() *finalProposal {
	t.Tracef(TraceTagBaseProposer, "IN tryBaseExtendProposal %s", t.Name)

	extend := t.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		t.Log().Warnf("tryBaseExtendProposal-%s: can't find own milestone output", t.Name)
		return nil
	}

	if !ledger.ValidSequencerPace(extend.Timestamp(), t.targetTs) {
		t.Tracef(TraceTagBaseProposerExit, "tryBaseExtendProposal %s: invalid pace from %s", t.Name, extend.IDStringShort)
		return nil
	}

	if extend.Slot() != t.targetTs.Slot {
		t.Tracef(TraceTagBaseProposerExit, "tryBaseExtendProposal %s: cross-slot %s", t.Name, extend.IDStringShort)
		return nil
	}
	if !extend.VID.IsSequencerTransaction() {
		t.Tracef(TraceTagBaseProposerExit, "tryBaseExtendProposal %s: not-sequencer %s", t.Name, extend.IDStringShort)
		return nil
	}

	t.Tracef(TraceTagBaseProposer, "tryBaseExtendProposal %s: predecessor %s is sequencer milestone with coverage %s",
		t.Name, extend.IDStringShort, extend.VID.GetLedgerCoverageString)

	a, err := attacher.NewIncrementalAttacher(t.Name, t.environment, t.targetTs, extend)
	if err != nil {
		t.Tracef(TraceTagBaseProposerExit, "tryBaseExtendProposal %s: can't create attacher: '%v'", t.Name, err)
		return nil
	}

	prop, err := t.newProposal(a)
	if err != nil {
		t.Tracef(TraceTagBaseProposerExit, "tryBaseExtendProposal %s: can't create proposal: '%v'", t.Name, err)
		return nil
	}

	t.Tracef(TraceTagBaseProposer, "tryBaseExtendProposal %s: collecting and inserting tag-along inputs, extending %s", t.Name, extend.IDStringShort)
	prop.insertInputs()

	fp, err := prop.finalize("base")
	if err != nil {
		t.Log().Warnf("tryBaseExtendProposal-%s: finalize failed: %v", t.Name, err)
		return nil
	}
	return fp
}
