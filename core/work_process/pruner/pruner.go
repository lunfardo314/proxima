package pruner

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	environment interface {
		global.NodeGlobal
		WithGlobalWriteLock(func())
		Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx
		PurgeDeletedVertices(deleted []*vertex.WrappedTx)
		PurgeCachedStateReaders() (int, int)
		NumVertices() int
		NumStateReaders() int
	}
	Pruner struct {
		environment

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

func New(env environment) *Pruner {
	ret := &Pruner{environment: env}
	ret.registerMetrics()

	ret.RepeatInBackground(Name, ledger.SlotDuration(), func() bool {
		ret.doPrune()
		ret.updateMetrics()
		return true
	}, true)

	return ret
}

// pruneVertices returns how many marked for deletion and how many past cones unreferenced
func (p *Pruner) pruneVertices() (markedForDeletionCount, unreferencedPastConeCount int, refStats [6]uint32) {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	for _, vid := range p.Vertices() {
		markedForDeletion, unreferencedPastCone, nReferences := vid.DoPruningIfRelevant(nowis)
		if markedForDeletion {
			toDelete = append(toDelete, vid)
			markedForDeletionCount++
			p.Tracef(TraceTag, "marked for deletion %s", vid.IDShortString)
		}
		if unreferencedPastCone {
			unreferencedPastConeCount++
			p.Tracef(TraceTag, "past cone of %s has been unreferenced", vid.IDShortString)
		}
		if int(nReferences) < len(refStats)-1 {
			refStats[nReferences]++
		} else {
			refStats[len(refStats)-1]++
		}
	}
	p.PurgeDeletedVertices(toDelete)
	for _, deleted := range toDelete {
		p.StopTracingTx(deleted.ID)
	}
	return
}

func (p *Pruner) doPrune() {
	start := time.Now()

	nDeleted, nUnReferenced, refStats := p.pruneVertices()
	nReadersPurged, readersLeft := p.PurgeCachedStateReaders()

	p.Log().Infof("[memDAG pruner] vertices: %d, deleted: %d, detached past cones: %d. state readers purged: %d, left: %d. Ref stats: %v (took %v)",
		p.NumVertices(), nDeleted, nUnReferenced, nReadersPurged, readersLeft, refStats, time.Since(start))
}

func (p *Pruner) registerMetrics() {
	p.numVerticesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_memDAG_numVerticesGauge",
		Help: "number of vertices in the memDAG",
	})
	p.numStateReadersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_memDAG_numStateReadersGauge",
		Help: "number of cached state readers in the memDAG",
	})
	p.MetricsRegistry().MustRegister(p.numStateReadersGauge, p.numVerticesGauge)
}

func (p *Pruner) updateMetrics() {
	p.numVerticesGauge.Set(float64(p.NumVertices()))
	p.numStateReadersGauge.Set(float64(p.NumStateReaders()))
}
