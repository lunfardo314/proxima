package peering

import (
	"math/rand"
	"sync"

	"github.com/lunfardo314/proxima/util"
)

type (
	Peers interface {
		OnReceiveMessage(fun func(msgBytes []byte, from PeerID))
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

type peers struct {
	mutex     sync.RWMutex
	peers     map[PeerID]*peerImpl // except self
	onReceive func(msgBytes []byte, from PeerID)
}

var _ Peers = &peers{}

func NewPeers() *peers {
	return &peers{
		peers:     make(map[PeerID]*peerImpl),
		onReceive: func(_ []byte, _ PeerID) {},
	}
}

func (ps *peers) SendMsgBytesToPeer(id PeerID, msgBytes []byte) bool {
	if p, ok := ps.peers[id]; ok {
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *peers) SendMsgBytesToRandomPeer(msgBytes []byte) bool {
	peers := util.Values(ps.peers)
	if len(peers) > 0 {
		p := peers[rand.Intn(len(peers))]
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *peers) BroadcastToPeers(msgBytes []byte, except ...PeerID) {
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

func (ps *peers) SelfID() PeerID {
	return ""
}

func (ps *peers) OnReceiveMessage(fun func(msgBytes []byte, from PeerID)) {
	ps.onReceive = fun
}
