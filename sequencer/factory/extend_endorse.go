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
//
// Backtrack guard: the newest extend candidate for an endorsement must either be recent enough
// to not risk TTL detachment (within targetSlot - 1), or absent from the memDAG entirely
// (virtual — will be pulled fresh). This prevents the sequencer from extending an old milestone
// on a stale branch when a newer one exists elsewhere, which would create a self-fork.
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	// synthetic ts at the end of the slot — pace checks are maximally permissive
	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))

	seqID := f.SequencerID()
	latestOwn := f.GetLatestMilestone(seqID)
	var latestOwnSlot uint32
	if latestOwn != nil {
		latestOwnSlot = latestOwn.Slot()
	}
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

		// backtrack guard: the newest extend candidate must be non-detachable
		// (at most 1 slot behind target) or not present in memDAG (virtual — will be pulled).
		if !f.extendCandidateIsSafe(futureConeMilestones, latestOwnSlot) {
			f.Tracef(TraceTagChooseFirstPair, "SKIP endorse %s: extend candidates too far behind latest own milestone (slot %d)",
				endorse.IDShortString, latestOwnSlot)
			continue
		}

		f.Tracef(TraceTagChooseFirstPair, "check endorse %s against %d extension candidates",
			endorse.IDShortString, len(futureConeMilestones))

		ret = f.chooseBestExtendForEndorsement(endorse, futureConeMilestones, syntheticTs)
		if ret != nil {
			return ret
		}
	}
	return nil
}

// extendCandidateIsSafe checks that the newest extend candidate is safe to use:
// - it must be absent from the memDAG (virtual — will be pulled fresh from txstore), OR
// - it must be recent enough relative to our latest own milestone, so that extending it
//   does not backtrack the chain and risk GC detachment during attachment.
// Returns false if the best extend candidate is an old in-memDAG vertex far behind
// our latest milestone.
func (f *Factory) extendCandidateIsSafe(candidates []vertex.WrappedOutput, latestOwnSlot uint32) bool {
	if len(candidates) == 0 {
		return false
	}
	newest := candidates[len(candidates)-1]
	// if vertex is not in memDAG (virtual), it will be pulled fresh — safe
	if newest.VID.IsVirtualTx() {
		return true
	}
	// if we don't have a latest own milestone, any candidate is acceptable
	if latestOwnSlot == 0 {
		return true
	}
	// the extend candidate must not be more than maxExtendBacktrackSlots behind
	// our latest own milestone. This prevents forking our own chain by reaching
	// back to a stale branch when a much newer milestone exists.
	newestSlot := newest.VID.Slot()
	return latestOwnSlot <= newestSlot+maxExtendBacktrackSlots
}

const (
	// maxExtendBacktrackSlots limits how far behind the latest own milestone the extend
	// candidate can be. Beyond this, the endorsement is skipped to avoid backtracking the
	// sequencer's chain. Must be well below vertexTTLSlots (24) to provide safety margin
	// against GC detachment during attachment.
	maxExtendBacktrackSlots = 3
)

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
