package peering

import (
	"math/rand"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/maps"
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
			ps.removeNotAliveDynamicPeers()
			ps.discoverPeersIfNeeded()
			ps.dropExcessPeersIfNeeded()

			for err := range ps.kademliaDHT.ForceRefresh() {
				if err != nil {
					ps.Tracef(TraceTagAutopeering, "kademlia ForceRefresh: %v", err)
				}
				break
			}
		}
	}
}

func (ps *Peers) discoverPeersIfNeeded() {
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
}

func (ps *Peers) dropExcessPeersIfNeeded() {
	if _, aliveDynamic := ps.NumAlive(); aliveDynamic <= ps.cfg.MaxDynamicPeers {
		return
	}
	ranks := ps.calcDynamicPeerRanks()
	sortedByRanks := maps.Keys(ranks)
	sort.Slice(sortedByRanks, func(i, j int) bool {
		return ranks[sortedByRanks[i]] > ranks[sortedByRanks[j]]
	})

	if len(sortedByRanks) <= ps.cfg.MaxDynamicPeers {
		return
	}
	for _, p := range sortedByRanks[:ps.cfg.MaxDynamicPeers] {
		ps.removeDynamicPeer(p)
	}
}

// calcDynamicPeerRanks uses very simple peer ranking strategy. It sorts peers according to different criteria
// The rank according to that criterion is index in the sorted array.
// Final rank is sum of ranks of different criteria without any weights.
// Bigger the rank, bigger priority of removal
func (ps *Peers) calcDynamicPeerRanks() map[*Peer]int {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	peers := make([]*Peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.isPreConfigured {
			peers = append(peers, p)
		}
	}
	ranks := make(map[*Peer]int, len(peers))

	// sort by begin peering time, descending
	sort.Slice(peers, func(i, j int) bool {
		// older it is, bigger the rank
		return peers[i].whenAdded.After(peers[j].whenAdded)
	})
	for i, p := range peers {
		ranks[p] = ranks[p] + i
	}

	// sort by last activity
	sort.Slice(peers, func(i, j int) bool {
		// most recent activity means bigger rank
		return peers[i].lastActivity.After(peers[j].lastActivity)
	})
	for i, p := range peers {
		ranks[p] = ranks[p] + i
	}

	// sort by clock difference
	sort.Slice(peers, func(i, j int) bool {
		// bigger clock difference means bigger priority of removal
		// >>>> may be slow with many peers and many clock differences stored
		return peers[i].avgClockDifference() < peers[j].avgClockDifference()
	})
	for i, p := range peers {
		ranks[p] = ranks[p] + i
	}

	// sort by good transactions
	sort.Slice(peers, func(i, j int) bool {
		// more good transaction, less priority of removal
		return peers[i].incomingGood > peers[j].incomingGood
	})
	for i, p := range peers {
		ranks[p] = ranks[p] + i
	}

	// sort by bad transactions
	sort.Slice(peers, func(i, j int) bool {
		// more bad transaction, more priority of removal
		return peers[i].incomingBad < peers[j].incomingGood
	})
	for i, p := range peers {
		ranks[p] = ranks[p] + i
	}
	return ranks
}
