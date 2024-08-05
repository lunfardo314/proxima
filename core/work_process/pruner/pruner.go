package pruner

import (
	"fmt"
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	Environment interface {
		global.NodeGlobal
		WithGlobalWriteLock(func())
		Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx
		PurgeDeletedVertices(deleted []*vertex.WrappedTx)
		PurgeCachedStateReaders() (int, int)
		NumVertices() int
		NumStateReaders() int
	}
	Pruner struct {
		Environment

		// metrics
		metricsEnabled       bool
		numVerticesGauge     prometheus.Gauge
		numStateReadersGauge prometheus.Gauge
	}
)

const (
	Name     = "pruner"
	TraceTag = Name
)

func New(env Environment) *Pruner {
	ret := &Pruner{
		Environment: env,
	}
	ret.registerMetrics()
	return ret
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
			p.Tracef(TraceTag, "marked for deletion %s", vid.IDShortString)
		}
		if unreferencedPastCone {
			unreferencedPastConeCount++
			p.Tracef(TraceTag, "past cone of %s has been unreferenced", vid.IDShortString)
		}
	}
	p.PurgeDeletedVertices(toDelete)
	for _, deleted := range toDelete {
		p.StopTracingTx(deleted.ID)
	}
	return markedForDeletionCount, unreferencedPastConeCount
}

func (p *Pruner) Start() {
	p.Infof0("STARTING MemDAG pruner..")
	go func() {
		p.mainLoop()
		p.Log().Debugf("MemDAG pruner STOPPED")
	}()
}

func (p *Pruner) doPrune() {
	nDeleted, nUnReferenced := p.pruneVertices()
	nReadersPurged, readersLeft := p.PurgeCachedStateReaders()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memStr := fmt.Sprintf("Mem. alloc: %.1f MB, GC: %d, GoRt: %d, ",
		float32(memStats.Alloc*10/(1024*1024))/10,
		memStats.NumGC,
		runtime.NumGoroutine(),
	)
	p.Infof0("[memDAG pruner] vertices deleted: %d, detached past cones: %d. Vertices left: %d. Cached state readers purged: %d, left: %d. "+memStr,
		nDeleted, nUnReferenced, p.NumVertices(), nReadersPurged, readersLeft)
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
		p.doPrune()
		p.updateMetrics()
	}
}

func (p *Pruner) registerMetrics() {
	p.numVerticesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memDAG_numVerticesGauge",
		Help: "number of vertices in the memDAG",
	})
	p.MetricsRegistry().MustRegister(p.numVerticesGauge)

	p.numStateReadersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memDAG_numStateReadersGauge",
		Help: "number of cached state readers in the memDAG",
	})
	p.MetricsRegistry().MustRegister(p.numStateReadersGauge)
}

func (p *Pruner) updateMetrics() {
	p.numVerticesGauge.Set(float64(p.NumVertices()))
	p.numStateReadersGauge.Set(float64(p.NumStateReaders()))
}
