package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// pull request message 1st byte is the type of the message. The rest is message body

const (
	MaxNumTransactionID = (MaxPayloadSize - 2) / ledger.TransactionIDLength

	PullRequestTransactions = byte(iota)
	PullRequestBrancheTips
)

func (ps *Peers) pullStreamHandler(stream network.Stream) {
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

	msgData, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	if err = ps.processPullFrame(msgData, p); err != nil {
		ps.log.Errorf("error while decoding message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return

	}
	_ = stream.Close()
}

func (ps *Peers) processPullFrame(msgData []byte, p *Peer) error {
	if len(msgData) == 0 {
		return fmt.Errorf("expected pull message, got empty frame")
	}
	switch msgData[0] {
	case PullRequestTransactions:
		txLst, err := decodePullTransactionsMsg(msgData)
		if err != nil {
			return err
		}
		p.evidenceActivity(ps, "pullTx")
		ps.onReceivePullTx(p.id, txLst)

	case PullRequestBrancheTips:
		if err := decodePullBranchTipsMsg(msgData); err != nil {
			return err
		}
		p.evidenceActivity(ps, "pullTips")
		ps.onReceivePullTips(p.id)

	default:
		return fmt.Errorf("unsupported type of the pull message %d", msgData[0])
	}
	return nil
}

func (ps *Peers) sendPullTransactionsToPeer(id peer.ID, txLst ...ledger.TransactionID) {
	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolPull)
	if err != nil {
		return
	}
	defer stream.Close()

	_ = writeFrame(stream, encodePullTransactionsMsg(txLst...))
}

// PullTransactionsFromRandomPeer sends pull request to the random peer which has txStore
func (ps *Peers) PullTransactionsFromRandomPeer(txids ...ledger.TransactionID) bool {
	if len(txids) == 0 {
		return false
	}
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := util.Keys(ps.peers)
	for _, idx := range rand.Perm(len(all)) {
		rndID := all[idx]
		p := ps.peers[rndID]
		if p.isCommunicationOpen() && p.isAlive() && p.HasTxStore() {
			global.TracePull(ps.log, "pull from random peer %s: %s",
				func() any { return ShortPeerIDString(rndID) },
				func() any { return _txidLst(txids...) },
			)

			ps.sendPullTransactionsToPeer(rndID, txids...)
			return true
		}
	}
	return false
}

func _txidLst(txids ...ledger.TransactionID) string {
	ret := make([]string, len(txids))
	for i := range ret {
		ret[i] = txids[i].StringShort()
	}
	return strings.Join(ret, ",")
}

func encodePullTransactionsMsg(txids ...ledger.TransactionID) []byte {
	util.Assertf(len(txids) <= MaxNumTransactionID, "number of transactions IDS %d exceed maximum %d", len(txids), MaxNumTransactionID)

	var buf bytes.Buffer
	// write request type byte
	buf.WriteByte(PullRequestTransactions)
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
	if len(data) < 3 || data[0] != PullRequestTransactions {
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
		common.AssertNoError(err)
	}
	return ret, nil
}

func encodePullBranchTipsMsg() []byte {
	return []byte{PullRequestBrancheTips}
}

func decodePullBranchTipsMsg(data []byte) error {
	if len(data) != 1 || data[0] != PullRequestBrancheTips {
		return fmt.Errorf("not a pull branch tips message")
	}
	return nil
}
