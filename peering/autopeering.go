package peering

import (
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
)

const checkPeersEvery = 3 * time.Second

func (ps *Peers) isCandidateToConnect(id peer.ID) (yes bool) {
	if id == ps.host.ID() {
		return
	}
	ps.withPeer(id, func(p *Peer) {
		// libp2p's host.Connect dedups in-flight dials internally, so we don't
		// need a separate connectList. Peers we've previously dropped aren't
		// auto-redialled — they get re-discovered organically via DHT or peer
		// exchange and have to pass through this check again.
		yes = p == nil
	})
	return
}

func (ps *Peers) discoverPeersIfNeeded() {
	_, aliveDynamic, _ := ps.NumAlive()

	if aliveDynamic >= ps.cfg.MaxDynamicPeers {
		return
	}
	maxToAdd := ps.cfg.MaxDynamicPeers - aliveDynamic
	util.Assertf(maxToAdd > 0, "maxToAdd > 0")

	const peerDiscoveryLimit = 20
	peerChan, err := ps.routingDiscovery.FindPeers(ps.Ctx(), ps.rendezvousString, discovery.Limit(peerDiscoveryLimit))
	if err != nil {
		ps.Log().Errorf("[peering] unexpected error while trying to discover peers")
		return
	}

	candidates := make([]peer.AddrInfo, 0)
	for addrInfo := range peerChan {
		if ps.isCandidateToConnect(addrInfo.ID) {
			candidates = append(candidates, addrInfo)
		}
	}

	if len(candidates) == 0 {
		return
	}
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > maxToAdd {
		candidates = candidates[:maxToAdd]
	}
	for _, a := range candidates {
		if ps.addPeer(&a, "", false) {
			ps.Log().Infof("[peering] added dynamic peer %s", a.ID.String())
		}
	}
}

