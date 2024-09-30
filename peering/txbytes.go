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

	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	if ps._isInBlacklist(id) {
		// ignore
		_ = stream.Close()
		return
	}
	p := ps._getPeer(id)
	if p == nil {
		// from unknown peer -> ignore
		_ = stream.Close()
		return
	}

	txBytesWithMetadata, err := readFrame(stream)
	if err != nil {
		ps.Log().Errorf("gossip: error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Close()
		return
	}

	ps.transactionsReceivedCounter.Inc()
	ps.txBytesReceivedCounter.Add(float64(len(txBytesWithMetadata)))

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
		ps._dropPeer(p, err.Error())
		_ = stream.Reset()
		return
	}
	_ = stream.Close()
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
	ps.Tracef(TraceTag, "SendTxBytesWithMetadataToPeer to %s, length: %d (host %s)",
		func() any { return ShortPeerIDString(id) },
		len(txBytes),
		func() any { return ShortPeerIDString(ps.host.ID()) },
	)
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
func (gm _gossipMsgWrapper) SetNow()         {}
func (gm _gossipMsgWrapper) Counter() uint32 { return 0 }
