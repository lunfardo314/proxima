package factory

import (
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
)

const TraceTagChooseFirstPair = "factory_choosePair"

// chooseFirstExtendEndorsePair finds the first valid (extend, endorse) pair, returning an
// IncrementalAttacher with 1 endorsement, or nil if no valid pair is found. Uses a synthetic
// timestamp at the end of the slot for candidate filtering (maximally permissive).
//
// The extend candidate is the sequencer's own chain output, looked up in two phases:
//
//   - Phase 1 (head-first, memDAG): extend the NEWEST own milestone in the memDAG — the unspent
//     chain head — and take the first endorse candidate (coverage-descending) that reconciles with
//     it. This preserves the work already built into the head (its tag-along inputs); re-anchoring
//     to a committed output would orphan it. Only the head is tried: the older own memDAG outputs
//     are all already spent by the chain continuation, so they can only produce "already consumed"
//     conflicts. Trying them (and, worse, oldest-first) only wastes the round — for a sequencer
//     that never branches, whose head is always in an earlier slot than the target, that churn
//     reached the working head+branch-endorse pair too late and starved the round, stalling it.
//   - Phase 2 (re-anchor via branch state): fallback for when the head cannot be extended (e.g. it
//     double-spends against the consolidated state and is therefore orphaned). Read the own chain
//     output committed in an available branch and extend that (a VirtualTx), endorsing a candidate
//     on that branch's lineage — re-attaching to the consolidated lineage without the boot proposer.
//
// Both phases defer correctness to the incremental attacher: a double-spend (extending an
// already-spent output) surfaces as a conflict and the pair is skipped, so no heuristic
// backtrack guard is needed. Endorse candidates arrive coverage-descending, and Phase 2 branches
// are ordered committed-first then by that coverage to minimize trie reads.
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))
	if len(endorseCandidates) == 0 {
		return nil
	}

	// Phase 1: extend the chain head (newest own memDAG milestone). memDAGExtend is ascending, so
	// the head is the last element. A head in a later slot (the own branch of the next slot,
	// submitted while this round still targets the previous one) cannot be extended by a
	// milestone of the target slot: paired with this slot's endorsements it would make a
	// skeleton the ledger refuses as a cross-slot endorsement.
	memDAGExtend := f.OwnMilestoneOutputsInMemDAGAscending()
	var fromHead *attacher.IncrementalAttacher
	headOnTop := false
	if len(memDAGExtend) > 0 && memDAGExtend[len(memDAGExtend)-1].Timestamp().Slot <= targetSlot {
		head := memDAGExtend[len(memDAGExtend)-1]
		for i, endorse := range endorseCandidates {
			select {
			case <-f.ctx.Done():
				return nil
			default:
			}
			if ret := f.chooseBestExtendForEndorsement(endorse, []vertex.WrappedOutput{head}, syntheticTs); ret != nil {
				fromHead, headOnTop = ret, i == 0
				break
			}
		}
	}
	if headOnTop {
		// the head endorses the candidate with the most coverage: the chain is on the leading lineage
		return fromHead
	}

	// Otherwise the lineage the head is on may have fallen behind the one the leading candidate is
	// on, or the head cannot be extended at all. Re-anchoring is tried too, and whichever of the two
	// covers more seeds the round: a chain whose lineage lost after its first move keeps finding
	// pairs on it, the peers left there among the candidates, and without the comparison it would
	// never move to the lineage the others build on.
	reanchored := f.reanchorPair(targetSlot, endorseCandidates, memDAGExtend, syntheticTs)
	switch {
	case reanchored == nil:
		return fromHead
	case fromHead == nil:
		return reanchored
	case reanchored.FinalLedgerCoverage(syntheticTs) > fromHead.FinalLedgerCoverage(syntheticTs):
		f.Tracef(TraceTagChooseFirstPair, "re-anchor %s covers more than the head's pair %s", reanchored.Name, fromHead.Name)
		fromHead.Close()
		return reanchored
	default:
		reanchored.Close()
		return fromHead
	}
}

// reanchorPair returns the first valid pair that extends an own output of an earlier slot, the
// way the chain moves to another lineage. Extending an own milestone of the target slot again
// would fork the own chain within the slot.
func (f *Factory) reanchorPair(targetSlot uint32, endorseCandidates []*vertex.WrappedTx, memDAGExtend []vertex.WrappedOutput, syntheticTs base.LedgerTime) *attacher.IncrementalAttacher {
	// Phase 1b: re-anchor via the candidate's past cone. A newer own milestone than the one
	// committed in the candidate's branch is often already in the candidate's past cone (a peer
	// endorsed it), and extending the committed output would double-spend against it. The newest
	// own milestone the past cone references is the one whose chain output nothing in that cone
	// has consumed yet.
	for _, endorse := range endorseCandidates {
		select {
		case <-f.ctx.Done():
			return nil
		default:
		}
		if extend, ok := newestOwnInPastCone(memDAGExtend, endorse); ok && extend.Timestamp().Slot < targetSlot {
			if ret := f.chooseBestExtendForEndorsement(endorse, []vertex.WrappedOutput{extend}, syntheticTs); ret != nil {
				return ret
			}
		}
	}

	// Phase 2: re-anchor via branch state, on the own chain output committed in the candidate's
	// baseline branch. Many candidates share one; the output is read once per branch and slot.
	for _, bc := range f.rankedUniqueBaselines(endorseCandidates) {
		select {
		case <-f.ctx.Done():
			return nil
		default:
		}
		extend, ok := f.committedOwnOutput(bc.endorse)
		if !ok || extend.Timestamp().Slot >= targetSlot {
			continue
		}
		f.Tracef(TraceTagChooseFirstPair, "re-anchor: extend committed output %s from branch %s, endorse %s",
			extend.IDStringShort, bc.branchID.StringShort, bc.endorse.IDShortString)
		if ret := f.chooseBestExtendForEndorsement(bc.endorse, []vertex.WrappedOutput{extend}, syntheticTs); ret != nil {
			return ret
		}
	}
	return nil
}

// newestOwnInPastCone returns the newest of the own milestone outputs (ascending) that the
// candidate's past cone references.
func newestOwnInPastCone(ownAscending []vertex.WrappedOutput, candidate *vertex.WrappedTx) (vertex.WrappedOutput, bool) {
	for i := len(ownAscending) - 1; i >= 0; i-- {
		if candidate.PastConeReferences(ownAscending[i].VID) {
			return ownAscending[i], true
		}
	}
	return vertex.WrappedOutput{}, false
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
