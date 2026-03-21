package factory

import (
	"errors"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const TraceTagChooseFirstPair = "factory_choosePair"

// chooseFirstExtendEndorsePair finds the first valid (extend, endorse) pair by traversing
// endorsement candidates sorted by coverage. Returns an IncrementalAttacher with 1 endorsement,
// or nil if no valid pair is found.
// Uses a synthetic timestamp at the end of the slot for candidate filtering (maximally permissive).
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	// synthetic ts at the end of the slot — pace checks are maximally permissive
	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))

	seqID := f.SequencerID()
	var ret *attacher.IncrementalAttacher

	for _, endorse := range endorseCandidates {
		select {
		case <-f.ctx.Done():
			return nil
		default:
		}

		baselineBranchID, ok := endorse.BaselineBranch()
		if !ok {
			continue
		}

		seqOut, err := f.Branches().GetChainOutputFromBranch(baselineBranchID, seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		f.AssertNoError(err)
		extendRoot := attacher.AttachOutputID(seqOut.ID, f)

		f.AddOwnMilestone(extendRoot.VID)
		futureConeMilestones := f.FutureConeOwnMilestonesOrdered(extendRoot, syntheticTs)

		f.Tracef(TraceTagChooseFirstPair, "check endorse %s against %d extension candidates",
			endorse.IDShortString, len(futureConeMilestones))

		ret = f.chooseBestExtendForEndorsement(endorse, futureConeMilestones, syntheticTs)
		if ret != nil {
			return ret
		}
	}
	return nil
}

// chooseBestExtendForEndorsement tries all extend candidates for a given endorsement.
// Returns the attacher with the biggest coverage, or nil.
func (f *Factory) chooseBestExtendForEndorsement(endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput, syntheticTs base.LedgerTime) *attacher.IncrementalAttacher {
	var best *attacher.IncrementalAttacher

	for _, extend := range extendCandidates {
		if f.checkedCombinations.isChecked(extend, nil, endorse) {
			continue
		}

		a, err := attacher.NewIncrementalAttacher("factory", f, syntheticTs, extend, endorse)
		f.checkedCombinations.markChecked(extend, nil, endorse)

		if err != nil {
			continue
		}
		if !a.Completed() {
			a.Close()
			continue
		}

		switch {
		case best == nil:
			best = a
		case a.FinalLedgerCoverage(syntheticTs) > best.FinalLedgerCoverage(syntheticTs):
			best.Close()
			best = a
		default:
			a.Close()
		}
	}
	return best
}
