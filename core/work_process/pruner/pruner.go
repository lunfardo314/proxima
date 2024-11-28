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
		DurationSinceLastMessageFromPeer() time.Duration
	}
	Pruner struct {
		environment

		// metrics
		metricsEnabled       bool
		numVerticesGauge     prometheus.Gauge
		numStateReadersGauge prometheus.Gauge
		numVerticesPrev      int
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

const disconnectThreshold = 4 * time.Second

// pruneVertices returns how many marked for deletion and how many past cones unreferenced
func (p *Pruner) pruneVertices() (markedForDeletionCount, unreferencedPastConeCount int, refStats [6]uint32) {
	if p.DurationSinceLastMessageFromPeer() > disconnectThreshold {
		p.Log().Warnf("[memDAG pruner] is inactive: node is disconnected for %v", p.DurationSinceLastMessageFromPeer())
		return
	}
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

	n := p.NumVertices()
	p.Log().Infof("[memDAG pruner] vertices: %d(%+d), deleted: %d, detached past cones: %d. state readers purged: %d, left: %d. Ref stats: %v (%v)",
		n, n-p.numVerticesPrev, nDeleted, nUnReferenced, nReadersPurged, readersLeft, refStats, time.Since(start))
	p.numVerticesPrev = n
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
