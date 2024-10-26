package peering

import (
	"bytes"
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// pull request message 1st byte is the type of the message. The rest is message body

const PullTransactions = byte(iota)

func (ps *Peers) pullStreamHandler(stream network.Stream) {

	defer func() {
		stream.Close()
		ps.Log().Errorf("[peering] pull: streamHandler exit")
	}()

	if ps.cfg.IgnoreAllPullRequests {
		// ignore all pull requests
		return
	}

	ps.Log().Infof("[peering] pull: ******** streamHandler started")
	id := stream.Conn().RemotePeer()

	known, blacklisted, static := ps.knownPeer(id, func(p *Peer) {
	})
	if blacklisted {
		// just ignore
		return
	}
	if !known {
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Log().Errorf("[peering] node does not take any incoming dynamic peers")
			return
		}
		ps.Log().Infof("[peering] incoming peer request. Add new dynamic peer %s", id.String())
	}

	if !static && ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
		// ignore pull requests from automatic peers
		return
	}

	// receive start
	_, err := readFrame(stream)
	if err != nil {
		ps.Log().Errorf("[peering] hb: error while reading start message from peer %s: err='%v'", ShortPeerIDString(id), err)
		return
	}
	for {
		msgData, err := readFrame(stream)
		_, blacklisted, _ := ps.knownPeer(id, func(p *Peer) {
			p.numIncomingPull++
		})
		if blacklisted {
			// just ignore
			return
		}

		ps.inMsgCounter.Inc()
		switch {
		case err != nil:
			ps.Log().Error("pull: error while reading message from peer %s: %v", id.String(), err)
			return
		case len(msgData) == 0:
			ps.Log().Error("pull: error while reading message from peer %s: empty data", id.String())
			return
		case msgData[0] != PullTransactions:
			ps.Log().Error("pull: wrong msg type '%d'", msgData[0])
			return
		}

		var txid ledger.TransactionID
		txid, err = decodePullTransactionMsg(msgData)
		if err != nil {
			ps.Log().Error("pull: error while decoding message: %v", err)
			return
		}
		go ps.onReceivePullTx(id, txid)
		ps.pullRequestsIn.Inc()
	}
}

func (ps *Peers) sendPullTransactionToPeers(ids []peer.ID, txid ledger.TransactionID) {
	msg := _pullTransaction{
		txid: txid,
	}
	ps.sendMsgBytesOutMulti(ids, ps.lppProtocolPull, msg.Bytes())
}

// PullTransactionsFromNPeers sends pull request to the random peer which has txStore
// Return number of peer pull request was sent to
func (ps *Peers) PullTransactionsFromNPeers(nPeers int, txid ledger.TransactionID) int {
	util.Assertf(nPeers >= 1, "nPeers")

	targets := ps.chooseNPullTargets(nPeers)
	ps.sendPullTransactionToPeers(targets, txid)
	return len(targets)
}

func encodePullTransactionMsg(txid ledger.TransactionID) []byte {
	var buf bytes.Buffer
	// write request type byte
	buf.WriteByte(PullTransactions)
	buf.Write(txid[:])
	return buf.Bytes()
}

func decodePullTransactionMsg(data []byte) (ledger.TransactionID, error) {
	if len(data) != 1+ledger.TransactionIDLength || data[0] != PullTransactions {
		return ledger.TransactionID{}, fmt.Errorf("not a pull txransactions message")
	}
	return ledger.TransactionIDFromBytes(data[1:])
}

func (ps *Peers) _isPullTarget(p *Peer) bool {
	return p.respondsToPullRequests || ps.cfg.ForcePullFromAllPeers
}

// out message wrappers
type _pullTransaction struct {
	txid ledger.TransactionID
}

func (pt *_pullTransaction) Bytes() []byte { return encodePullTransactionMsg(pt.txid) }
