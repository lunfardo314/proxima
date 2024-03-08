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

const (
	Name     = "pruner"
	TraceTag = Name
)

func New(env Environment) *Pruner {
	return &Pruner{
		Environment: env,
	}
}

// pruneVertices returns how many marked for deletion and how many past cones unreferenced
func (p *Pruner) pruneVertices() (int, int) {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	var markedForDeletionCount, unreferencedPastConeCount int
	for _, vid := range p.Vertices() {
		markedForDeletion, unreferencedPastCone := vid.DoPruningIfRelevant(nowis)
		if markedForDeletion {
			toDelete = append(toDelete, vid)
			markedForDeletionCount++
		}
		if unreferencedPastCone {
			unreferencedPastConeCount++
		}
	}
	p.PurgeDeletedVertices(toDelete)
	for _, deleted := range toDelete {
		p.Tracef(TraceTag, "deleted %s", deleted.IDShortString)
		p.StopTracingTx(deleted.ID)
	}
	return markedForDeletionCount, unreferencedPastConeCount
}

func (p *Pruner) Start() {
	p.Log().Infof("STARTING.. [%s]", p.Log().Level().String())
	go func() {
		p.mainLoop()
		p.Log().Debugf("DAG pruner STOPPED")
	}()
}

func (p *Pruner) mainLoop() {
	p.MarkWorkProcessStarted(Name)
	defer p.MarkWorkProcessStopped(Name)

	prunerLoopPeriod := ledger.SlotDuration() / 2

	for {
		select {
		case <-p.Ctx().Done():
			return
		case <-time.After(prunerLoopPeriod):
		}
		nDeleted, nUnReferenced := p.pruneVertices()
		nReadersPurged, readersLeft := p.PurgeCachedStateReaders()

		// not discoverable anymore
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
			float32(memStats.Alloc*10/(1024*1024))/10,
			memStats.NumGC,
			runtime.NumGoroutine(),
		)
		p.Log().Infof("vertices deleted: %d, detached past cones: %d. Vertices left: %d. Cached state readers purged: %d, left: %d. "+memStr,
			nDeleted, nUnReferenced, p.NumVertices(), nReadersPurged, readersLeft)

		//p.Log().Infof("\n------------------\n%s\n-------------------", p.Info(true))
	}
}
