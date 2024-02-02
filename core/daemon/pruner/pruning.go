package pruner

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

type (
	Environment interface {
		global.Logging
	}

	Pruner struct {
		*dag.DAG
		Environment
	}
)

func New(dag *dag.DAG, env Environment) *Pruner {
	return &Pruner{
		DAG:         dag,
		Environment: env,
	}
}

func (p *Pruner) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	go func() {
		p.mainLoop(ctx)
		doneOnClose.Wait()
		p.Log().Infof("DAG pruner STOPPED")
	}()
}

// _topList returns a descending list of vertices which belongs to top nSlots slots, also the oldest slot in the top list
func (p *Pruner) _topList(verticesDescending []*vertex.WrappedTx, nTopSlots int) (ledger.Slot, []*vertex.WrappedTx) {
	topSlots := set.New[ledger.Slot]()
	for _, vid := range verticesDescending {
		topSlots.Insert(vid.Slot())
		if len(topSlots) == nTopSlots {
			break
		}
	}
	earliestTopSlot := util.Minimum(maps.Keys(topSlots), func(s1, s2 ledger.Slot) bool {
		return s1 < s2
	})
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.Slot() < earliestTopSlot {
			break
		}
		ret = append(ret, vid)
	}
	return earliestTopSlot, ret
}

func _collectReachableSet(rootVID *vertex.WrappedTx, ret set.Set[*vertex.WrappedTx]) {
	if ret.Contains(rootVID) {
		return
	}
	rootVID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret.Insert(rootVID)
			v.ForEachInputDependency(func(_ byte, inp *vertex.WrappedTx) bool {
				_collectReachableSet(inp, ret)
				return true
			})
			v.ForEachEndorsement(func(_ byte, vEnd *vertex.WrappedTx) bool {
				_collectReachableSet(vEnd, ret)
				return true
			})
		},
		VirtualTx: func(_ *vertex.VirtualTransaction) {
			ret.Insert(rootVID)
		},
	})
}

// _reachableFromTipSet a set of dag reachable from any of the vertex in the top set
func _reachableFromTopList(topList []*vertex.WrappedTx) set.Set[*vertex.WrappedTx] {
	ret := set.New[*vertex.WrappedTx]()
	for _, vid := range topList {
		_collectReachableSet(vid, ret)
	}
	return ret
}

// _orphanedFromReachableSet no global lock while traversing
// Returns vertices not reachable and older than baseline real time
func _badOrOrphanedFromReachableSet(verticesDescending []*vertex.WrappedTx, reachable set.Set[*vertex.WrappedTx], oldestSlotInTopList ledger.Slot) []*vertex.WrappedTx {
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

func (p *Pruner) BadOrOrphanedVertices(nTopSlots int) []*vertex.WrappedTx {
	verticesDescending := p.VerticesDescending()
	oldestSlotInTopList, top := p._topList(verticesDescending, nTopSlots)
	reachable := _reachableFromTopList(top)
	return _badOrOrphanedFromReachableSet(verticesDescending, reachable, oldestSlotInTopList)
}

var prunerLoopPeriod = ledger.SlotDuration() / 2

const TopSlots = 5

func (p *Pruner) mainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(prunerLoopPeriod):
		}
		toDelete := p.BadOrOrphanedVertices(TopSlots)
	}

}
