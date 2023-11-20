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
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

type (
	Peers struct {
		mutex                sync.RWMutex
		host                 host.Host
		peers                map[PeerID]*peerImpl // except self
		onReceiveTxBytes     func(from PeerID, txBytes []byte)
		onReceivePullRequest func(from PeerID, txids []core.TransactionID)
	}

	PeerID string
)

const (
	PeerMessageTypeQueryTransactions = byte(iota)
	PeerMessageTypeTxBytes
)

func NewPeersDummy() *Peers {
	return &Peers{
		peers:                make(map[PeerID]*peerImpl),
		onReceiveTxBytes:     func(_ PeerID, _ []byte) {},
		onReceivePullRequest: func(_ PeerID, _ []core.TransactionID) {},
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
		host:                 lppHost,
		peers:                make(map[PeerID]*peerImpl),
		onReceiveTxBytes:     func(_ PeerID, _ []byte) {},
		onReceivePullRequest: func(_ PeerID, _ []core.TransactionID) {},
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

func (ps *Peers) PullTransactionsFromRandomPeer(txids ...core.TransactionID) bool {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	if len(ps.peers) == 0 {
		return false
	}
	msgBytes := encodePeerMessageQueryTransactions(txids...)
	peers := util.Values(ps.peers)
	p := peers[rand.Intn(len(peers))]
	return p.sendMsgBytes(msgBytes)

}

func (ps *Peers) SendTxBytesToPeer(txBytes []byte, peerID PeerID) bool {
	ps.mutex.RLock()
	ps.mutex.RUnlock()

	peer, ok := ps.peers[peerID]
	if !ok {
		return false
	}
	return peer.sendMsgBytes(encodePeerMessageTxBytes(txBytes))
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, except ...PeerID) {
	msgBytes := encodePeerMessageTxBytes(txBytes)

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

func (ps *Peers) OnReceiveTxBytes(fun func(from PeerID, txBytes []byte)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceiveTxBytes = fun
}

func (ps *Peers) OnReceivePullRequest(fun func(from PeerID, txids []core.TransactionID)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceivePullRequest = fun
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
