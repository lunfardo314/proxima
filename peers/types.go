package peers

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	Peers interface {
		SelfID() PeerID
		SelectRandomPeer() Peer
		BroadcastToPeers(msgBytes []byte, except ...PeerID)
	}

	Peer interface {
		ID() PeerID
		SendMsgBytes(msgBytes []byte) bool
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
}

var _ Peer = &peerImpl{}

//

func NewPeer() *peerImpl {
	return &peerImpl{}
}

func (p *peerImpl) ID() PeerID {
	return ""
}

func (p *peerImpl) SendMsgBytes(msgBytes []byte) bool {
	return true
}

const ttlBloomFilterEntry = 10 * time.Second

type peeringImpl struct {
	mutex sync.RWMutex
	peers []*peerImpl // except self
}

var _ Peers = &peeringImpl{}

func NewDummyPeering() *peeringImpl {
	return &peeringImpl{}
}

func (p *peeringImpl) SelectRandomPeer() Peer {
	return &peerImpl{}
}

func (p *peeringImpl) BroadcastToPeers(msgBytes []byte, except ...PeerID) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	for _, peer := range p.peers {
		if len(except) > 0 && peer.ID() == except[0] {
			continue
		}
		peerCopy := peer
		go peerCopy.SendMsgBytes(msgBytes)
	}
}

func (p *peeringImpl) SelfID() PeerID {
	return ""
}
