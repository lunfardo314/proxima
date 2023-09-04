package utangle

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (ut *UTXOTangle) SetLastPrunedOrphaned(t time.Time) {
	ut.lastPrunedOrphaned.Store(t)
}

func (ut *UTXOTangle) SinceLastPrunedOrphaned() time.Duration {
	return time.Since(ut.lastPrunedOrphaned.Load())
}

func (ut *UTXOTangle) SetLastCutFinal(t time.Time) {
	ut.lastCutFinal.Store(t)
}

func (ut *UTXOTangle) SinceLastCutFinal() time.Duration {
	return time.Since(ut.lastCutFinal.Load())
}

func _collectReachableSet(rootVID *WrappedTx, ret set.Set[*WrappedTx]) {
	if ret.Contains(rootVID) {
		return
	}
	ret.Insert(rootVID)
	rootVID.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if v.Tx.IsSequencerMilestone() {
				_collectReachableSet(v.StateDelta.baselineBranch, ret)
			}
			v.forEachInputDependency(func(_ byte, inp *WrappedTx) bool {
				_collectReachableSet(inp, ret)
				return true
			})
			v.forEachEndorsement(func(_ byte, vEnd *WrappedTx) bool {
				_collectReachableSet(vEnd, ret)
				return true
			})
		},
		Orphaned: func() {
			util.Panicf("_collectReachableSet: orphaned vertex reached %s", rootVID.IDShort())
		},
	})
}

// _reachableFromTipSet a set of vertices reachable from any of the vertex in the tip set
func _reachableFromTipList(tips []*WrappedTx) set.Set[*WrappedTx] {
	ret := set.New[*WrappedTx]()
	for _, vid := range tips {
		_collectReachableSet(vid, ret)
	}
	return ret
}

// _orphanedFromReachableSet no global lock
func (ut *UTXOTangle) _orphanedFromReachableSet(reachable set.Set[*WrappedTx], baselineTime time.Time) set.Set[*WrappedTx] {
	ret := set.New[*WrappedTx]()
	for _, vid := range ut.vertices {
		if !vid.Time().Before(baselineTime) {
			continue
		}
		if !reachable.Contains(vid) {
			ret.Insert(vid)
		}
	}
	return ret
}

// ReachableAndOrphaned used for testing
func (ut *UTXOTangle) ReachableAndOrphaned(nLatestSlots int) (set.Set[*WrappedTx], set.Set[*WrappedTx], time.Time) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	tipList, baselineTime, nSlots := ut._tipList(nLatestSlots)
	if nSlots != nLatestSlots {
		return nil, nil, time.Time{}
	}

	reachable := _reachableFromTipList(tipList)
	orphaned := ut._orphanedFromReachableSet(reachable, baselineTime)

	return reachable, orphaned, baselineTime
}

// PruneOrphaned acquires global lock and orphans all vertices not reachable from the top N branches
func (ut *UTXOTangle) PruneOrphaned(nLatestSlots int) (int, int, int) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	tipList, baselineTime, nSlots := ut._tipList(nLatestSlots)
	if nSlots != nLatestSlots {
		return 0, 0, 0
	}
	orphaned := ut._orphanedFromReachableSet(_reachableFromTipList(tipList), baselineTime)
	// delete from transaction dictionary
	orphaned.ForEach(func(vid *WrappedTx) bool {
		vid.MarkOrphaned()
		ut.deleteVertex(vid.ID())
		return true
	})
	// delete branches
	orphanedBranches := make([]*WrappedTx, 0)
	nPrunedBranches := 0
	toDeleteSlots := make([]core.TimeSlot, 0)
	for slot, branches := range ut.branches {
		orphanedBranches = orphanedBranches[:0]
		for vid := range branches {
			if orphaned.Contains(vid) {
				orphanedBranches = append(orphanedBranches, vid)
			}
		}
		for _, vid := range orphanedBranches {
			delete(branches, vid)
			ut.numDeletedBranches++
		}
		if len(branches) == 0 {
			toDeleteSlots = append(toDeleteSlots, slot)
		}
		nPrunedBranches += len(orphanedBranches)
	}

	for _, slot := range toDeleteSlots {
		delete(ut.branches, slot)
	}
	return len(orphaned), nPrunedBranches, len(toDeleteSlots)
}

func (ut *UTXOTangle) _oldestNonEmptySlot() (core.TimeSlot, int) {
	// ascending
	slots := util.FilterSlice(ut.timeSlotsOrdered(), func(el core.TimeSlot) bool {
		return len(ut.branches[el]) > 0
	})
	if len(slots) < TipSlots+2 {
		return 0, -1
	}
	return slots[0], len(ut.branches[slots[0]])
}

func (ut *UTXOTangle) CutFinalBranchIfExists(nLatestSlots int) (*core.TransactionID, int) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	slots := util.SortKeys(ut.branches, func(slot1, slot2 core.TimeSlot) bool {
		return slot1 < slot2
	})
	if len(slots) < nLatestSlots+2 {
		return nil, 0
	}
	util.Assertf(len(ut.branches[slots[0]]) >= 0, "no branches in slot %d", slots[0])

	if len(ut.branches[slots[0]]) > 1 {
		// more than one reachable oldest branches
		return nil, 0
	}

	br := util.MustTakeFirstKeyInMap(ut.branches[slots[0]])

	orderedPastCone := br.PastConeSet().Ordered(func(vid1, vid2 *WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	for _, vid := range orderedPastCone {
		vid.ConvertToVirtualTx() // cuts past cone from the tangle
	}
	delete(ut.branches, slots[0])

	return br.ID(), len(orderedPastCone)
}
