package peering

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	ps.inMsgCounter.Inc()

	id := stream.Conn().RemotePeer()

	if ps.isInBlacklist(id) {
		//_ = stream.Reset()
		_ = stream.Close()
		return
	}

	callAfter := func() {}

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			// from unknown peer
			//_ = stream.Reset()
			_ = stream.Close()
			return
		}
		txBytesWithMetadata, err := readFrame(stream)
		if err != nil {
			//_ = stream.Reset()
			err = fmt.Errorf("gossip: error while reading message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			p.errorCounter++
			//ps._dropPeer(p, err.Error())
			_ = stream.Close()
			return
		}
		metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps._dropPeer(p, err.Error())
			_ = stream.Reset()
			return
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message metadata from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps.dropPeer(p.id, err.Error())
			_ = stream.Reset()
			return
		}

		_ = stream.Close()

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

	ps.sendMsgOutQueued(&_gossipMsgWrapper{
		metadata: metadata,
		txBytes:  txBytes,
	}, id, ps.lppProtocolGossip)
	return true
}

// message wrapper
type _gossipMsgWrapper struct {
	metadata *txmetadata.TransactionMetadata
	txBytes  []byte
}

func (gm _gossipMsgWrapper) Bytes() []byte {
	return common.ConcatBytes(gm.metadata.Bytes(), gm.txBytes)
}
func (gm _gossipMsgWrapper) SetNow() {}
