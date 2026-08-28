package peering

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	// msg metrics
	inMsgCounter         prometheus.Counter
	outMsgCounter        prometheus.Counter
	outMsgDroppedCounter prometheus.Counter
	outQueueMaxLen       prometheus.Gauge
	pullRequestsIn       prometheus.Counter
	pullRequestsOut      prometheus.Counter

	// peers metrics
	peersAll    prometheus.Gauge
	peersStatic prometheus.Gauge
	peersDead   prometheus.Gauge
	peersAlive  prometheus.Gauge

	// txMsg metrics
	transactionsReceivedCounter prometheus.Counter
	txBytesReceivedCounter      prometheus.Counter
}

func (ps *Peers) registerMetrics() {
	ps.inMsgCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_inMsgCounter",
		Help: "counts number of incoming messages",
	})
	ps.outMsgCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_outMsgCounter",
		Help: "counts number of messages coming out",
	})
	ps.pullRequestsIn = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_pullRequestsIn",
		Help: "counts number of received pull request messages",
	})
	ps.pullRequestsOut = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_pullRequestsOut",
		Help: "counts number of sent pull request messages",
	})
	ps.outMsgDroppedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_outMsgDropped",
		Help: "counts outgoing messages dropped because the peer's send backlog was full",
	})
	ps.outQueueMaxLen = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peering_outQueueMaxLen",
		Help: "deepest per-peer outgoing send backlog currently queued, across all peers and protocols",
	})
	ps.MetricsRegistry().MustRegister(ps.inMsgCounter, ps.outMsgCounter, ps.outMsgDroppedCounter,
		ps.outQueueMaxLen, ps.pullRequestsIn, ps.pullRequestsOut)

	// peers metrics
	ps.peersAll = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peers_all",
		Help: "number of current peers",
	})
	ps.peersStatic = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peers_static",
		Help: "number of static peers",
	})
	ps.peersDead = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peers_dead",
		Help: "number of dead peers",
	})
	ps.peersAlive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peers_alive",
		Help: "number of alive peers",
	})
	ps.MetricsRegistry().MustRegister(ps.peersAll, ps.peersStatic, ps.peersDead, ps.peersAlive)

	// tx counters
	ps.transactionsReceivedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_txReceived",
		Help: "counts number of received transaction messages",
	})

	ps.txBytesReceivedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_peering_txBytesReceived",
		Help: "counts number of received transaction bytes",
	})
	ps.MetricsRegistry().MustRegister(ps.transactionsReceivedCounter, ps.txBytesReceivedCounter)
}

func (ps *Peers) peerStats() (ret peersStats) {
	ps.forEachPeerRLock(func(p *Peer) bool {
		ret.peersAll++
		for _, s := range p.streams {
			if n := len(s.out); n > ret.outQueueMaxLen {
				ret.outQueueMaxLen = n
			}
		}
		if ps._isAlive(p) {
			ret.peersAlive++
		}
		if ps._isDead(p) {
			ret.peersDead++
		}
		if p.isStatic {
			ret.peersStatic++
		}
		return true
	})
	return
}

func (ps *Peers) updatePeerMetrics(stats peersStats) {
	ps.peersAll.Set(float64(stats.peersAll))
	ps.peersStatic.Set(float64(stats.peersStatic))
	ps.peersDead.Set(float64(stats.peersDead))
	ps.peersAlive.Set(float64(stats.peersAlive))
	ps.outQueueMaxLen.Set(float64(stats.outQueueMaxLen))
}
