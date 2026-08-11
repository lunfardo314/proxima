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
// First-fit on purpose: the deadline is what limits the sequencer, not the CPU, so a factory
// returns a usable skeleton as soon as it has one and improves it afterwards. Which skeleton the
// sequencer actually uses is decided later, by score, among everything the group posts — so the
// searches stay cheap and the choosing happens in one place.
//
// The heuristic supplies both orders. Extend candidates come from the whole own past cone, not
// just its head: extending an earlier own output orphans the milestones built on it, and that
// revert is how a sequencer resolves a conflict — a normal move, not an exception. Endorse
// candidates are ordered by the heuristic too, greedily by coverage or shuffled.
//
// Falls back to re-anchoring on a branch's committed state when no own output can be extended,
// which reattaches the chain to a lineage its head cannot reach.
//
// Correctness is deferred to the incremental attacher throughout: a double-spend (extending an
// already-spent output) surfaces as a conflict and the pair is skipped.
func (f *Factory) chooseFirstExtendEndorsePair(targetSlot uint32) *attacher.IncrementalAttacher {
	f.Tracef(TraceTagChooseFirstPair, "IN slot=%d", targetSlot)

	syntheticTs := base.T(targetSlot, base.MaxTickValue)

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	f.Tracef(TraceTagChooseFirstPair, "endorse candidates: %d", len(endorseCandidates))
	if len(endorseCandidates) == 0 {
		return nil
	}
	seqID := f.SequencerID()

	// Phase 1: extend the chain head (newest own memDAG milestone). memDAGExtend is ascending, so
	// the head is the last element.
	if memDAGExtend := f.OwnMilestoneOutputsInMemDAGAscending(); len(memDAGExtend) > 0 {
		head := memDAGExtend[len(memDAGExtend)-1]
		for _, endorse := range endorseCandidates {
			select {
			case <-f.ctx.Done():
				return nil
			default:
			}
			if ret := f.chooseBestExtendForEndorsement(endorse, []vertex.WrappedOutput{head}, syntheticTs); ret != nil {
				return ret
			}
		}
	}

	// Re-anchor via branch state: no own output could be extended. Dedup the baseline branches
	// (many endorse candidates share one) and read each at most once, committed-before-pending.
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

	var bestScore uint64
	for _, extend := range extendCandidates {
		// check and mark in one step: the factories of a group share this set, and two of them
		// racing on the same combination would otherwise both build it
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
			// resolve on its own within the slot, so unmark and retry it later — keeping it
			// marked would discard it for the whole slot over a passing condition.
			f.sh.combinations.unmark(extend, nil, endorse)
			a.Close()
			continue
		}

		if sc := f.score(a, syntheticTs); best == nil || sc >= bestScore {
			if best != nil {
				best.Close()
			}
			best, bestScore = a, sc
		} else {
			a.Close()
		}
	}
	return best
}
