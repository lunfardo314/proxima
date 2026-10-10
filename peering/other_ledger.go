package peering

import (
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// A node of another ledger. The application protocols are named after the
// library hash at slot 0, so after a reset an old node and a new one share no
// protocol: every stream between them fails with "protocol not supported",
// and no transaction can cross. The libp2p connection itself is version-blind,
// though, so without this a leftover node stays connected, is counted alive,
// holds a dynamic slot and costs a failed negotiation on every gossip
// message, for as long as it runs. The first failed negotiation therefore
// bans the peer: its connection is closed, a dynamic peer is dropped, and its
// redials during the ban are closed at connect without a log line.

var errOtherLedger = errors.New("peer is on another ledger")

// banOtherLedger records the peer as being on another ledger and cuts it off.
// A static peer is an operator's stale configuration: it is warned about and
// closed, but kept, as static peers always are.
func (ps *Peers) banOtherLedger(id peer.ID, missing protocol.ID) {
	ps.mutex.Lock()
	now := time.Now()
	for p, until := range ps.otherLedger {
		if !now.Before(until) {
			delete(ps.otherLedger, p)
		}
	}
	if len(ps.otherLedger) < otherLedgerBanMaxEntries {
		ps.otherLedger[id] = now.Add(otherLedgerBanDuration)
	}
	p := ps.peers[id]
	static := p != nil && p.isStatic
	if p != nil && !static {
		ps._dropPeer(p, "on another ledger: does not support "+string(missing))
	}
	ps.mutex.Unlock()

	if static {
		ps.Log().Warnf("[peering] static peer %s is on another ledger: it does not support %s; closing its connection for %v, fix the configuration",
			ShortPeerIDString(id), missing, otherLedgerBanDuration)
		if p != nil {
			for _, s := range p.streams {
				ps.clearPeerStream(s)
			}
		}
		_ = ps.host.Network().ClosePeer(id)
		return
	}
	if p == nil {
		_ = ps.host.Network().ClosePeer(id)
	}
}

// isOtherLedger reports whether the peer is banned as being on another ledger.
func (ps *Peers) isOtherLedger(id peer.ID) bool {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	until, ok := ps.otherLedger[id]
	return ok && time.Now().Before(until)
}
