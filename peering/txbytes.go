package peering

import (
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()

	if ps.isInBlacklist(id) {
		_ = stream.Reset()
		return
	}

	callAfter := func() {}

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			// from unknown peer
			_ = stream.Reset()
			ps.Tracef(TraceTag, "txBytes: unknown peer %s", id.String())
			return
		}
		txBytesWithMetadata, err := readFrame(stream)
		if err != nil {
			_ = stream.Reset()
			ps._dropPeer(p, "read error")
			ps.Log().Errorf("error while reading message from peer %s: %v", id.String(), err)
			return
		}
		metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			_ = stream.Reset()
			ps._dropPeer(p, "error while parsing tx metadata")
			ps.Log().Errorf("error while parsing tx message from peer %s: %v", id.String(), err)
			return
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			_ = stream.Reset()
			ps.dropPeer(p.id, "error while parsing tx metadata")
			ps.Log().Errorf("error while parsing tx message metadata from peer %s: %v", id.String(), err)
			return
		}

		_ = stream.Close()

		p._evidenceActivity("gossip")
		callAfter = func() { ps.onReceiveTx(id, txBytes, metadata) }
	})

	callAfter()
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int {
	countSent := 0
	for _, id := range ps.peerIDsAlive() {
		if len(except) > 0 && id == except[0] {
			continue
		}
		if ps.SendTxBytesWithMetadataToPeer(id, txBytes, metadata) {
			countSent++
		}
	}
	return countSent
}

func (ps *Peers) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	ps.Tracef(TraceTag, "SendTxBytesWithMetadataToPeer to %s, length: %d (host %s)",
		func() any { return ShortPeerIDString(id) },
		len(txBytes),
		func() any { return ShortPeerIDString(ps.host.ID()) },
	)

	if ps.isInBlacklist(id) {
		return false
	}
	if p := ps.getPeer(id); p == nil {
		return false
	}

	ps.sendMsgAsync(common.ConcatBytes(metadata.Bytes(), txBytes), id, ps.lppProtocolGossip)
	return true
}
