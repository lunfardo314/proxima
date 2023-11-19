package peering

import (
	"math/rand"
	"sync"

	"github.com/lunfardo314/proxima/util"
)

type (
	Peers struct {
		mutex     sync.RWMutex
		peers     map[PeerID]*peerImpl // except self
		onReceive func(msgBytes []byte, from PeerID)
	}

	PeerID string
)

const (
	PeerMessageTypeQueryTransactions = byte(iota)
	PeerMessageTypeTxBytes
)

func NewPeers() *Peers {
	return &Peers{
		peers:     make(map[PeerID]*peerImpl),
		onReceive: func(_ []byte, _ PeerID) {},
	}
}

func (ps *Peers) SendMsgBytesToPeer(id PeerID, msgBytes []byte) bool {
	if p, ok := ps.peers[id]; ok {
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *Peers) SendMsgBytesToRandomPeer(msgBytes []byte) bool {
	peers := util.Values(ps.peers)
	if len(peers) > 0 {
		p := peers[rand.Intn(len(peers))]
		return p.sendMsgBytes(msgBytes)
	}
	return false
}

func (ps *Peers) BroadcastToPeers(msgBytes []byte, except ...PeerID) {
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

func (ps *Peers) SelfID() PeerID {
	return ""
}

func (ps *Peers) OnReceiveMessage(fun func(msgBytes []byte, from PeerID)) {
	ps.onReceive = fun
}

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
