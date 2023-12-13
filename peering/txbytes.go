package peering

import (
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		_ = stream.Reset()
		return
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	txBytes, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	defer stream.Close()

	p.evidenceActivity(ps, "gossip")
	ps.onReceiveGossip(id, txBytes)
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, except ...peer.ID) int {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	countSent := 0
	for id, p := range ps.peers {
		if !p.isCommunicationOpen() {
			continue
		}
		if len(except) > 0 && id == except[0] {
			continue
		}
		if !p.isAlive() {
			continue
		}
		if ps.SendTxBytesToPeer(id, txBytes) {
			countSent++
		}
	}
	return countSent
}

func (ps *Peers) SendTxBytesToPeer(id peer.ID, txBytes []byte) bool {
	ps.trace("SendTxBytesToPeer to %s, length: %d (host %s)",
		func() any { return ShortPeerIDString(id) },
		len(txBytes),
		func() any { return ShortPeerIDString(ps.host.ID()) },
	)

	if p := ps.getPeer(id); p == nil || !p.isCommunicationOpen() {
		return false
	}

	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolGossip)
	if err != nil {
		ps.trace("SendTxBytesToPeer to %s: %v (host %s)",
			func() any { return ShortPeerIDString(id) }, err,
			func() any { return ShortPeerIDString(ps.host.ID()) },
		)
		return false
	}
	defer stream.Close()

	if err = writeFrame(stream, txBytes); err != nil {
		ps.trace("SendTxBytesToPeer.writeFrame to %s: %v (host %s)", ShortPeerIDString(id), err, ShortPeerIDString(ps.host.ID()))
	}
	return err == nil
}
