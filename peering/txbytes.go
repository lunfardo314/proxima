package peering

import (
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.Tracef(TraceTag, "unknown peer %s", id.String())
		_ = stream.Reset()
		return
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	txBytesWithMetadata, err := readFrame(stream)
	if err != nil {
		ps.dropPeer(p)
		ps.Log().Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		ps.dropPeer(p)
		ps.Log().Errorf("error while parsing tx message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	if err != nil {
		ps.dropPeer(p)
		ps.Log().Errorf("error while parsing tx message metadata from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}

	defer stream.Close()

	p.evidenceActivity(ps, "gossip")
	ps.onReceiveTx(id, txBytes, metadata)
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int {
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

	if p := ps.getPeer(id); p == nil || !p.isCommunicationOpen() {
		return false
	}

	stream, err := ps.host.NewStream(ps.Ctx(), id, ps.lppProtocolGossip)
	if err != nil {
		ps.Tracef(TraceTag, "SendTxBytesWithMetadataToPeer to %s: %v (host %s)",
			func() any { return ShortPeerIDString(id) }, err,
			func() any { return ShortPeerIDString(ps.host.ID()) },
		)
		return false
	}
	defer stream.Close()

	if err = writeFrame(stream, common.ConcatBytes(metadata.Bytes(), txBytes)); err != nil {
		ps.Tracef("SendTxBytesWithMetadataToPeer.writeFrame to %s: %v (host %s)", ShortPeerIDString(id), err, ShortPeerIDString(ps.host.ID()))
	}
	return err == nil
}
