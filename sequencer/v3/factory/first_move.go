package factory

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

const (
	TraceTagFirstMove = "factory_firstMove"
	// MaxFirstMovePairs bounds the endorse candidates tried in one first-move round. It covers
	// today's branches per slot with room to spare.
	MaxFirstMovePairs = 48
)

// firstMoveChoice is one endorse candidate (nil: stay on the own branch) with the own outputs that
// may extend it, newest first.
type firstMoveChoice struct {
	extends []vertex.WrappedOutput
	endorse *vertex.WrappedTx
}

// firstMovePending tells whether the sequencer has no non-branch milestone in the target slot yet.
// Its next milestone is then the first move of the slot, the one that chooses the lineage.
func (f *Factory) firstMovePending(slot uint32) bool {
	ms := f.GetLatestMilestone(f.SequencerID())
	return ms == nil || ms.Slot() < slot || (ms.Slot() == slot && ms.IsBranchTransaction())
}

// searchFirstMove attaches the choices not tried yet in the slot and posts each one at least as
// good as the best so far, so that the first milestone is the best pair found by the time it is
// built, not the first one. Choices are attached in parallel. For one endorse candidate the newest
// own output that extends it validly is taken and the older ones are not tried: spending an own
// output again while a newer own milestone that spent it can be extended forks the own chain, and
// the two spends make the lineage conflict with every past cone holding the newer one
// (kb/research/split_slots.md). A choice whose past cone is not solid yet is left for the next
// round, as are the choices of branches arriving later. The first move of the post-branch zone,
// kb/sequencer_search.md.
func (f *Factory) searchFirstMove(roundCtx context.Context, slot uint32) {
	syntheticTs := base.T(slot, base.MaxTickValue)
	choices := f.firstMoveChoices(slot, syntheticTs)
	f.Tracef(TraceTagFirstMove, "slot %d: %d choices to attach", slot, len(choices))

	var mutex sync.Mutex // guards checkedCombinations and posting
	var wg sync.WaitGroup
	workers := make(chan struct{}, NumImprovementWorkers)

	for _, c := range choices {
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
			f.attachFirstMoveChoice(roundCtx, slot, syntheticTs, c, &mutex)
		}()
	}
	wg.Wait()
}

// attachFirstMoveChoice attaches the extensions of one choice newest first and posts the first valid
// one. An extension not solid yet ends the choice for this round without falling back to an older
// one; the older ones are marked tried once a newer one is taken.
func (f *Factory) attachFirstMoveChoice(roundCtx context.Context, slot uint32, syntheticTs base.LedgerTime, c firstMoveChoice, mutex *sync.Mutex) {
	var endorse []*vertex.WrappedTx
	endorsed := "none"
	if c.endorse != nil {
		endorse = append(endorse, c.endorse)
		endorsed = c.endorse.IDShortString()
	}
	for i, extend := range c.extends {
		mutex.Lock()
		checked := f.checkedCombinations.isChecked(extend, nil, c.endorse)
		mutex.Unlock()
		if checked {
			continue
		}
		a, err := attacher.NewIncrementalAttacher("factory-first", f, syntheticTs, extend, endorse...)
		if err != nil {
			// conflict or no baseline: rejected for the whole slot, try the next older extension
			mutex.Lock()
			f.checkedCombinations.markChecked(extend, nil, c.endorse)
			mutex.Unlock()
			continue
		}
		if !a.Completed() {
			// not solid yet: may become solid within the slot, retried next round
			a.Close()
			return
		}
		value := RankValue(f, a, syntheticTs)

		mutex.Lock()
		for _, e := range c.extends[i:] {
			f.checkedCombinations.markChecked(e, nil, c.endorse)
		}
		posted := f.tryPostSkeleton(roundCtx, a, value, true)
		mutex.Unlock()
		a.Close()
		f.Tracef(TraceTagFirstMove, "FIRSTMOVE slot %d extend %s endorse %s value %d posted %v",
			slot, extend.IDStringShort(), endorsed, value, posted)
		return
	}
}

// firstMoveChoices lists the choices not tried yet in the slot, endorse candidates in their
// coverage-descending order. For each candidate, the extensions are the own outputs the own past
// cone offers as the chain predecessor: the chain head, the newest own non-branch milestone of an
// earlier slot, the newest own milestone the candidate's past cone references, and the own chain
// output committed in the candidate's baseline branch. After a lost branch race the head is the
// own losing branch, which cannot extend the winner, and the winner's past cone does not reference
// the own milestones issued after its cut: the newest non-branch milestone, the losing branch's
// chain predecessor, is how the chain keeps them. Staying on the own branch of the slot, extending
// it with no endorsement, is one more choice. The incremental attacher decides which are valid.
func (f *Factory) firstMoveChoices(slot uint32, syntheticTs base.LedgerTime) []firstMoveChoice {
	candidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
	own := f.OwnMilestoneOutputsInMemDAGAscending()

	// a head of a later slot (the own branch of the next slot) cannot be extended by a
	// milestone of this one
	var head, late *vertex.WrappedOutput
	if len(own) > 0 && own[len(own)-1].Timestamp().Slot <= slot {
		head = &own[len(own)-1]
	}
	for i := len(own) - 1; i >= 0; i-- {
		if own[i].Timestamp().Slot < slot && !own[i].VID.IsBranchTransaction() {
			late = &own[i]
			break
		}
	}

	ret := make([]firstMoveChoice, 0)
	if head != nil && head.VID.IsBranchTransaction() && head.Timestamp().Slot == slot &&
		!f.checkedCombinations.isChecked(*head, nil, nil) {
		ret = append(ret, firstMoveChoice{extends: []vertex.WrappedOutput{*head}})
	}
	for _, endorse := range candidates {
		if len(ret) >= MaxFirstMovePairs {
			break
		}
		var extends []vertex.WrappedOutput
		add := func(extend vertex.WrappedOutput) {
			for _, e := range extends {
				if e == extend {
					return
				}
			}
			extends = append(extends, extend)
		}
		if head != nil {
			add(*head)
		}
		if late != nil {
			add(*late)
		}
		if extend, ok := newestOwnInPastCone(own, endorse); ok {
			add(extend)
		}
		if extend, ok := f.committedOwnOutput(endorse); ok {
			add(extend)
		}
		slices.SortStableFunc(extends, func(a, b vertex.WrappedOutput) int {
			switch {
			case b.Timestamp().Before(a.Timestamp()):
				return -1
			case a.Timestamp().Before(b.Timestamp()):
				return 1
			}
			return 0
		})
		for _, e := range extends {
			if !f.checkedCombinations.isChecked(e, nil, endorse) {
				ret = append(ret, firstMoveChoice{extends: extends, endorse: endorse})
				break
			}
		}
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
