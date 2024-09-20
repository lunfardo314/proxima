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
		ps.drop1ExcessPeerIfNeeded() // dropping excess dynamic peers one-by-one
		return true
	}, true)
}

func (ps *Peers) isCandidateToConnect(id peer.ID) bool {
	if id == ps.host.ID() {
		return false
	}
	if ps.isInBlacklist(id) {
		return false
	}
	if ps.getPeer(id) != nil {
		return false
	}
	return true
}

func (ps *Peers) discoverPeersIfNeeded() {
	_, aliveDynamic := ps.NumAlive()
	ps.Tracef(TraceTagAutopeering, "FindPeers: num alive dynamic = %d", aliveDynamic)

	if aliveDynamic >= ps.cfg.MaxDynamicPeers {
		return
	}
	maxToAdd := ps.cfg.MaxDynamicPeers - aliveDynamic
	util.Assertf(maxToAdd > 0, "maxToAdd > 0")

	const peerDiscoveryLimit = 10
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

func (ps *Peers) deadDynamicPeers() []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeer(func(p *Peer) bool {
		if !p.isStatic && p._isDead() {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
}

func (ps *Peers) drop1ExcessPeerIfNeeded() {
	if _, aliveDynamic := ps.NumAlive(); aliveDynamic <= ps.cfg.MaxDynamicPeers {
		return
	}

	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	sortedPeers := ps._sortedDynamicPeersByActivityAsc()
	for _, p := range sortedPeers {
		if p._isDead() {
			// drop the oldest dead
			ps._dropPeer(p, "excess peer (dead)")
			return
		}
	}
	if len(sortedPeers) > 0 {
		// just drop the oldest
		ps._dropPeer(sortedPeers[0], "excess peer (errors/oldest)")
	}
}

func (ps *Peers) _sortedDynamicPeersByActivityAsc() []*Peer {
	peers := util.ValuesFiltered(ps.peers, func(p *Peer) bool {
		return !p.isStatic
	})
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].errorCounter > peers[j].errorCounter || peers[i].whenAdded.Before(peers[j].whenAdded)
	})
	return peers
}
