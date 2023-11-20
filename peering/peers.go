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
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"
)

type (
	Config struct {
		HostIDPrivateKey ed25519.PrivateKey
		HostIDPublicKey  ed25519.PublicKey
		HostPort         int
		KnownPeers       map[string]multiaddr.Multiaddr // name -> PeerAddr
	}

	Peers struct {
		mutex                sync.RWMutex
		cfg                  *Config
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

const lppProtocolGossip = "/proxima/gossip/1.0.0"

func NewPeersDummy() *Peers {
	return &Peers{
		peers:                make(map[PeerID]*peerImpl),
		onReceiveTxBytes:     func(_ PeerID, _ []byte) {},
		onReceivePullRequest: func(_ PeerID, _ []core.TransactionID) {},
	}
}

func New(cfg *Config) (*Peers, error) {
	hostIDPrivateKey, err := crypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("wrong private key: %w", err)
	}
	lppHost, err := libp2p.New(
		libp2p.Identity(hostIDPrivateKey),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.HostPort)),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.NoSecurity,
	)
	if err != nil {
		return nil, fmt.Errorf("unable create libp2p host: %w", err)
	}

	for _, maddr := range cfg.KnownPeers {
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			return nil, fmt.Errorf("can't get multiaddress info: %v", err)
		}
		lppHost.Peerstore().AddAddr(info.ID, maddr, peerstore.PermanentAddrTTL)
	}
	return &Peers{
		cfg:                  cfg,
		host:                 lppHost,
		peers:                make(map[PeerID]*peerImpl),
		onReceiveTxBytes:     func(_ PeerID, _ []byte) {},
		onReceivePullRequest: func(_ PeerID, _ []core.TransactionID) {},
	}, nil
}

func readPeeringConfig() (*Config, error) {
	cfg := &Config{
		KnownPeers: make(map[string]multiaddr.Multiaddr),
	}
	cfg.HostPort = viper.GetInt("peering.host.port")
	if cfg.HostPort == 0 {
		return nil, fmt.Errorf("peering.host.port: wrong port")
	}
	pkStr := viper.GetString("peering.host.private_key")
	pkBin, err := hex.DecodeString(pkStr)
	if err != nil {
		return nil, fmt.Errorf("host.private_key: wrong id private key: %v", err)
	}
	cfg.HostIDPrivateKey = pkBin

	pkStr = viper.GetString("peering.host.public_key")
	pkBin, err = hex.DecodeString(pkStr)
	if err != nil {
		return nil, fmt.Errorf("host.public_key: wrong id public key: %v", err)
	}
	cfg.HostIDPublicKey = pkBin
	if !cfg.HostIDPublicKey.Equal(cfg.HostIDPrivateKey.Public().(ed25519.PublicKey)) {
		return nil, fmt.Errorf("inconsistent host ID pivate and public keys")
	}

	peerNames := util.KeysSorted(viper.GetStringMap("peering.peers"), func(k1, k2 string) bool {
		return k1 < k2
	})

	for _, peerName := range peerNames {
		addrString := viper.GetString("peering.peers." + peerName)
		if cfg.KnownPeers[peerName], err = multiaddr.NewMultiaddr(addrString); err != nil {
			return nil, fmt.Errorf("can't parse multiaddress: %w", err)
		}
	}
	return cfg, nil
}

func NewPeersFromConfig() (*Peers, error) {
	cfg, err := readPeeringConfig()
	if err != nil {
		return nil, err
	}
	return New(cfg)
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
