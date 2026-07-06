package peering

import (
	"bytes"
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/base"
)

// pull request message 1st byte is the type of the message. The rest is message body

const PullTransactions = byte(iota)

func (ps *Peers) pullStreamHandler(stream network.Stream) {
	defer func() {
		_ = stream.Close()
	}()

	if ps.cfg.IgnoreAllPullRequests {
		// ignore all pull requests
		return
	}

	id := stream.Conn().RemotePeer()

	known, static := ps.knownPeer(id, func(p *Peer) {
	})
	if !known {
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Log().Warnf("[peering] node does not take any incoming dynamic peers")
			return
		}
	}

	if !static && ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
		// ignore pull requests from automatic peers
		return
	}

	// receive start
	_, err := readFrame(stream)
	if err != nil {
		return
	}
	var msgData []byte

	for {
		msgData, err = readFrame(stream)
		ps.knownPeer(id, func(p *Peer) {
			p.numIncomingPull++
		})

		ps.inMsgCounter.Inc()
		switch {
		case err != nil:
			return
		case len(msgData) == 0:
			ps.Log().Errorf("pull: error while reading message from peer %s: empty data", id.String())
			return
		case msgData[0] != PullTransactions:
			ps.Log().Errorf("pull: wrong msg type '%d'", msgData[0])
			return
		}

		var txid base.TransactionID
		txid, err = decodePullTransactionMsg(msgData)
		if err != nil {
			ps.Log().Errorf("pull: error while decoding message: %v", err)
			return
		}

		ps.evidenceMessage()

		go ps.onReceivePullTx(id, txid)
		ps.pullRequestsIn.Inc()
	}
}

func (ps *Peers) sendPullTransactionToPeers(ids []peer.ID, txid base.TransactionID) {
	msg := _pullTransaction{
		txid: txid,
	}
	ps.sendMsgBytesOutMulti(ids, ps.lppProtocolPull, msg.Bytes())
	// a pull is broadcast to all alive peers; count one sent message per target, mirroring
	// pullRequestsIn which counts one per received frame
	ps.pullRequestsOut.Add(float64(len(ids)))
}

// PullTransactionsFromPeers sends pull request to all peers that respond to pull requests
func (ps *Peers) PullTransactionsFromPeers(txid base.TransactionID) int {
	targets := ps.allAliveIDs()
	ps.sendPullTransactionToPeers(targets, txid)
	return len(targets)
}

func encodePullTransactionMsg(txid base.TransactionID) []byte {
	var buf bytes.Buffer
	// write request type byte
	buf.WriteByte(PullTransactions)
	buf.Write(txid[:])
	return buf.Bytes()
}

func decodePullTransactionMsg(data []byte) (base.TransactionID, error) {
	if len(data) != 1+base.TransactionIDLength || data[0] != PullTransactions {
		return base.TransactionID{}, fmt.Errorf("not a pull txransactions message")
	}
	return base.TransactionIDFromBytes(data[1:])
}

// out message wrappers
type _pullTransaction struct {
	txid base.TransactionID
}

func (pt *_pullTransaction) Bytes() []byte { return encodePullTransactionMsg(pt.txid) }

func (ps *Peers) allAliveIDs() []peer.ID {
	ret := make([]peer.ID, 0)

	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		if ps._isAlive(p) {
			ret = append(ret, p.id)
		}
	}
	return ret
}
