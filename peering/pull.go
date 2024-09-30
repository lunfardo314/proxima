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
	ps.inMsgCounter.Inc()
	if ps.cfg.IgnoreAllPullRequests {
		// ignore all pull requests
		_ = stream.Close()
		return
	}

	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	id := stream.Conn().RemotePeer()
	if ps._isInBlacklist(id) {
		// just ignore
		_ = stream.Close()
		return
	}

	p := ps._getPeer(id)
	if p == nil {
		// ignore
		_ = stream.Close()
		return
	}

	if !p.isStatic && ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
		// ignore pull requests from automatic peers
		_ = stream.Close()
		return
	}

	var err error
	var callAfter func()
	var msgData []byte

	msgData, err = readFrame(stream)
	if err != nil {
		ps.Log().Error("pull: error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Close()
		return
	}
	callAfter, err = ps.processPullFrame(msgData, p)
	if err != nil {
		// protocol violation
		err = fmt.Errorf("pull: error while decoding message from peer %s: %v", id.String(), err)
		ps.Log().Error(err)
		ps._dropPeer(p, err.Error())
		_ = stream.Reset()
		return
	}
	_ = stream.Close()
	callAfter()
}

func (ps *Peers) processPullFrame(msgData []byte, p *Peer) (callAfter func(), err error) {
	callAfter = func() {}
	if len(msgData) == 0 {
		return nil, fmt.Errorf("expected pull message, got empty frame")
	}
	switch msgData[0] {
	case PullTransactions:
		var txid ledger.TransactionID
		txid, err = decodePullTransactionMsg(msgData)
		if err != nil {
			return
		}
		fun := ps.onReceivePullTx
		callAfter = func() {
			fun(p.id, txid)
			ps.pullRequestsIn.Inc()
		}
	default:
		err = fmt.Errorf("unsupported type of the pull message %d", msgData[0])
	}
	return
}

func (ps *Peers) sendPullTransactionToPeer(id peer.ID, txid ledger.TransactionID) {
	msg := _pullTransaction{
		txid: txid,
	}
	ps.sendMsgBytesOut(id, ps.lppProtocolPull, msg.Bytes())
}

func (ps *Peers) sendPullTransactionToPeerQueued(id peer.ID, txid ledger.TransactionID) {
	ps.sendMsgOutQueued(&_pullTransaction{
		txid: txid,
	}, id, ps.lppProtocolPull)
}

// PullTransactionsFromNPeers sends pull request to the random peer which has txStore
// Return number of peer pull request was sent to
func (ps *Peers) PullTransactionsFromNPeers(nPeers int, txid ledger.TransactionID) int {
	util.Assertf(nPeers >= 1, "nPeers")

	targets := ps.chooseNPullTargets(nPeers)
	for _, rndPeerID := range targets {
		ps.sendPullTransactionToPeer(rndPeerID, txid)
	}
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

func (pt *_pullTransaction) Bytes() []byte   { return encodePullTransactionMsg(pt.txid) }
func (pt *_pullTransaction) SetNow()         {}
func (pt *_pullTransaction) Counter() uint32 { return 0 }
