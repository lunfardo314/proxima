package peering

import (
	"fmt"
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

func (ps *Peers) autopeeringLoop() {
	util.Assertf(ps.isAutopeeringEnabled(), "ps.isAutopeeringEnabled()")

	ps.Log().Infof("[peering] start autopeering loop")

	for {
		select {
		case <-ps.Ctx().Done():
			ps.Log().Infof("[peering] autopeering loop stopped")
			return

		case <-time.After(checkPeersEvery):
			ps.removeDeadDynamicPeers()
			ps.discoverPeersIfNeeded()
			ps.dropExcessPeersIfNeeded()
		}
	}
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
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	ret := make([]peer.ID, 0)
	for id, p := range ps.peers {
		if !p.isStatic && p.isDead() {
			ret = append(ret, id)
		}
	}
	return ret
}

func (ps *Peers) removeDeadDynamicPeers() {
	toRemove := ps.deadDynamicPeers()
	for _, id := range toRemove {
		ps.logConnectionStatusIfNeeded(id)
		ps.dropPeer(id, "dead")
	}
}

func (ps *Peers) dropExcessPeersIfNeeded() {
	if _, aliveDynamic := ps.NumAlive(); aliveDynamic <= ps.cfg.MaxDynamicPeers {
		return
	}
	// dropping
	sortedPeers := ps.sortPeersByActivityDesc()
	if len(sortedPeers) <= ps.cfg.MaxDynamicPeers {
		return
	}
	for _, p := range sortedPeers[:ps.cfg.MaxDynamicPeers] {
		ps.dropPeer(p.id, fmt.Sprintf("excess over maximum dynamic peers configured: %d", ps.cfg.MaxDynamicPeers))
	}
}

func (ps *Peers) sortPeersByActivityDesc() []*Peer {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	peers := util.ValuesFiltered(ps.peers, func(p *Peer) bool {
		return !p.isStatic
	})
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].whenAdded.Before(peers[j].whenAdded)
	})
	return peers
}
