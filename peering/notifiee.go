package peering

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
)

// peeringNotifiee implements network.Notifiee and turns libp2p connection-up/down
// events into our CONNECTED / LOST CONNECTION log lines and the static-peer
// reconnect trigger. In phase 3a this runs alongside the existing HB-timeout
// machinery; phase 3b will delete the redundant manual state.
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

// connectionGater implements connmgr.ConnectionGater and consults the blacklist
// at every gate-able entry point. In phase 3a the per-handler blacklist checks
// are still in place as belt-and-braces; phase 3b removes them.
//
// ps may be nil briefly between libp2p.New and the Peers struct assignment;
// during that window every gate returns "allow" (there is nothing to check yet).
type connectionGater struct {
	ps *Peers
}

func (g *connectionGater) denied(id peer.ID) bool {
	if g.ps == nil {
		return false
	}
	return g.ps.IsBlacklisted(id)
}

func (g *connectionGater) InterceptPeerDial(p peer.ID) bool                        { return !g.denied(p) }
func (g *connectionGater) InterceptAddrDial(p peer.ID, _ multiaddr.Multiaddr) bool { return !g.denied(p) }
func (g *connectionGater) InterceptAccept(_ network.ConnMultiaddrs) bool           { return true }
func (g *connectionGater) InterceptSecured(_ network.Direction, p peer.ID, _ network.ConnMultiaddrs) bool {
	return !g.denied(p)
}
func (g *connectionGater) InterceptUpgraded(_ network.Conn) (bool, control.DisconnectReason) {
	return true, 0
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
	for {
		select {
		case <-ps.Ctx().Done():
			return
		case <-time.After(backoff):
		}
		// bail if the peer is no longer ours (e.g. dropped/blacklisted after a
		// protocol violation). Static peers we still care about survive dropPeer
		// via the cleanCoolofflist re-add path, but once that's gone (phase 3b)
		// this check is what stops orphan goroutines.
		if ps.getPeer(id) == nil {
			return
		}
		ctx, cancel := context.WithTimeout(ps.Ctx(), dialTimeout)
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
