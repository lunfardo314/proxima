package factory

import (
	"errors"
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const TraceTagChooseFirstPair = "factory_choosePair"

// reanchorGainPermille is how much heavier a re-anchored lineage must be before the sequencer
// leaves the one its own chain is on. Leaving orphans the milestones already built on that chain,
// so it is only worth doing for a materially better lineage.
//
// The bound exists because both extremes have been observed to fail. With no threshold at all —
// take whichever is heavier — sequencers chased each other between lineages and the network's
// coverage oscillated, because sibling branches of a slot are equalised by pre-branch
// consolidation to within about a thousandth of a percent and that difference is noise. With no
// re-anchor at all unless extending own state was impossible, sequencers sat a whole slot on a
// branch holding two thirds of the coverage their peers had, because a sequencer with any peer on
// its own lineage always found something to extend and never reconsidered. One percent is far
// above the noise and far below a real divergence.
const reanchorGainPermille = 10

// chooseFirstExtendEndorsePair finds a valid (extend, endorse) pair, returning an
// IncrementalAttacher with 1 endorsement, or nil if none is found. Uses a synthetic timestamp at
// the end of the slot for candidate filtering (maximally permissive).
//
// Two sources of the extend, which is always an own chain output:
//
//   - Own past cone: any own output the heuristic offers, not only the chain head. Extending an
//     output older than the head orphans the milestones built on it, and that revert is how a
//     sequencer resolves a conflict — a move the search must be able to make, not an exception.
//   - Re-anchor via branch state: the own chain output as committed in a candidate's baseline
//     branch, extended as a VirtualTx. This is the only way onto a lineage the own chain cannot
//     reach, since sibling branches conflict over the parent's stem.
//
// Both are searched and the re-anchor wins only by reanchorGainPermille, so a sequencer stays put
// unless leaving is clearly worth it. Within each source the first workable pair is taken: the
// deadline is what limits the sequencer, not the CPU, so a usable skeleton is produced at once
// and improved afterwards.
//
// The heuristic supplies both orders. Correctness is deferred to the incremental attacher
// throughout: a double-spend (extending an already-spent output) surfaces as a conflict and the
// pair is skipped.
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d heuristic=%s", targetSlot, f.h.name)

	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.h.endorseCandidates(f, syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "[%s] endorse candidates: %d", f.h.name, len(endorseCandidates))
	if len(endorseCandidates) == 0 {
		return nil
	}
	seqID := f.SequencerID()

	// extend own state: every own output the heuristic offers, in its order
	var fromOwn *attacher.IncrementalAttacher
	if ownExtend := f.h.ownExtendCandidates(f); len(ownExtend) > 0 {
		for _, endorse := range endorseCandidates {
			select {
			case <-f.ctx.Done():
				return nil
			default:
			}
			if fromOwn = f.chooseBestExtendForEndorsement(endorse, ownExtend, syntheticTs); fromOwn != nil {
				break
			}
		}
	}

	// re-anchor via branch state. Dedup the baseline branches (many endorse candidates share one)
	// and read each at most once, committed-before-pending.
	var fromReanchor *attacher.IncrementalAttacher
	for _, bc := range f.rankedUniqueBaselines(endorseCandidates) {
		select {
		case <-f.ctx.Done():
			return firstNonNil(fromOwn, fromReanchor)
		default:
		}
		seqOut, ok := f.ownChainOutputInBranch(bc.branchID, seqID)
		if !ok {
			continue
		}
		// Attach WITH the output just read from the branch. Attaching by ID alone would leave a
		// VirtualTx carrying no output, and the incremental attacher never pulls (noPull) — it
		// skips a not-yet-solid input instead — so such a candidate could never complete.
		extendRoot := attacher.AttachOutputWithID(*seqOut, f, attacher.WithInvokedBy(TraceTagChooseFirstPair))
		f.AddOwnMilestone(extendRoot.VID)
		f.Tracef(TraceTagChooseFirstPair, "re-anchor: extend committed output %s from branch %s, endorse %s",
			extendRoot.IDStringShort, bc.branchID.StringShort, bc.endorse.IDShortString)
		if fromReanchor = f.chooseBestExtendForEndorsement(bc.endorse, []vertex.WrappedOutput{extendRoot}, syntheticTs); fromReanchor != nil {
			break
		}
	}

	switch {
	case fromReanchor == nil:
		return fromOwn
	case fromOwn == nil:
		return fromReanchor
	}
	own := fromOwn.FinalLedgerCoverage(syntheticTs)
	re := fromReanchor.FinalLedgerCoverage(syntheticTs)
	if re > own+own/1000*reanchorGainPermille {
		f.Tracef(TraceTagChooseFirstPair, "[%s] re-anchoring: %d -> %d", f.h.name, own, re)
		fromOwn.Close()
		return fromReanchor
	}
	fromReanchor.Close()
	return fromOwn
}

func firstNonNil(a, b *attacher.IncrementalAttacher) *attacher.IncrementalAttacher {
	if a != nil {
		if b != nil {
			b.Close()
		}
		return a
	}
	return b
}

// ownChainOutputInBranch reads the sequencer's own chain output as committed in a branch,
// memoised for the target slot. Committed branch state does not change, and the re-anchor is now
// evaluated every round rather than only as a fallback, so without the memo this would repeat the
// same trie reads a couple of hundred times a slot.
func (f *Factory) ownChainOutputInBranch(branchID base.TransactionID, seqID base.ChainID) (*ledger.OutputWithID, bool) {
	if o, memoised := f.chainOutInBranch[branchID]; memoised {
		return o, o != nil
	}
	o, err := f.Branches().GetChainOutputFromBranch(branchID, seqID)
	if err != nil && !errors.Is(err, multistate.ErrNotFound) {
		f.AssertNoError(err)
	}
	if errors.Is(err, multistate.ErrNotFound) {
		o = nil
	}
	f.chainOutInBranch[branchID] = o
	return o, o != nil
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
		// check and mark in one step: the factories of a group share this set, and two racing on
		// the same combination would otherwise both build it
		if f.sh.combinations.checkAndMark(extend, nil, endorse) {
			continue
		}

		a, err := attacher.NewIncrementalAttacher("factory", f, syntheticTs, extend, endorse)
		if err != nil {
			// conflict / no baseline: the pair is rejected on its own merits and stays rejected
			// for this target slot, so leave it marked
			continue
		}
		if !a.Completed() {
			// the past cone is not solid yet. With noPull that is not resolved here but may
			// resolve on its own within the slot, so leave the pair unmarked and retry it —
			// marking would discard it for the whole slot over a passing condition.
			f.sh.combinations.unmark(extend, nil, endorse)
			a.Close()
			continue
		}

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
