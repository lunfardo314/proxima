package pruner

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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
		doneOnClose.Done()
		p.Log().Infof("DAG pruner STOPPED")
	}()
}

const pruningTTLSlots = 5

var pruningTTL = pruningTTLSlots * ledger.SlotDuration()

func (p *Pruner) selectVerticesToPrune() []*vertex.WrappedTx {
	ttlHorizon := time.Now().Add(-pruningTTL)
	ret := make([]*vertex.WrappedTx, 0)
	for _, vid := range p.Vertices() {
		if vid.GetTxStatus() == vertex.Bad || vid.Time().Before(ttlHorizon) {
			ret = append(ret, vid)
		}
	}
	return ret
}

var prunerLoopPeriod = ledger.SlotDuration() / 2

func (p *Pruner) mainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(prunerLoopPeriod):
		}

		toDelete := p.selectVerticesToPrune()

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
		p.Log().Infof("pruned %d vertices. Total vertices on the DAG: %d", len(toDelete), p.NumVertices())

		p.PurgeCachedStateReaders()
	}
}
