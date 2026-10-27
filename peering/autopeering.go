package peering

import (
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"
)

const checkPeersEvery = 3 * time.Second

func (ps *Peers) isCandidateToConnect(id peer.ID) (yes bool) {
	if id == ps.host.ID() || ps.isOtherLedger(id) || ps.isChurnCooldown(id) {
		return
	}
	ps.withPeer(id, func(p *Peer) {
		// libp2p's host.Connect dedups in-flight dials internally. A peer with a
		// dial already in flight is not a candidate again: the slot is reserved in
		// pendingDialing until that dial finishes, so rediscovering it must not
		// launch a second dial. A peer we dropped is not auto-redialled until its
		// cooldown expires; this applies to every drop, not only churn. After that,
		// discovery may surface it again and it has to pass this check once more.
		yes = p == nil && !ps.pendingDialing.Contains(id)
	})
	return
}

func (ps *Peers) discoverPeersIfNeeded() {
	// Count dials in flight, not just connected peers: a dial can take up to the
	// connect timeout, and a tick landing while earlier dials are still pending
	// would otherwise start another full batch, ending up with more dynamic peers
	// than the cap once they all connect.
	ps.mutex.RLock()
	free := ps.cfg.MaxDynamicPeers - ps.numDynamicOrPendingLocked()
	ps.mutex.RUnlock()
	if free <= 0 {
		return
	}

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
	added := 0
	for _, a := range candidates {
		if added >= free {
			return
		}
		// addPeer re-checks the cap under the peers lock and reserves the slot
		// before kicking the async dial; it logs only on dial success.
		if ps.addPeer(&a, "", false) {
			added++
		}
	}
}
