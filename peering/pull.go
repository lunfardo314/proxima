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

	id := stream.Conn().RemotePeer()
	if ps.isInBlacklist(id) {
		// just ignore
		_ = stream.Close()
		return
	}
	var err error
	var callAfter func()
	var msgData []byte

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			// just ignore
			_ = stream.Close()
			return
		}
		if !p.isStatic && ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
			// ignore pull requests from automatic peers
			_ = stream.Close()
			return
		}
		msgData, err = readFrame(stream)
		if err != nil {
			err = fmt.Errorf("pull: error while reading message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			p.errorCounter++
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
	})

	if callAfter != nil {
		// call outside lock
		callAfter()
	}
}

func (ps *Peers) processPullFrame(msgData []byte, p *Peer) (func(), error) {
	callAfter := func() {}
	if len(msgData) == 0 {
		return nil, fmt.Errorf("expected pull message, got empty frame")
	}
	switch msgData[0] {
	case PullTransactions:
		txLst, err := decodePullTransactionMsg(msgData)
		if err != nil {
			return nil, err
		}
		p._evidenceActivity("pullTx")
		fun := ps.onReceivePullTx
		callAfter = func() {
			fun(p.id, txLst)
			ps.pullRequestsIn.Inc()
		}

	default:
		return nil, fmt.Errorf("unsupported type of the pull message %d", msgData[0])
	}
	return callAfter, nil
}

func (ps *Peers) sendPullTransactionToPeer(id peer.ID, txid ledger.TransactionID) {
	ps.sendMsgOutQueued(&_pullTransactions{
		txid: txid,
	}, id, ps.lppProtocolPull)
}

// PullTransactionsFromRandomPeers sends pull request to the random peer which has txStore
// Return number of peer pull request was sent to
func (ps *Peers) PullTransactionsFromRandomPeers(nPeers int, txid ledger.TransactionID) int {
	util.Assertf(nPeers >= 1, "nPeers")

	targets := ps.randomPullPeers(nPeers)
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
	if _, inBlackList := ps.blacklist[p.id]; inBlackList {
		return false
	}
	if p.ignoresPullRequests {
		return false
	}
	//if p._isDead() {
	//	return false
	//}
	return true
}

func (ps *Peers) pullTxTargets() []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if ps._isPullTarget(p) {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
}

func (ps *Peers) randomPullPeers(nPeers int) []peer.ID {
	util.Assertf(nPeers >= 1, "nPeers >= 1")
	targets := ps.pullTxTargets()
	return util.RandomElements(nPeers, targets...)
}

// out message wrappers
type (
	_pullTransactions struct {
		txid ledger.TransactionID
	}
)

func (pt *_pullTransactions) Bytes() []byte { return encodePullTransactionMsg(pt.txid) }
func (pt *_pullTransactions) SetNow()       {}
