package peering

import (
	"math/rand"
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

	for {
		select {
		case <-ps.Ctx().Done():
			ps.Log().Infof("peering: autopeering loop stopped")
			return

		case <-time.After(checkPeersEvery):
			ps.checkPeers()
		}
	}
}

func (ps *Peers) removeNotAliveDynamicPeers() {
	ps.mutex.RLock()
	toRemove := make([]*Peer, 0)
	for _, p := range ps.peers {
		if !p.isPreConfigured && !p.isAlive() {
			toRemove = append(toRemove, p)
		}
	}
	ps.mutex.RUnlock()

	if len(toRemove) == 0 {
		return
	}

	for _, p := range toRemove {
		ps.removeDynamicPeer(p)
	}
	for err := range ps.kademliaDHT.ForceRefresh() {
		if err != nil {
			ps.Tracef(TraceTagAutopeering, "kademlia ForceRefresh: %v", err)
		}
		break
	}
}

func (ps *Peers) checkPeers() {
	ps.removeNotAliveDynamicPeers()
	_, aliveDynamic := ps.NumAlive()

	maxToAdd := ps.cfg.MaxDynamicPeers - aliveDynamic
	if maxToAdd == 0 {
		return
	}
	util.Assertf(maxToAdd > 0, "maxToAdd > 0")

	const peerDiscoveryLimit = 10
	peerChan, err := ps.routingDiscovery.FindPeers(ps.Ctx(), ps.rendezvousString, discovery.Limit(peerDiscoveryLimit))
	if err != nil {
		ps.Log().Errorf("peering: unexpected error while trying to discover peers")
		return
	}

	candidates := make([]peer.AddrInfo, 0)
	for addrInfo := range peerChan {
		if addrInfo.ID == ps.host.ID() || ps.getPeer(addrInfo.ID) != nil {
			continue
		}
		candidates = append(candidates, addrInfo)
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
