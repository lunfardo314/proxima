package peering

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sync"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

type (
	Peers struct {
		mutex     sync.RWMutex
		host      host.Host
		peers     map[PeerID]*peerImpl // except self
		onReceive func(msgBytes []byte, from PeerID)
	}

	PeerID string
)

const (
	PeerMessageTypeQueryTransactions = byte(iota)
	PeerMessageTypeTxBytes
)

func NewPeersDummy() *Peers {
	return &Peers{
		peers:     make(map[PeerID]*peerImpl),
		onReceive: func(_ []byte, _ PeerID) {},
	}
}

func NewPeers(idPrivateKey ed25519.PrivateKey, port int) (*Peers, error) {
	privKey, err := crypto.UnmarshalEd25519PrivateKey(idPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("wrong private key: %w", err)
	}
	lppHost, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.NoSecurity,
	)
	if err != nil {
		return nil, fmt.Errorf("unable create libp2p host: %w", err)
	}
	return &Peers{
		host:      lppHost,
		peers:     make(map[PeerID]*peerImpl),
		onReceive: func(_ []byte, _ PeerID) {},
	}, nil
}

func NewPeersFromConfig() (*Peers, error) {
	port := viper.GetInt("host.port")
	if port == 0 {
		return nil, fmt.Errorf("host.port: wrong port")
	}
	pkStr := viper.GetString("host.private_key")
	pkBin, err := hex.DecodeString(pkStr)
	if err != nil {
		return nil, fmt.Errorf("host.private_key: wrong id private key: %v", err)
	}
	pk := ed25519.PrivateKey(pkBin)
	return NewPeers(pk, port)
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
