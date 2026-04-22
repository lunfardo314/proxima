package peering

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	defer func() {
		_ = stream.Close()
	}()

	id := stream.Conn().RemotePeer()

	known, blacklisted, _ := ps.knownPeer(id, func(p *Peer) {
	})
	if blacklisted {
		// ignore
		return
	}
	if !known {
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Log().Warnf("[peering] node does not take any incoming dynamic peers")
			return
		}
	}

	// receive start
	_, err := readFrame(stream)
	if err != nil {
		return
	}

	var txBytesWithMetadata, metadataBytes, txBytes []byte
	var metadata *txmetadata.TransactionMetadata
	var txIDPrefix base.TransactionID

	for {
		txBytesWithMetadata, err = readFrame(stream)
		ps.inMsgCounter.Inc()
		_, blacklisted, _ = ps.knownPeer(id, func(p *Peer) {
			p.numIncomingTx++
		})
		if blacklisted {
			// ignore
			return
		}
		if err != nil {
			return
		}
		if len(txBytesWithMetadata) < base.TransactionIDLength {
			// protocol violation
			err = fmt.Errorf("gossip: wrong tx message from peer %s (txid prefix): at least 32 bytes expected", id.String())
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error(), true)
			return
		}
		txIDPrefix, err = base.TransactionIDFromBytes(txBytesWithMetadata[:base.TransactionIDLength])
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: wrong tx message from peer (txid prefix) %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error(), true)
			return
		}
		txBytesWithMetadata = txBytesWithMetadata[base.TransactionIDLength:]
		metadataBytes, txBytes, err = txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error(), true)
			return
		}
		metadata, err = txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: error while parsing tx message metadata from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error(), true)
			return
		}

		ps.evidenceMessage()

		ps.transactionsReceivedCounter.Inc()
		ps.txBytesReceivedCounter.Add(float64(len(txBytesWithMetadata)))

		go ps.onReceiveTx(id, txBytes, metadata, txIDPrefix)
	}
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID, except ...peer.ID) {
	targets := ps.peerIDsAlive(except...)
	ps.sendTxBytesWithMetadataToPeers(targets, txBytes, metadata, txid)
}

func (ps *Peers) sendTxBytesWithMetadataToPeers(ids []peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID) {
	msg := gossipMsgWrapper{
		txid:     txid,
		metadata: metadata,
		txBytes:  txBytes,
	}
	ps.sendMsgBytesOutMulti(ids, ps.lppProtocolGossip, msg.Bytes())
}

func (ps *Peers) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID) bool {
	msg := gossipMsgWrapper{
		txid:     txid,
		metadata: metadata,
		txBytes:  txBytes,
	}
	return ps.sendMsgBytesOut(id, ps.lppProtocolGossip, msg.Bytes())
}

// message wrapper
type gossipMsgWrapper struct {
	txid     base.TransactionID
	metadata *txmetadata.TransactionMetadata
	txBytes  []byte
}

func (gm gossipMsgWrapper) Bytes() []byte {
	return common.Concat(gm.txid[:], gm.metadata.Bytes(), gm.txBytes)
}
