package peering

import (
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
)

const checkPeersEvery = 3 * time.Second

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

func (ps *Peers) checkPeers() {
	if ps.NumPeers() == ps.cfg.MaxPeers {
		return
	}
	util.Assertf(ps.NumPeers() < ps.cfg.MaxPeers, "len(ps.peers) <= ps.cfg.MaxPeers")

	peerChan, err := ps.routingDiscovery.FindPeers(ps.Ctx(), ps.rendezvousString)
	if err != nil {
		ps.Log().Errorf("peering: unexpected error while trying to discover peers")
		return
	}

	candidates := make([]peer.AddrInfo, 0, ps.NumPeers())
	for addrInfo := range peerChan {
		if ps.getPeer(addrInfo.ID) != nil {
			continue
		}
		candidates = append(candidates, addrInfo)
		// TODO filter out candidates just recently dropped
	}
	if len(candidates) == 0 {
		return
	}
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	for i := range candidates {
		ps.addPeer(candidates[i].ID, "", false)
	}
}
