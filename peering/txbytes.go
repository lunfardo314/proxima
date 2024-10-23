package peering

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {

	defer func() {
		stream.Close()
		ps.Log().Infof("[peering] gossip: streamHandler exit")
	}()

	id := stream.Conn().RemotePeer()

	known, blacklisted, _ := ps.knownPeer(id, func(p *Peer) {
	})
	if !known || blacklisted {
		// ignore
		return
	}
	for {
		txBytesWithMetadata, err := readFrame(stream)
		ps.inMsgCounter.Inc()
		known, blacklisted, _ := ps.knownPeer(id, func(p *Peer) {
			p.numIncomingTx++
		})
		if !known || blacklisted {
			// ignore
			return
		}
		if err != nil {
			ps.Log().Errorf("gossip: error while reading message from peer %s: %v", id.String(), err)
			return
		}

		metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			return
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message metadata from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			return
		}

		ps.transactionsReceivedCounter.Inc()
		ps.txBytesReceivedCounter.Add(float64(len(txBytesWithMetadata)))

		go ps.onReceiveTx(id, txBytes, metadata)
	}
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) {
	targets := ps.peerIDsAlive(except...)
	ps.sendTxBytesWithMetadataToPeers(targets, txBytes, metadata)
}

func (ps *Peers) sendTxBytesWithMetadataToPeers(ids []peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) {
	msg := gossipMsgWrapper{
		metadata: metadata,
		txBytes:  txBytes,
	}
	ps.sendMsgBytesOutMulti(ids, ps.lppProtocolGossip, msg.Bytes())
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
