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

// chooseBestExtendEndorsePair finds the (extend, endorse) pair with the biggest coverage,
// returning an IncrementalAttacher with 1 endorsement, or nil if no valid pair is found. Uses a
// synthetic timestamp at the end of the slot for candidate filtering (maximally permissive).
//
// The extend candidate is the sequencer's own chain output, sourced two ways:
//
//   - Own chain head (memDAG): extend the NEWEST own milestone — the unspent chain head. This
//     preserves the work already built into the head (its tag-along inputs). Only the head is
//     tried: the older own memDAG outputs are all already spent by the chain continuation, so
//     they can only produce "already consumed" conflicts.
//   - Re-anchor via branch state: read the own chain output committed in an available branch and
//     extend that (a VirtualTx), endorsing a candidate on that branch's lineage. This is how a
//     sequencer leaves a lineage, and it orphans its own head to do so.
//
// Both sources compete on coverage and the heavier pair wins. Trying the head first and taking
// its first success meant leaving a lineage was never weighed against staying on it: a sequencer
// kept whatever lineage it was on for as long as any peer there remained endorsable, and moved
// only once staying had become impossible. Sequencers therefore sat on measurably lighter
// branches for whole slots, and a network split into two lineages sustained itself, each side
// always having someone of its own to endorse.
//
// Both sources defer correctness to the incremental attacher: a double-spend (extending an
// already-spent output) surfaces as a conflict and the pair is skipped, so no heuristic
// backtrack guard is needed.
func (f *Factory) chooseBestExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))
	if len(endorseCandidates) == 0 {
		return nil
	}
	seqID := f.SequencerID()

	var best *attacher.IncrementalAttacher
	// keeps the heavier of the two and closes the loser. On equal coverage the incumbent wins, so
	// the sequencer does not orphan its own head for nothing.
	keepBest := func(cand *attacher.IncrementalAttacher) {
		switch {
		case cand == nil:
		case best == nil:
			best = cand
		case cand.FinalLedgerCoverage(syntheticTs) > best.FinalLedgerCoverage(syntheticTs):
			best.Close()
			best = cand
		default:
			cand.Close()
		}
	}

	// Own chain head. memDAGExtend is ascending, so the head is the last element.
	if memDAGExtend := f.OwnMilestoneOutputsInMemDAGAscending(); len(memDAGExtend) > 0 {
		head := memDAGExtend[len(memDAGExtend)-1]
		for _, endorse := range endorseCandidates {
			select {
			case <-f.ctx.Done():
				return best
			default:
			}
			keepBest(f.chooseBestExtendForEndorsement(endorse, []vertex.WrappedOutput{head}, syntheticTs))
		}
	}

	// Re-anchor via branch state. Dedup the baseline branches (many endorse candidates share one)
	// and read each at most once, committed-before-pending and coverage-descending.
	for _, bc := range f.rankedUniqueBaselines(endorseCandidates) {
		select {
		case <-f.ctx.Done():
			return best
		default:
		}
		seqOut, memoised := f.chainOutInBranch[bc.branchID]
		if !memoised {
			var err error
			seqOut, err = f.Branches().GetChainOutputFromBranch(bc.branchID, seqID)
			if err != nil && !errors.Is(err, multistate.ErrNotFound) {
				f.AssertNoError(err)
			}
			if errors.Is(err, multistate.ErrNotFound) {
				seqOut = nil
			}
			f.chainOutInBranch[bc.branchID] = seqOut
		}
		if seqOut == nil {
			continue
		}
		// Attach WITH the output just read from the branch. Attaching by ID alone would leave a
		// VirtualTx carrying no output, and the incremental attacher never pulls (noPull) — it
		// skips a not-yet-solid input instead — so such a candidate could never complete.
		extendRoot := attacher.AttachOutputWithID(*seqOut, f, attacher.WithInvokedBy(TraceTagChooseFirstPair))
		f.AddOwnMilestone(extendRoot.VID)
		f.Tracef(TraceTagChooseFirstPair, "re-anchor: extend committed output %s from branch %s, endorse %s",
			extendRoot.IDStringShort, bc.branchID.StringShort, bc.endorse.IDShortString)
		keepBest(f.chooseBestExtendForEndorsement(bc.endorse, []vertex.WrappedOutput{extendRoot}, syntheticTs))
	}
	return best
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
