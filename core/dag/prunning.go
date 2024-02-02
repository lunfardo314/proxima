package dag

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

// _topList returns a set of vertices which belongs to top nSlots slots and oldest slot in the top list
func (d *DAG) _topList(verticesDescending []*vertex.WrappedTx, nTopSlots int) (ledger.Slot, []*vertex.WrappedTx) {
	topSlots := set.New[ledger.Slot]()
	for _, vid := range verticesDescending {
		topSlots.Insert(vid.Slot())
		if len(topSlots) == nTopSlots {
			break
		}
	}
	pruningSlot := util.Minimum(maps.Keys(topSlots), func(s1, s2 ledger.Slot) bool {
		return s1 < s2
	})
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.Slot() < pruningSlot {
			break
		}
		ret = append(ret, vid)
	}
	return pruningSlot, ret
}

func (d *DAG) badVerticesOlderThan(ts ledger.LogicalTime) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range d.Vertices() {
		if ts.Before(vid.Timestamp()) && vid.IsBadOrDeleted() {
			ret = append(ret, vid)
		}
	}
	return ret
}

func _collectReachableSet(rootVID *vertex.WrappedTx, ret set.Set[*vertex.WrappedTx]) {
	if ret.Contains(rootVID) {
		return
	}
	ret.Insert(rootVID)
	rootVID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			v.ForEachInputDependency(func(_ byte, inp *vertex.WrappedTx) bool {
				_collectReachableSet(inp, ret)
				return true
			})
			v.ForEachEndorsement(func(_ byte, vEnd *vertex.WrappedTx) bool {
				_collectReachableSet(vEnd, ret)
				return true
			})
		},
		Deleted: func() {
			util.Panicf("_collectReachableSet: orphaned vertex reached %s", rootVID.IDShortString())
		},
	})
}

// _reachableFromTipSet a set of dag reachable from any of the vertex in the top set
func _reachableFromTopList(topList []*vertex.WrappedTx) set.Set[*vertex.WrappedTx] {
	ret := set.New[*vertex.WrappedTx](topList...)
	for _, vid := range topList {
		_collectReachableSet(vid, ret)
	}
	return ret
}

// _orphanedFromReachableSet no global lock while traversing
// Returns vertices not reachable and older than baseline real time
func _orphanedOrBadFromReachableSet(verticesDescending []*vertex.WrappedTx, reachable set.Set[*vertex.WrappedTx], oldestSlotInTopList ledger.Slot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.GetTxStatus() == vertex.Bad {
			ret = append(ret, vid)
		}
		if vid.Slot() >= oldestSlotInTopList {
			continue
		}
		if !reachable.Contains(vid) {
			ret = append(ret, vid)
		}
	}
	return ret
}

func (d *DAG) OrphanedAndBadVertices(nTopSlots int) []*vertex.WrappedTx {
	verticesDescending := d.VerticesDescending()
	oldestSlotInTopList, top := d._topList(verticesDescending, nTopSlots)
	reachable := _reachableFromTopList(top)
	return _orphanedOrBadFromReachableSet(verticesDescending, reachable, oldestSlotInTopList)
}
