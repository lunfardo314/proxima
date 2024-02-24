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

// pruneVertices returns how many marked for deletion and how many past cones unreferenced
func (d *Pruner) pruneVertices() (int, int) {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	var markedForDeletionCount, unreferencedPastConeCount int
	for _, vid := range d.Vertices() {
		markedForDeletion, unreferencedPastCone := vid.DoPruningIfRelevant(nowis)
		if markedForDeletion {
			toDelete = append(toDelete, vid)
			markedForDeletionCount++
		}
		if unreferencedPastCone {
			unreferencedPastConeCount++
		}
	}
	d.PurgeDeletedVertices(toDelete)
	return markedForDeletionCount, unreferencedPastConeCount
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
		nDeleted, nUnReferenced := d.pruneVertices()
		nReadersPurged, readersLeft := d.PurgeCachedStateReaders()

		// not discoverable anymore
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
			float32(memStats.Alloc*10/(1024*1024))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
		d.Log().Infof("vertices deleted: %d, detached past cones: %d. Vertices left: %d. Cached state readers purged: %d, left: %d. "+memStr,
			nDeleted, nUnReferenced, d.NumVertices(), nReadersPurged, readersLeft)

		//p.Log().Infof("\n------------------\n%s\n-------------------", p.Info(true))
	}
}
