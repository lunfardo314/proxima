package peering

import (
	"math/rand"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
)

const (
	TraceTagAutopeering = "autopeering"
	checkPeersEvery     = 3 * time.Second
)

func (ps *Peers) startAutopeering() {
	util.Assertf(ps.isAutopeeringEnabled(), "ps.isAutopeeringEnabled()")

	ps.RepeatInBackground("autopeering_loop", checkPeersEvery, func() bool {
		ps.discoverPeersIfNeeded()
		ps.dropExcessPeersIfNeeded() // dropping excess dynamic peers one-by-one
		return true
	}, true)
}

func (ps *Peers) isCandidateToConnect(id peer.ID) (yes bool) {
	if id == ps.host.ID() {
		return
	}
	ps.withPeer(id, func(p *Peer) {
		yes = p != nil && !ps._isInBlacklist(id)
	})
	return
}

func (ps *Peers) discoverPeersIfNeeded() {
	_, aliveDynamic := ps.NumAlive()
	ps.Tracef(TraceTagAutopeering, "FindPeers: num alive dynamic = %d", aliveDynamic)

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
	ps.Tracef(TraceTagAutopeering, "FindPeers: len(candidates) = %d", len(candidates))

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
		ps.addPeer(&a, "", false)
	}
}

func (ps *Peers) dropExcessPeersIfNeeded() {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	sortedDynamicPeers := ps._sortedDynamicPeersByRankAsc()
	if len(sortedDynamicPeers) <= ps.cfg.MaxDynamicPeers {
		return
	}
	for _, p := range sortedDynamicPeers[:len(sortedDynamicPeers)-ps.cfg.MaxDynamicPeers] {
		if time.Since(p.whenAdded) > gracePeriodAfterAdded {
			ps._dropPeer(p, "excess peer (by rank)")
		}
	}
}

func (ps *Peers) _sortedDynamicPeersByRankAsc() []*Peer {
	peers := util.ValuesFiltered(ps.peers, func(p *Peer) bool {
		return !p.isStatic
	})
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].rank() < peers[j].rank()
	})
	return peers
}
