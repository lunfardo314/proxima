package factory

import (
	"errors"
	"fmt"
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

	var d deadEnd
	defer func() {
		// A sequencer that consolidates nothing all slot long is the signature of a stalled
		// network, and every path out of here is a silent nil. Report it once per slot.
		if d.found {
			return
		}
		if f.lastDeadEndSlot.Swap(targetSlot) != targetSlot {
			f.Log().Warnf("[factory] no extend+endorse pair for slot %d: %s", targetSlot, d.String())
		}
	}()

	endorseCandidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	d.candidates = len(endorseCandidates)
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
			if ret := f.chooseBestExtendForEndorsement(endorse, []vertex.WrappedOutput{head}, syntheticTs, &d); ret != nil {
				d.found = true
				return ret
			}
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
		d.baselines++
		seqOut, err := f.Branches().GetChainOutputFromBranch(bc.branchID, seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			d.baselineNoChainOut++
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
		if ret := f.chooseBestExtendForEndorsement(bc.endorse, []vertex.WrappedOutput{extendRoot}, syntheticTs, &d); ret != nil {
			d.found = true
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

// deadEnd accumulates why a factory round found no (extend, endorse) pair. Every rejection
// below is a bare `continue`, so without this a stalled sequencer reports only "no proposals"
// and the actual reason — conflict, unresolved baseline, unsolid past cone — is invisible.
type deadEnd struct {
	candidates   int
	pairsTried   int
	skipped      int // already checked earlier in this slot
	notCompleted int // past cone not solid (noPull, may resolve later in the slot)
	attacherErr  int
	firstErr     error

	baselines          int // Phase 2: distinct baseline branches tried
	baselineNoChainOut int // Phase 2: own chain output absent from that branch's state
	found              bool
}

func (d *deadEnd) String() string {
	firstErr := "<none>"
	if d.firstErr != nil {
		firstErr = d.firstErr.Error()
	}
	return fmt.Sprintf("candidates=%d pairsTried=%d skipped=%d notCompleted=%d attacherErr=%d baselines=%d baselineNoChainOut=%d firstErr=%q",
		d.candidates, d.pairsTried, d.skipped, d.notCompleted, d.attacherErr, d.baselines, d.baselineNoChainOut, firstErr)
}

// chooseBestExtendForEndorsement tries all extend candidates for a given endorsement.
// Returns the attacher with the biggest coverage, or nil.
func (f *Factory) chooseBestExtendForEndorsement(endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput, syntheticTs base.LedgerTime, d *deadEnd) *attacher.IncrementalAttacher {
	var best *attacher.IncrementalAttacher

	for _, extend := range extendCandidates {
		if f.checkedCombinations.isChecked(extend, nil, endorse) {
			d.skipped++
			continue
		}
		d.pairsTried++

		a, err := attacher.NewIncrementalAttacher("factory", f, syntheticTs, extend, endorse)
		if err != nil {
			// conflict / no baseline: the pair is rejected on its own merits and will stay
			// rejected for this target slot, so remember it
			d.attacherErr++
			if d.firstErr == nil {
				d.firstErr = err
			}
			f.checkedCombinations.markChecked(extend, nil, endorse)
			continue
		}
		if !a.Completed() {
			d.notCompleted++
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
