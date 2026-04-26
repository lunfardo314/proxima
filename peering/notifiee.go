package peering

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
)

// peeringNotifiee implements network.Notifiee and turns libp2p connection-up/down
// events into our CONNECTED / LOST CONNECTION log lines and the static-peer
// reconnect trigger.
type peeringNotifiee struct {
	ps *Peers
}

func (n *peeringNotifiee) Listen(network.Network, multiaddr.Multiaddr)      {}
func (n *peeringNotifiee) ListenClose(network.Network, multiaddr.Multiaddr) {}

func (n *peeringNotifiee) Connected(_ network.Network, conn network.Conn) {
	id := conn.RemotePeer()
	n.ps.withPeer(id, func(p *Peer) {
		if p == nil {
			// Peer is not in our table yet — HB handler still owns dynamic-peer creation
			// in phase 3a. Nothing to log here; the CONNECTED line will come when the
			// struct appears.
			return
		}
		if !p.lastLoggedConnected {
			n.ps.Log().Infof("[peering] CONNECTED to %s peer %s ('%s')",
				util.Cond(p.isStatic, "static", "dynamic"), ShortPeerIDString(id), p.name)
			p.lastLoggedConnected = true
		}
	})
}

func (n *peeringNotifiee) Disconnected(_ network.Network, conn network.Conn) {
	id := conn.RemotePeer()
	var isStatic bool
	var shouldReconnect bool
	n.ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
		if p.lastLoggedConnected {
			n.ps.Log().Infof("[peering] LOST CONNECTION with %s peer %s ('%s')",
				util.Cond(p.isStatic, "static", "dynamic"), ShortPeerIDString(id), p.name)
			p.lastLoggedConnected = false
		}
		isStatic = p.isStatic
		shouldReconnect = isStatic
	})
	if shouldReconnect {
		go n.ps.scheduleStaticReconnect(id)
	}
}

// scheduleStaticReconnect keeps attempting to reconnect to a static peer that
// libp2p reports as Disconnected, with exponential backoff capped at 30s.
// At most one goroutine per peer runs at any time — duplicate Disconnected
// events are coalesced via ps.reconnecting.
//
// Exits when host.Connect succeeds (Notifiee.Connected will then log CONNECTED),
// when the node is shutting down, or when the peer is no longer tracked.
func (ps *Peers) scheduleStaticReconnect(id peer.ID) {
	ps.mutex.Lock()
	if ps.reconnecting.Contains(id) {
		ps.mutex.Unlock()
		return
	}
	ps.reconnecting.Insert(id)
	ps.mutex.Unlock()

	defer func() {
		ps.mutex.Lock()
		ps.reconnecting.Remove(id)
		ps.mutex.Unlock()
	}()

	const (
		backoffStart = 1 * time.Second
		backoffMax   = 30 * time.Second
		dialTimeout  = 10 * time.Second
	)
	backoff := backoffStart
	// Use the per-Peers stoppedCtx so that Stop() promptly tears these
	// goroutines down even if the env's global ctx is still alive (notably
	// in tests that re-use ports across cases).
	ctxStop := ps.stoppedCtx
	if ctxStop == nil {
		ctxStop = ps.Ctx()
	}
	for {
		select {
		case <-ctxStop.Done():
			return
		case <-time.After(backoff):
		}
		// bail if the peer is no longer in our table — static peers are kept
		// across drops (see _dropPeer), so this should only fire if the peer
		// was explicitly removed elsewhere or if the host is shutting down.
		if ps.getPeer(id) == nil {
			return
		}
		ctx, cancel := context.WithTimeout(ctxStop, dialTimeout)
		err := ps.host.Connect(ctx, peer.AddrInfo{ID: id})
		cancel()
		if err == nil {
			return
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
}
