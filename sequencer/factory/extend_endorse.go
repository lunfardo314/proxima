package factory

import (
	"errors"
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const TraceTagChooseFirstPair = "factory_choosePair"

// chooseFirstExtendEndorsePair finds the first valid (extend, endorse) pair, returning an
// IncrementalAttacher with 1 endorsement, or nil if no valid pair is found. Uses a synthetic
// timestamp at the end of the slot for candidate filtering (maximally permissive).
//
// The extend candidate is the sequencer's own chain output. It is looked up in two phases, so
// the common case is served without touching branch state:
//
//   - Phase 1 (memDAG-first): extend the sequencer's own milestone outputs already live in the
//     memDAG, endorsing the highest-coverage candidate that reconciles. No state reads. The
//     candidate set is not narrowed by slot: an own tip several slots back is exactly the case
//     that must still be extendable, otherwise a single missed slot compounds into a stall.
//   - Phase 2 (re-anchor via branch state): only when Phase 1 finds no pair — i.e. the own tip
//     is missing or un-reconcilable in the memDAG (e.g. abandoned past its TTL). Read the own
//     chain output committed in an available branch and extend that (a VirtualTx), endorsing a
//     candidate on that branch's lineage. This is what lets a sequencer that has fallen off its
//     own chain re-attach to the consolidated lineage without waiting for the boot proposer.
//
// Both phases defer correctness to the incremental attacher: a double-spend (extending an
// already-spent output) surfaces as a conflict and the pair is skipped, so no heuristic
// backtrack guard is needed. The search follows biggest coverage: endorse candidates arrive
// coverage-descending, and Phase 2 branches are ordered committed-first then by that coverage
// to minimize trie reads.
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))
	if len(endorseCandidates) == 0 {
		return nil
	}
	seqID := f.SequencerID()

	// Phase 1: memDAG-first.
	memDAGExtend := f.OwnMilestoneOutputsInMemDAGAscending()
	for _, endorse := range endorseCandidates {
		select {
		case <-f.ctx.Done():
			return nil
		default:
		}
		if ret := f.chooseBestExtendForEndorsement(endorse, memDAGExtend, syntheticTs); ret != nil {
			return ret
		}
	}

	// Phase 2: re-anchor via branch state. Dedup the baseline branches (many endorse candidates
	// share one) and read each at most once, committed-before-pending and coverage-descending.
	for _, bc := range f.rankedUniqueBaselines(endorseCandidates) {
		select {
		case <-f.ctx.Done():
			return nil
		default:
		}
		seqOut, err := f.Branches().GetChainOutputFromBranch(bc.branchID, seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		f.AssertNoError(err)
		// Attach WITH the output just read from the branch. Attaching by ID alone would leave a
		// VirtualTx carrying no output, and the incremental attacher never pulls (noPull) — it
		// skips a not-yet-solid input instead — so such a candidate could never complete.
		extendRoot := attacher.AttachOutputWithID(*seqOut, f, attacher.WithInvokedBy(TraceTagChooseFirstPair))
		f.AddOwnMilestone(extendRoot.VID)
		f.Tracef(TraceTagChooseFirstPair, "re-anchor: extend committed output %s from branch %s, endorse %s",
			extendRoot.IDStringShort, bc.branchID.StringShort, bc.endorse.IDShortString)
		if ret := f.chooseBestExtendForEndorsement(bc.endorse, []vertex.WrappedOutput{extendRoot}, syntheticTs); ret != nil {
			return ret
		}
	}
	return nil
}

// baselineCand pairs a unique baseline branch with a representative endorse candidate on its
// lineage (the highest-coverage one, since endorseCandidates arrive coverage-descending).
type baselineCand struct {
	branchID base.TransactionID
	endorse  *vertex.WrappedTx
	pending  bool
}

// rankedUniqueBaselines dedups the baseline branches across endorse candidates and orders them
// committed-before-pending, preserving the input coverage-descending order within each group, so
// Phase 2 reads the cheapest (already committed) and highest-coverage branch state first.
func (f *Factory) rankedUniqueBaselines(endorseCandidates []*vertex.WrappedTx) []baselineCand {
	seen := make(map[base.TransactionID]struct{}, len(endorseCandidates))
	ret := make([]baselineCand, 0, len(endorseCandidates))
	for _, e := range endorseCandidates {
		bid, ok := e.BaselineBranch()
		if !ok {
			continue
		}
		if _, already := seen[bid]; already {
			continue
		}
		seen[bid] = struct{}{}
		ret = append(ret, baselineCand{branchID: bid, endorse: e, pending: f.Branches().IsPending(bid)})
	}
	sort.SliceStable(ret, func(i, j int) bool {
		return !ret[i].pending && ret[j].pending // committed first; coverage order preserved within groups
	})
	return ret
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
		if err != nil {
			// conflict / no baseline: the pair is rejected on its own merits and will stay
			// rejected for this target slot, so remember it
			f.checkedCombinations.markChecked(extend, nil, endorse)
			continue
		}
		if !a.Completed() {
			// the past cone is not solid yet. With noPull that is not resolved here but may
			// resolve on its own within the slot, so leave the pair unmarked and retry it —
			// marking would discard it for the whole slot over a passing condition.
			a.Close()
			continue
		}
		f.checkedCombinations.markChecked(extend, nil, endorse)

		switch {
		case best == nil:
			best = a
		// Tiebreaker: >= (not >) so later (newer-timestamp) candidates replace earlier
		// (older) ones at equal coverage. extendCandidates is ordered oldest-first, so
		// this picks the newest tip on tie — avoids generating siblings off an old output
		// when a newer chain tip with the same coverage is available.
		case a.FinalLedgerCoverage(syntheticTs) >= best.FinalLedgerCoverage(syntheticTs):
			best.Close()
			best = a
		default:
			a.Close()
		}
	}
	return best
}
