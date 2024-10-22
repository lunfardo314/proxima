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

func (ps *Peers) isCandidateToConnect(id peer.ID) (yes bool) {
	if id == ps.host.ID() {
		return
	}
	ps.withPeer(id, func(p *Peer) {
		yes = p == nil && !ps._isInBlacklist(id) && !ps._isInCoolOffList(id) && !ps._isInConnectList(id)
	})
	return
}

func (ps *Peers) discoverPeersIfNeeded() {
	aliveStatic, aliveDynamic, pullTargets := ps.NumAlive()
	ps.Tracef(TraceTagAutopeering, "FindPeers: num alive dynamic = %d, static = %d, pull targets = %d", aliveDynamic, aliveStatic, pullTargets)

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
		if ps.addPeer(&a, "", false) {
			ps.Log().Infof("[peering] added dynamic peer %s", a.ID.String())
			ps.Tracef(TraceTagAutopeering, "added dynamic peer %s", a.String())
		}
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
	dynamicPeers := util.ValuesFiltered(ps.peers, func(p *Peer) bool {
		return !p.isStatic
	})
	sort.Slice(dynamicPeers, func(i, j int) bool {
		return dynamicPeers[i].rank() < dynamicPeers[j].rank()
	})
	return dynamicPeers
}
