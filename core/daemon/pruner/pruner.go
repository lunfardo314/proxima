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
	Pruner struct {
		*dag.DAG
		global.Logging
	}
)

func New(dag *dag.DAG, log global.Logging) *Pruner {
	return &Pruner{
		DAG:     dag,
		Logging: global.MakeSubLogger(log, "[prune]"),
	}
}

func (p *Pruner) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	go func() {
		p.mainLoop(ctx)
		doneOnClose.Wait()
		p.Log().Infof("DAG pruner STOPPED")
	}()
}

// pruneBaselineSlot vertices older than pruneBaselineSlot are candidates for pruning
// Vertices with the pruneBaselineSlot and younger are not touched
func pruneBaselineSlot(verticesDescending []*vertex.WrappedTx, nTopSlots int) ledger.Slot {
	topSlots := set.New[ledger.Slot]()
	for _, vid := range verticesDescending {
		topSlots.Insert(vid.Slot())
		if len(topSlots) == nTopSlots {
			break
		}
	}
	return util.Minimum(maps.Keys(topSlots), func(s1, s2 ledger.Slot) bool {
		return s1 < s2
	})
}

// topList returns a descending list of vertices which belongs to top nSlots slots, also the oldest slot in the top list
func topList(verticesDescending []*vertex.WrappedTx, pruneBaselineSlot ledger.Slot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.Slot() < pruneBaselineSlot {
			break
		}
		ret = append(ret, vid)
	}
	return ret
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
func _badOrOrphanedFromReachableSet(verticesDescending []*vertex.WrappedTx, reachable set.Set[*vertex.WrappedTx], pruneBaselineSlot ledger.Slot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.GetTxStatus() == vertex.Bad {
			ret = append(ret, vid)
		}
		if vid.Slot() >= pruneBaselineSlot {
			continue
		}
		if !reachable.Contains(vid) {
			ret = append(ret, vid)
		}
	}
	return ret
}

func badOrOrphanedVertices(verticesDescending, topList []*vertex.WrappedTx, pruneBaselineSlot ledger.Slot) []*vertex.WrappedTx {
	reachable := _reachableFromTopList(topList)
	return _badOrOrphanedFromReachableSet(verticesDescending, reachable, pruneBaselineSlot)
}

func (p *Pruner) branchesToPrune(pruneBaselineSlot ledger.Slot) []*vertex.WrappedTx {
	branches := p.Branches()
	toDelete := make([]*vertex.WrappedTx, 0)
	for _, vid := range branches {
		if vid.Slot() < pruneBaselineSlot {
			vid.ConvertBranchVertexToVirtualTx()
			toDelete = append(toDelete, vid)
		}
	}
	return toDelete
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

		// TODO take into account real time, not ledger time
		// top list must contain those which appeared recently

		verticesDescending := p.VerticesDescending()
		_pruneBaselineSlot := pruneBaselineSlot(verticesDescending, TopSlots)

		_branchesToPrune := p.branchesToPrune(_pruneBaselineSlot)
		p.PurgeBranches(_branchesToPrune)
		p.Log().Infof("pruned %d branches", len(_branchesToPrune))

		_topList := topList(verticesDescending, _pruneBaselineSlot)
		toDelete := badOrOrphanedVertices(verticesDescending, _topList, _pruneBaselineSlot)
		for _, vid := range toDelete {
			vid.MarkDeleted()
		}
		// here deleted transactions are discoverable in the DAG in 'deleted' state
		p.PurgeDeleted(toDelete)
		// not discoverable anymore
		p.Log().Infof("pruned %d vertices", len(toDelete))
	}
}
