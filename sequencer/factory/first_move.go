package factory

import (
	"context"
	"errors"
	"sync"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const (
	TraceTagFirstMove = "factory_firstMove"
	// MaxFirstMovePairs bounds the pairs attached in one first-move round. It covers today's
	// branches per slot times the three kinds of extension with room to spare.
	MaxFirstMovePairs = 48
)

type extendEndorsePair struct {
	extend  vertex.WrappedOutput
	endorse *vertex.WrappedTx
}

// firstMovePending tells whether the sequencer has no non-branch milestone in the target slot yet.
// Its next milestone is then the first move of the slot, the one that chooses the lineage.
func (f *Factory) firstMovePending(slot uint32) bool {
	ms := f.GetLatestMilestone(f.SequencerID())
	return ms == nil || ms.Slot() < slot || (ms.Slot() == slot && ms.IsBranchTransaction())
}

// searchFirstMove attaches every extend-endorse pair not tried yet in the slot and posts each
// one at least as good as the best so far, so that the first milestone is the best pair found
// by the time it is built, not the first one. Pairs are attached in parallel. A pair whose past
// cone is not solid yet is left for the next round, as are the pairs of branches arriving later.
func (f *Factory) searchFirstMove(roundCtx context.Context, slot uint32) {
	syntheticTs := base.T(slot, base.MaxTickValue)
	pairs := f.firstMovePairs(slot, syntheticTs)
	f.Tracef(TraceTagFirstMove, "slot %d: %d pairs to attach", slot, len(pairs))

	var mutex sync.Mutex // guards checkedCombinations and posting
	var wg sync.WaitGroup
	workers := make(chan struct{}, NumImprovementWorkers)

	for _, p := range pairs {
		if roundCtx.Err() != nil {
			break
		}
		workers <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() {
				<-workers
				wg.Done()
			}()
			a, err := attacher.NewIncrementalAttacher("factory-first", f, syntheticTs, p.extend, p.endorse)
			if err != nil {
				// conflict or no baseline: rejected for the whole slot
				mutex.Lock()
				f.checkedCombinations.markChecked(p.extend, nil, p.endorse)
				mutex.Unlock()
				return
			}
			defer a.Close()
			if !a.Completed() {
				// not solid yet: may become solid within the slot, retried next round
				return
			}
			coverage := a.FinalLedgerCoverage(syntheticTs)

			mutex.Lock()
			defer mutex.Unlock()
			f.checkedCombinations.markChecked(p.extend, nil, p.endorse)
			if f.tryPostSkeleton(a, coverage) {
				f.Tracef(TraceTagFirstMove, "posted %s, coverage: %d", a.Name(), coverage)
			}
		}()
	}
	wg.Wait()
}

// firstMovePairs lists the pairs not tried yet in the slot, endorse candidates in their
// coverage-descending order. For each candidate, the extensions are the own outputs the own past
// cone offers as the chain predecessor: the chain head, the newest own milestone the candidate's
// past cone references, and the own chain output committed in the candidate's baseline branch.
// The incremental attacher decides which of them are valid.
func (f *Factory) firstMovePairs(slot uint32, syntheticTs base.LedgerTime) []extendEndorsePair {
	candidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	own := f.OwnMilestoneOutputsInMemDAGAscending()

	// a head of a later slot (the own branch of the next slot) cannot be extended by a
	// milestone of this one
	var head *vertex.WrappedOutput
	if len(own) > 0 && own[len(own)-1].Timestamp().Slot <= slot {
		head = &own[len(own)-1]
	}

	ret := make([]extendEndorsePair, 0)
	planned := make(map[combinationKey]struct{})
	add := func(extend vertex.WrappedOutput, endorse *vertex.WrappedTx) {
		key := combinationHash(extend, nil, endorse)
		if _, already := planned[key]; already || f.checkedCombinations.isChecked(extend, nil, endorse) {
			return
		}
		planned[key] = struct{}{}
		ret = append(ret, extendEndorsePair{extend: extend, endorse: endorse})
	}

	for _, endorse := range candidates {
		if len(ret) >= MaxFirstMovePairs {
			break
		}
		if head != nil {
			add(*head, endorse)
		}
		if extend, ok := newestOwnInPastCone(own, endorse); ok {
			add(extend, endorse)
		}
		if extend, ok := f.committedOwnOutput(endorse); ok {
			add(extend, endorse)
		}
	}
	if len(ret) > MaxFirstMovePairs {
		ret = ret[:MaxFirstMovePairs]
	}
	return ret
}

// committedOwnOutput returns the own chain output committed in the baseline branch of the
// candidate, attached with its output so that the incremental attacher, which does not pull,
// can complete with it. Read once per branch and target slot.
func (f *Factory) committedOwnOutput(endorse *vertex.WrappedTx) (vertex.WrappedOutput, bool) {
	branchID, ok := endorse.BaselineBranch()
	if !ok {
		return vertex.WrappedOutput{}, false
	}
	if wOut, already := f.committedOwn[branchID]; already {
		return wOut, wOut.VID != nil
	}
	seqOut, err := f.Branches().GetChainOutputFromBranch(branchID, f.SequencerID())
	if errors.Is(err, multistate.ErrNotFound) {
		f.committedOwn[branchID] = vertex.WrappedOutput{}
		return vertex.WrappedOutput{}, false
	}
	f.AssertNoError(err)
	wOut := attacher.AttachOutputWithID(*seqOut, f, attacher.WithInvokedBy(TraceTagFirstMove))
	f.AddOwnMilestone(wOut.VID)
	f.committedOwn[branchID] = wOut
	return wOut, true
}
