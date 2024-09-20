package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// pull request message 1st byte is the type of the message. The rest is message body

const MaxNumTransactionID = (MaxPayloadSize - 2) / ledger.TransactionIDLength

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
		//_ = stream.Reset()
		_ = stream.Close()
		return
	}
	var err error
	var callAfter func()
	var msgData []byte

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			// just ignore
			//_ = stream.Reset()
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
			//_ = stream.Reset()
			err = fmt.Errorf("pull: error while reading message from peer %s: %v", id.String(), err)
			ps.Log().Error(err)
			p.errorCounter++
			//ps._dropPeer(p, err.Error())
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
		txLst, err := decodePullTransactionsMsg(msgData)
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

func (ps *Peers) sendPullTransactionsToPeer(id peer.ID, txLst ...ledger.TransactionID) {
	ps.sendMsgOutQueued(&_pullTransactions{
		txids: txLst,
	}, id, ps.lppProtocolPull)
}

// PullTransactionsFromRandomPeers sends pull request to the random peer which has txStore
// Return number of peer pull request was sent to
func (ps *Peers) PullTransactionsFromRandomPeers(nPeers int, txids ...ledger.TransactionID) int {
	if len(txids) == 0 {
		return 0
	}
	util.Assertf(nPeers >= 1, "nPeers")

	targets := ps._randomPullPeers(nPeers)
	for _, rndPeerID := range targets {
		ps.sendPullTransactionsToPeer(rndPeerID, txids...)
	}
	return len(targets)
}

func (ps *Peers) PullTransactionsFromAllPeers(txids ...ledger.TransactionID) {
	if len(txids) == 0 {
		return
	}
	msg := &_pullTransactions{txids: txids}
	pullTargets := ps._pullTxTargets()
	for _, id := range pullTargets {
		ps.sendMsgOutQueued(msg, id, ps.lppProtocolPull)
	}
	ps.pullRequestsOut.Add(float64(len(pullTargets)))
}

func encodePullTransactionsMsg(txids ...ledger.TransactionID) []byte {
	util.Assertf(len(txids) <= MaxNumTransactionID, "number of transactions IDS %d exceed maximum %d", len(txids), MaxNumTransactionID)

	var buf bytes.Buffer
	// write request type byte
	buf.WriteByte(PullTransactions)
	// write number of transactions
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txids)))
	buf.Write(size[:])
	// write raw transaction IDs
	for i := range txids {
		buf.Write(txids[i][:])
	}
	return buf.Bytes()
}

func decodePullTransactionsMsg(data []byte) ([]ledger.TransactionID, error) {
	if len(data) < 3 || data[0] != PullTransactions {
		return nil, fmt.Errorf("not a pull txransactions message")
	}
	// read size of array
	ret := make([]ledger.TransactionID, binary.BigEndian.Uint16(data[1:3]))
	rdr := bytes.NewReader(data[3:])
	var txid [ledger.TransactionIDLength]byte
	for i := range ret {
		n, err := rdr.Read(txid[:])
		if err != nil || n != ledger.TransactionIDLength {
			return nil, fmt.Errorf("DecodePeerMessageQueryTransactions: wrong msg data")
		}
		ret[i], err = ledger.TransactionIDFromBytes(txid[:])
		util.AssertNoError(err)
	}
	return ret, nil
}

func (ps *Peers) _pullTxTargets(restrictedTargets ...string) []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeer(func(p *Peer) bool {
		if len(restrictedTargets) == 0 || slices.Contains(restrictedTargets, p.name) {
			if _, inBlackList := ps.blacklist[p.id]; !inBlackList && !p._isDead() && !p.ignoresAllPullRequests {
				ret = append(ret, p.id)
			}
		}
		return true
	})
	return ret
}

func (ps *Peers) _randomPullPeers(nPeers int, restrictedTargets ...string) []peer.ID {
	util.Assertf(nPeers >= 1, "nPeers >= 1")
	targets := ps._pullTxTargets(restrictedTargets...)
	return util.RandomElements(nPeers, targets...)
}

// out message wrappers
type (
	_pullTransactions struct {
		txids []ledger.TransactionID
	}
)

func (pt *_pullTransactions) Bytes() []byte { return encodePullTransactionsMsg(pt.txids...) }
func (pt *_pullTransactions) SetNow()       {}
