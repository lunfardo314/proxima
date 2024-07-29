package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/maps"
)

// pull request message 1st byte is the type of the message. The rest is message body

const (
	MaxNumTransactionID = (MaxPayloadSize - 2) / ledger.TransactionIDLength

	PullRequestTransactions = byte(iota)
	PullSyncPortion
)

func (ps *Peers) pullStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	if ps.isInBlacklist(id) {
		_ = stream.Reset()
		return
	}
	var err error
	var callAfter func()

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			_ = stream.Reset()
			ps.Tracef(TraceTag, "pull: unknown peer %s", id.String())
			return
		}
		var msgData []byte
		msgData, err = readFrame(stream)
		if err != nil {
			_ = stream.Reset()
			ps.Log().Errorf("error while reading message from peer %s: %v", id.String(), err)

			ps._dropPeer(p, "read error")
			return
		}
		callAfter, err = ps.processPullFrame(msgData, p)
		if err != nil {
			_ = stream.Reset()
			ps.Log().Errorf("error while decoding message from peer %s: %v", id.String(), err)

			ps._dropPeer(p, "error while parsing pull message")
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
	case PullRequestTransactions:
		txLst, err := decodePullTransactionsMsg(msgData)
		if err != nil {
			return nil, err
		}
		p._evidenceActivity("pullTx")
		fun := ps.onReceivePullTx
		callAfter = func() { fun(p.id, txLst) }

	case PullSyncPortion:
		startingFromSlot, maxBranches, err := decodeSyncPortionMsg(msgData)
		if err != nil {
			return nil, err
		}
		p._evidenceActivity("pullSync")
		fun := ps.onReceivePullSyncPortion
		callAfter = func() { fun(p.id, startingFromSlot, maxBranches) }

	default:
		return nil, fmt.Errorf("unsupported type of the pull message %d", msgData[0])
	}
	return callAfter, nil
}

func (ps *Peers) sendMsgToPeer(id peer.ID, msg []byte) {
	stream, err := ps.host.NewStream(ps.Ctx(), id, ps.lppProtocolPull)
	if err != nil {
		return
	}
	defer stream.Close()

	_ = writeFrame(stream, msg)
}

func (ps *Peers) sendPullTransactionsToPeer(id peer.ID, txLst ...ledger.TransactionID) {
	ps.sendMsgToPeer(id, encodePullTransactionsMsg(txLst...))
}

// PullTransactionsFromRandomPeer sends pull request to the random peer which has txStore
func (ps *Peers) PullTransactionsFromRandomPeer(txids ...ledger.TransactionID) bool {
	if len(txids) == 0 {
		return false
	}
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := maps.Keys(ps.peers)
	for _, idx := range rand.Perm(len(all)) {
		rndID := all[idx]
		p := ps.peers[rndID]
		_, inBlackList := ps.blacklist[p.id]
		if !inBlackList && !p._isDead() && p.hasTxStore {
			ps.Tracef(TraceTag, "pull from random peer %s: %s",
				func() any { return ShortPeerIDString(rndID) },
				func() any { return _txidLst(txids...) },
			)

			ps.sendPullTransactionsToPeer(rndID, txids...)
			return true
		}
	}
	return false
}

func (ps *Peers) PullTransactionsFromAllPeers(txids ...ledger.TransactionID) {
	if len(txids) == 0 {
		return
	}
	msg := encodePullTransactionsMsg(txids...)

	ps.forEachPeer(func(p *Peer) bool {
		_, inBlackList := ps.blacklist[p.id]
		if !inBlackList && !p._isDead() && p.hasTxStore {
			ps.sendMsgToPeer(p.id, msg)
		}
		return true
	})
}

func (ps *Peers) sendPullSyncPortionToPeer(id peer.ID, startingFrom ledger.Slot, maxSlots int) {
	ps.sendMsgToPeer(id, encodeSyncPortionMsg(startingFrom, maxSlots))
}

func (ps *Peers) PullSyncPortionFromRandomPeer(startingFrom ledger.Slot, maxSlots int) bool {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := maps.Keys(ps.peers)
	for _, idx := range rand.Perm(len(all)) {
		rndID := all[idx]
		p := ps.peers[rndID]
		_, inBlacklist := ps.blacklist[rndID]
		if !inBlacklist && !p._isDead() && p.hasTxStore && p.acceptsPullSyncRequests {
			ps.Log().Infof("[peering] pull sync portion from random peer %s. From slot: %d, up to slots: %d",
				ShortPeerIDString(rndID), int(startingFrom), maxSlots)
			ps.sendPullSyncPortionToPeer(rndID, startingFrom, maxSlots)
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
		util.AssertNoError(err)
	}
	return ret, nil
}

func encodeSyncPortionMsg(startingFrom ledger.Slot, maxSlots int) []byte {
	if maxSlots > global.MaxSyncPortionSlots {
		maxSlots = global.MaxSyncPortionSlots
	}
	util.Assertf(maxSlots < math.MaxUint16, "maxSlots < math.MaxUint16")

	var buf bytes.Buffer
	// write request type byte
	buf.WriteByte(PullSyncPortion)
	err := binary.Write(&buf, binary.BigEndian, uint32(startingFrom))
	util.AssertNoError(err)
	err = binary.Write(&buf, binary.BigEndian, uint16(maxSlots))
	util.AssertNoError(err)

	return buf.Bytes()
}

func decodeSyncPortionMsg(data []byte) (startingFrom ledger.Slot, maxSlots int, err error) {
	if len(data) != 1+4+2 || data[0] != PullSyncPortion {
		return 0, 0, fmt.Errorf("not a pull sync portion message")
	}
	startingFrom = ledger.Slot(binary.BigEndian.Uint32(data[1:5]))
	maxSlots = int(binary.BigEndian.Uint16(data[5:7]))
	return
}
