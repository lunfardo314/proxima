package pruner

import (
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
)

type (
	Environment interface {
		global.NodeGlobal
		WithGlobalWriteLock(func())
		Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx
		PruningTTLSlots() int
		PurgeDeletedVertices(deleted []*vertex.WrappedTx)
		PurgeCachedStateReaders() (int, int)
		NumVertices() int
	}
	Pruner struct {
		Environment
	}
)

const Name = "pruner"

func New(env Environment) *Pruner {
	return &Pruner{
		Environment: env,
	}
}

func (d *Pruner) pruneVertices() int {
	toDelete := make([]*vertex.WrappedTx, 0)
	for _, vid := range d.Vertices() {
		if vid.MarkDeletedIfNotReferenced() {
			toDelete = append(toDelete, vid)
		}
	}
	d.PurgeDeletedVertices(toDelete)
	return len(toDelete)
}

func (d *Pruner) Start() {
	d.Log().Infof("STARTING.. [%s]", d.Log().Level().String())
	go func() {
		d.mainLoop()
		d.Log().Debugf("DAG pruner STOPPED")
	}()
}

func (d *Pruner) mainLoop() {
	d.MarkWorkProcessStarted(Name)
	defer d.MarkWorkProcessStopped(Name)

	prunerLoopPeriod := ledger.SlotDuration() / 2

	for {
		select {
		case <-d.Ctx().Done():
			return
		case <-time.After(prunerLoopPeriod):
		}
		nDeleted := d.pruneVertices()
		nReadersPurged, readersLeft := d.PurgeCachedStateReaders()

		// not discoverable anymore
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
			float32(memStats.Alloc*10/(1024*1024))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
		d.Log().Infof("vertices pruned: %d. Vertices left: %d. Cached state readers purged: %d, left: %d. "+memStr,
			nDeleted, d.NumVertices(), nReadersPurged, readersLeft)

		//p.Log().Infof("\n------------------\n%s\n-------------------", p.Info(true))
	}
}
