package peering

import "github.com/prometheus/client_golang/prometheus"

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
	ps.MetricsRegistry().MustRegister(ps.inMsgCounter, ps.outMsgCounter, ps.pullRequestsIn, ps.pullRequestsOut)

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
	ps.peersPullTargets = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_peers_pull_targets",
		Help: "number of possible pull targets",
	})
	ps.MetricsRegistry().MustRegister(ps.peersAll, ps.peersStatic, ps.peersDead, ps.peersAlive, ps.peersPullTargets)
}

func (ps *Peers) peerStats() (ret peersStats) {
	ps.forEachPeer(func(p *Peer) bool {
		ret.peersAll++
		if p._isAlive() {
			ret.peersAlive++
		}
		if p._isDead() {
			ret.peersDead++
		}
		if p.isStatic {
			ret.peersStatic++
		}
		if ps._isPullTarget(p) {
			ret.peersPullTargets++
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
	ps.peersPullTargets.Set(float64(stats.peersPullTargets))
}
