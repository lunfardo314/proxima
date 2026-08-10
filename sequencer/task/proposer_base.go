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
	// Note: extend.VID.BaselineBranch() panics on VirtualTx, so only call it when safe.
	extBaselineSlot := int64(-1)
	extBaselineHex := ""
	if !extend.VID.IsVirtualTx() {
		if extBaseline, ok := extend.VID.BaselineBranch(); ok {
			extBaselineSlot = int64(extBaseline.Slot())
			extBaselineHex = extBaseline.StringHex()
		}
	}
	if !extend.VID.IsBranchTransaction() && extend.VID.Slot()+1 != t.targetTs.Slot {
		t.Log().Warnf("tryBranchProposal-%s: OUT_OF_REACH target=%d extend=%s extSlot=%d extIsBranch=%v extIsVirtual=%v extBaselineSlot=%d",
			t.Name, t.targetTs.Slot, extend.IDStringShort(), extend.VID.Slot(), extend.VID.IsBranchTransaction(), extend.VID.IsVirtualTx(), extBaselineSlot)
		return nil
	}

	// The branch's chain-predecessor input is exempt from the sequencer pace constraint --
	// the ledger (scanInputs) requires strict monotonicity only. That exemption is what lets
	// the final pre-branch consolidation land at the last tick of the slot with the branch one
	// tick later. Applying the full pace here would reject exactly the branches the pre-branch
	// consolidation strategy is designed to produce.
	if base.DiffTicks(t.targetTs, extend.Timestamp()) < 1 {
		t.Log().Warnf("tryBranchProposal-%s: NOT_MONOTONIC target=%s extend=%s extTs=%s",
			t.Name, t.targetTs.String(), extend.IDStringShort(), extend.Timestamp().String())
		return nil
	}

	a, err := attacher.NewIncrementalAttacher(t.Name, t.environment, t.targetTs, extend)
	if err != nil {
		t.Log().Warnf("tryBranchProposal-%s: ATTACHER_FAIL target=%d extend=%s extSlot=%d extIsBranch=%v extBaselineSlot=%d extBaselineHex=%s err=%v",
			t.Name, t.targetTs.Slot, extend.IDStringShort(), extend.VID.Slot(), extend.VID.IsBranchTransaction(),
			extBaselineSlot, extBaselineHex, err)
		return nil
	}

	prop, err := t.newProposal(a)
	if err != nil {
		t.Log().Warnf("tryBranchProposal-%s: PROPOSAL_FAIL target=%d extend=%s err=%v",
			t.Name, t.targetTs.Slot, extend.IDStringShort(), err)
		return nil
	}

	// coverage-contribution bounds check (bootstrap chain is exempt). The upper
	// bound is still a ledger constraint, so it is always checked here to avoid
	// building a branch the verifier would reject. The lower bound is enforced in
	// Go only (the constant stays on the ledger) and is suppressible via
	// suppress_coverage_contribution_lower_bound for restart from an old snapshot.
	if t.SequencerID() != base.BoostrapSequencerID {
		lib := prop.SeqTxBuilder.Library
		coverage := prop.SeqTxBuilder.CurrentCoverageContribution()
		lower := lib.CoverageContributionLowerBound(t.targetTs.Slot)
		upper := lib.CoverageContributionUpperBound(t.targetTs.Slot)
		belowLower := coverage < lower && !t.SuppressCoverageContributionLowerBound()
		if belowLower || coverage > upper {
			if !t.slotData.coverageBoundsWarned {
				t.slotData.coverageBoundsWarned = true
				t.Log().Warnf("tryBranchProposal-%s: coverage contribution %s out of bounds [%s, %s] at slot %d, skipping branch",
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
		t.logFinalizeFailure("tryBranchProposal-"+t.Name, err)
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
		t.logFinalizeFailure("tryBaseExtendProposal-"+t.Name, err)
		return nil
	}
	return fp
}
