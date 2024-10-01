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

	known, blacklisted, _ := ps.knownPeer(id)
	if !known || blacklisted {
		// ignore
		_ = stream.Close()
		return
	}
	txBytesWithMetadata, err := readFrame(stream)
	_ = stream.Close()

	if err != nil {
		ps.Log().Errorf("gossip: error while reading message from peer %s: %v", id.String(), err)
		return
	}

	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		// protocol violation
		err = fmt.Errorf("gossip: error while parsing tx message from peer %s: %v", id.String(), err)
		ps.Log().Error(err)
		ps.dropPeer(id, err.Error())
		return
	}
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	if err != nil {
		// protocol violation
		err = fmt.Errorf("gossip: error while parsing tx message metadata from peer %s: %v", id.String(), err)
		ps.Log().Error(err)
		ps.dropPeer(id, err.Error())
		return
	}

	ps.transactionsReceivedCounter.Inc()
	ps.txBytesReceivedCounter.Add(float64(len(txBytesWithMetadata)))

	ps.onReceiveTx(id, txBytes, metadata)
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
	msg := gossipMsgWrapper{
		metadata: metadata,
		txBytes:  txBytes,
	}
	return ps.sendMsgBytesOut(id, ps.lppProtocolGossip, msg.Bytes())
}

// message wrapper
type gossipMsgWrapper struct {
	metadata *txmetadata.TransactionMetadata
	txBytes  []byte
}

func (gm gossipMsgWrapper) Bytes() []byte {
	return common.ConcatBytes(gm.metadata.Bytes(), gm.txBytes)
}
