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

const keepNumTopSlots = 5

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

// pruningBaselineSlotAndDeadline vertices older than pruningBaselineSlotAndDeadline are candidates for pruning
// Vertices with the pruningBaselineSlotAndDeadline and younger are not touched
func pruningBaselineSlotAndDeadline(verticesDescending []*vertex.WrappedTx, nTopSlots int) (ledger.Slot, time.Time) {
	topSlots := set.New[ledger.Slot]()
	for _, vid := range verticesDescending {
		topSlots.Insert(vid.Slot())
		if len(topSlots) == nTopSlots {
			break
		}
	}
	return util.Minimum(maps.Keys(topSlots), func(s1, s2 ledger.Slot) bool {
		return s1 < s2
	}), time.Now().Add(-keepNumTopSlots * ledger.SlotDuration())
}

// topList returns a descending list of vertices which either belongs
// to top nSlots slots or is younger than a particular deadline
func topList(verticesDescending []*vertex.WrappedTx, pruneBaselineSlot ledger.Slot, youngerThan time.Time) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		if vid.Slot() < pruneBaselineSlot || vid.Time().After(youngerThan) {
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

// pruneBranches branches older than particular slot or real time deadline converts into virtual transactions.
// The past cone of the branch becomes unreachable from the branch
func (p *Pruner) pruneBranches(verticesDescending []*vertex.WrappedTx, pruneBaselineSlot ledger.Slot, pruneDeadline time.Time) (ret int) {
	for _, vid := range verticesDescending {
		if !vid.IsBranchTransaction() {
			continue
		}
		if vid.Slot() < pruneBaselineSlot || vid.Time().Before(pruneDeadline) {
			vid.ConvertBranchVertexToVirtualTx()
			ret++
		}
	}
	return
}

var prunerLoopPeriod = ledger.SlotDuration() / 2

func (p *Pruner) mainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(prunerLoopPeriod):
		}

		verticesDescending := p.VerticesDescending()
		_pruneBaselineSlot, _pruningDeadline := pruningBaselineSlotAndDeadline(verticesDescending, keepNumTopSlots)

		nPrunedBranches := p.pruneBranches(verticesDescending, _pruneBaselineSlot, _pruningDeadline)
		p.Log().Infof("pruned %d branches", nPrunedBranches)

		_topList := topList(verticesDescending, _pruneBaselineSlot, _pruningDeadline)
		toDelete := badOrOrphanedVertices(verticesDescending, _topList, _pruneBaselineSlot)

		// we do 2-step mark deleted-purge in order to avoid deadlocks. It is not completely correct,
		// because deletion from the DAG is not an atomic action. In some period, a transaction can be discovered
		// in the DAG by its ID (by AttachTxID function), however in the 'deleted state'. It means repetitive attachment of the
		// transaction is not possible until complete purge. Is that a problem? TODO

		for _, vid := range toDelete {
			vid.MarkDeleted()
		}
		// here deleted transactions are discoverable in the DAG in 'deleted' state
		p.PurgeDeletedVertices(toDelete)
		// not discoverable anymore
		p.Log().Infof("pruned %d vertices", len(toDelete))

		p.PurgeCachedStateReaders()
	}
}
