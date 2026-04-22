package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/util"
)

// tryBootProposal generates a non-branch transaction with an explicit baseline (LRB)
// when the own latest milestone is more than 1 slot in the past.
// This bootstraps the network: when all sequencer start UTXOs are far in the past,
// there's nothing to endorse, so the boot proposer bypasses endorsement by setting
// an explicit baseline branch. Once several sequencers produce boot transactions,
// they can endorse each other and coverage starts growing.
// Returns nil when the boot condition is not met (normal operation).

const TraceTagBootProposer = "propose-boot"

func (t *taskData) tryBootProposal() *finalProposal {
	extend := t.OwnLatestMilestoneOutput()
	if extend.VID == nil {
		t.Log().Warnf("BootProposer-%s: can't find own latest milestone output", t.Name)
		return nil
	}

	if t.targetTs.IsSlotBoundary() || extend.VID.Slot()+1 >= t.targetTs.Slot {
		// not in boot condition: milestone is recent enough
		t.Tracef(TraceTagBootProposer, "idle phase(%s). target: %s, extend: %s", t.Name, t.targetTs.String, extend.IDStringShort)
		return nil
	}

	lrb := t.Branches().FindLatestReliableBranch()
	if lrb == nil {
		t.Log().Warnf("BootProposer-%s: can't find latest reliable branch", t.Name)
		return nil
	}

	// explicit baseline must be in a past slot (ledger constraint)
	if lrb.Stem.ID.Slot() >= t.targetTs.Slot {
		t.Tracef(TraceTagBootProposer, "%s LRB slot %d >= target slot %d, skipping", t.Name, lrb.Stem.ID.Slot(), t.targetTs.Slot)
		return nil
	}

	a, err := attacher.NewIncrementalAttacherWithExplicitBaseline(t.Name, t.environment, t.targetTs, extend, lrb.Stem.ID.TransactionID())
	if err != nil {
		t.Tracef(TraceTagBootProposer, "%s can't create attacher: '%v'", t.Name, err)
		return nil
	}
	t.Tracef(TraceTagBootProposer, "%s created attacher with baseline %s, cov: %s",
		t.Name, a.BaselineBranch().StringShort, func() string { return util.Th(a.FinalLedgerCoverage(t.targetTs)) },
	)

	prop, err := t.newProposal(a)
	if err != nil {
		t.Tracef(TraceTagBootProposer, "%s can't create proposal: '%v'", t.Name, err)
		return nil
	}

	fp, err := prop.finalize("boot")
	if err != nil {
		t.Log().Warnf("BootProposer-%s: finalize failed: %v", t.Name, err)
		return nil
	}
	lrbTxID := lrb.Stem.ID.TransactionID()
	t.Log().Warnf("BootProposer-%s: FIRED target=%s extend=%s extSlot=%d baselineLRB=%s",
		t.Name, t.targetTs.String(), extend.IDStringShort(), extend.VID.Slot(), lrbTxID.StringShort())
	return fp
}
