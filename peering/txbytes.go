package peering

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	defer func() {
		_ = stream.Close()
	}()

	id := stream.Conn().RemotePeer()

	known, _ := ps.knownPeer(id, func(p *Peer) {
	})
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

	// Wire format (post metadata-refactor §7): [txid(32)] [txBytes].
	// The 1-byte length-prefixed metadata block has been removed; persistent
	// transaction metadata is gone (deterministic aggregates live on the stem).
	var msg, txBytes []byte
	var txIDPrefix base.TransactionID

	for {
		msg, err = readFrame(stream)
		ps.inMsgCounter.Inc()
		ps.knownPeer(id, func(p *Peer) {
			p.numIncomingTx++
		})
		if err != nil {
			return
		}
		if len(msg) < base.TransactionIDLength {
			// protocol violation
			err = fmt.Errorf("gossip: wrong tx message from peer %s (txid prefix): at least 32 bytes expected", id.String())
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error())
			return
		}
		txIDPrefix, err = base.TransactionIDFromBytes(msg[:base.TransactionIDLength])
		if err != nil {
			// protocol violation
			err = fmt.Errorf("gossip: wrong tx message from peer (txid prefix) %s: %v", id.String(), err)
			ps.Log().Error(err)
			ps.dropPeer(id, err.Error())
			return
		}
		txBytes = msg[base.TransactionIDLength:]

		ps.evidenceMessage()

		ps.transactionsReceivedCounter.Inc()
		ps.txBytesReceivedCounter.Add(float64(len(msg)))

		go ps.onReceiveTx(id, txBytes, txIDPrefix)
	}
}

// Wire format (post metadata-refactor §7): [txid(32)] [txBytes].
func gossipMsg(txid base.TransactionID, txBytes []byte) []byte {
	return common.Concat(txid[:], txBytes)
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, txid base.TransactionID, except ...peer.ID) {
	targets := ps.peerIDsAlive(except...)
	ps.sendMsgBytesOutMulti(targets, ps.lppProtocolGossip, gossipMsg(txid, txBytes))
}

func (ps *Peers) SendTxBytesToPeer(id peer.ID, txBytes []byte, txid base.TransactionID) bool {
	return ps.sendMsgBytesOut(id, ps.lppProtocolGossip, gossipMsg(txid, txBytes))
}
