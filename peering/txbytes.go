package peering

import (
	"bytes"
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/txmetadata"
	"github.com/lunfardo314/proxima/util"
)

type txBytesMsg struct {
	txBytes  []byte
	metadata *txmetadata.TransactionMetadata
}

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

	data, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	msg, err := txBytesMsgFromBytes(data)
	if err != nil {
		ps.log.Errorf("error while parsing txBytes message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}

	defer stream.Close()

	p.evidenceActivity(ps, "gossip")
	ps.onReceiveTx(id, msg.txBytes, msg.metadata)
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
		if ps.SendTxBytesToPeer(id, txBytes, metadata) {
			countSent++
		}
	}
	return countSent
}

func (ps *Peers) SendTxBytesToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
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

	msg := &txBytesMsg{
		txBytes:  txBytes,
		metadata: metadata,
	}
	if err = writeFrame(stream, msg.Bytes()); err != nil {
		ps.trace("SendTxBytesToPeer.writeFrame to %s: %v (host %s)", ShortPeerIDString(id), err, ShortPeerIDString(ps.host.ID()))
	}
	return err == nil
}

func (m *txBytesMsg) Bytes() []byte {
	var buf bytes.Buffer

	if m.metadata == nil {
		buf.WriteByte(0)
	} else {
		txMetadata := m.metadata.Bytes()
		util.Assertf(len(txMetadata) < 256, "len(txMetadata) < 256")
		buf.WriteByte(byte(len(txMetadata)))
		buf.Write(txMetadata)
	}
	buf.Write(m.txBytes)
	return buf.Bytes()
}

func txBytesMsgFromBytes(data []byte) (*txBytesMsg, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("txBytesMsgFromBytes: empty data")
	}
	var mdata *txmetadata.TransactionMetadata
	var err error
	if data[0] > 0 {
		mdata, err = txmetadata.TransactionMetadataFromBytes(data[1 : 1+data[0]])
		if err != nil {
			return nil, fmt.Errorf("txBytesMsgFromBytes: %w", err)
		}
	}
	return &txBytesMsg{
		txBytes:  data[1+data[0]:],
		metadata: mdata,
	}, nil
}
