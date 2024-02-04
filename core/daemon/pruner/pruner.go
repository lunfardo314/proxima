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
	p.Log().Infof("STARTING.. [%s]", p.Log().Level().String())
	go func() {
		p.mainLoop(ctx)
		doneOnClose.Done()
		p.Log().Infof("DAG pruner STOPPED")
	}()
}

func (p *Pruner) selectVerticesToPrune() []*vertex.WrappedTx {
	verticesDescending := p.VerticesDescending()
	baselineSlot := p.pruningBaselineSlot(verticesDescending)
	ttlHorizon := time.Now().Add(-time.Duration(p.PruningTTLSlots()) * ledger.SlotDuration())

	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range verticesDescending {
		switch {
		case vid.GetTxStatus() == vertex.Bad:
			ret = append(ret, vid)
		case vid.Time().After(ttlHorizon):
		case vid.Slot() >= baselineSlot:
		default:
			ret = append(ret, vid)
		}
	}
	return ret
}

// pruningBaselineSlot vertices older than pruningBaselineSlot are candidates for pruning
// Vertices with the pruningBaselineSlot and younger are not touched
func (p *Pruner) pruningBaselineSlot(verticesDescending []*vertex.WrappedTx) ledger.Slot {
	topSlots := set.New[ledger.Slot]()
	for _, vid := range verticesDescending {
		topSlots.Insert(vid.Slot())
		if len(topSlots) == p.PruningTTLSlots() {
			break
		}
	}
	return util.Minimum(maps.Keys(topSlots), func(s1, s2 ledger.Slot) bool {
		return s1 < s2
	})
}

var prunerLoopPeriod = ledger.SlotDuration() / 2

func (p *Pruner) mainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(prunerLoopPeriod):
		}

		nReadersPurged := p.PurgeCachedStateReaders()
		p.Log().Infof("purged %d state readers from the cache", nReadersPurged)

		toDelete := p.selectVerticesToPrune()

		// we do 2-step mark deleted-purge in order to avoid deadlocks. It is not completely correct,
		// because this way deletion from the DAG is not an atomic action. In some period, a transaction can be discovered
		// in the DAG by its ID (by AttachTxID function), however in the 'deleted state'. It means repetitive attachment of the
		// transaction is not possible until complete purge.
		// This may create non-determinism TODO !!!

		for _, vid := range toDelete {
			vid.MarkDeleted()
		}
		// --- > here deleted transactions are still discoverable in the DAG in 'deleted' state
		p.PurgeDeletedVertices(toDelete)
		// not discoverable anymore
		p.Log().Infof("pruned %d vertices. Total vertices on the DAG: %d", len(toDelete), p.NumVertices())

		//p.Log().Infof("\n------------------\n%s\n-------------------", p.Info(true))
	}
}
