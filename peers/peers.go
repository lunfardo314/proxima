package peers

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	Peers interface {
		SelfID() PeerID
		SendMsgBytesToPeer(id PeerID, msgBytes []byte) bool
		SendMsgBytesToRandomPeer(msgBytes []byte) bool
		BroadcastToPeers(msgBytes []byte, except ...PeerID)
	}

	PeerID string
)

const (
	PeerMessageTypeUndef = byte(iota)
	PeerMessageTypeQueryTransactions
	PeerMessageTypeTxBytes
)

func EncodePeerMessageTypeQueryTransactions(txids ...core.TransactionID) []byte {
	util.Assertf(len(txids) < math.MaxUint16, "too many transaction IDs")

	var buf bytes.Buffer
	buf.WriteByte(PeerMessageTypeQueryTransactions)
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txids)))
	buf.Write(size[:])
	for i := range txids {
		buf.Write(txids[i][:])
	}
	return buf.Bytes()
}

func DecodePeerMessageTypeQueryTransactions(data []byte) ([]core.TransactionID, error) {
	if len(data) < 3 || data[0] != PeerMessageTypeQueryTransactions {
		return nil, fmt.Errorf("not a QueryTransactions message")
	}
	ret := make([]core.TransactionID, binary.BigEndian.Uint16(data[1:3]))
	rdr := bytes.NewReader(data[3:])
	var txid [core.TransactionIDLength]byte
	for i := range ret {
		n, err := rdr.Read(txid[:])
		if err != nil || n != core.TransactionIDLength {
			return nil, fmt.Errorf("DecodePeerMessageTypeQueryTransactions: wrong msg data")
		}
		ret[i], err = core.TransactionIDFromBytes(txid[:])
		common.AssertNoError(err)
	}
	return ret, nil
}

func EncodePeerMessageTypeTxBytes(txBytes []byte) []byte {
	util.Assertf(len(txBytes) < math.MaxUint16, "too long transaction bytes")

	var buf bytes.Buffer
	buf.WriteByte(PeerMessageTypeTxBytes)
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txBytes)))
	buf.Write(size[:])
	buf.Write(txBytes)
	return buf.Bytes()
}

func DecodePeerMessageTypeTxBytes(data []byte) ([]byte, error) {
	if len(data) < 3 || data[0] != PeerMessageTypeTxBytes {
		return nil, fmt.Errorf("not a TxBytes message")
	}
	size := binary.BigEndian.Uint16(data[1:3])
	if len(data) != int(size)+3 {
		return nil, fmt.Errorf("DecodePeerMessageTypeTxBytes: wrong data length")
	}
	return data[3:], nil
}

// for testing

type peerImpl struct {
	mutex sync.RWMutex
	id    PeerID
}

func (p *peerImpl) ID() PeerID {
	return ""
}

func (p *peerImpl) sendMsgBytes(msgBytes []byte) bool {
	return true
}

type peeringImpl struct {
	mutex sync.RWMutex
	peers map[PeerID]*peerImpl // except self
}

var _ Peers = &peeringImpl{}

func NewDummyPeering() *peeringImpl {
	return &peeringImpl{}
}

func (ps *peeringImpl) SendMsgBytesToPeer(id PeerID, msgBytes []byte) bool {
	if p, ok := ps.peers[id]; ok {
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *peeringImpl) SendMsgBytesToRandomPeer(msgBytes []byte) bool {
	peers := util.Values(ps.peers)
	if len(peers) > 0 {
		p := peers[rand.Intn(len(peers))]
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *peeringImpl) BroadcastToPeers(msgBytes []byte, except ...PeerID) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, peer := range ps.peers {
		if len(except) > 0 && peer.ID() == except[0] {
			continue
		}
		peerCopy := peer
		go peerCopy.sendMsgBytes(msgBytes)
	}
}

func (ps *peeringImpl) SelfID() PeerID {
	return ""
}
