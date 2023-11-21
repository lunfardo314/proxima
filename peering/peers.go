package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type (
	Config struct {
		HostIDPrivateKey ed25519.PrivateKey
		HostIDPublicKey  ed25519.PublicKey
		HostPort         int
		KnownPeers       map[string]multiaddr.Multiaddr // name -> PeerAddr
	}

	Peers struct {
		mutex           sync.RWMutex
		cfg             *Config
		log             *zap.SugaredLogger
		ctx             context.Context
		stopFun         context.CancelFunc
		host            host.Host
		peers           map[peer.ID]*Peer // except self
		onReceiveGossip func(from peer.ID, txBytes []byte)
		onReceivePull   func(from peer.ID, txids []core.TransactionID)
		traceFlag       atomic.Bool
	}

	Peer struct {
		mutex        sync.RWMutex
		name         string
		lastActivity time.Time
	}
)

const (
	lppProtocolGossip    = "/proxima/gossip/1.0.0"
	lppProtocolPull      = "/proxima/pull/1.0.0"
	lppProtocolHeartbeat = "/proxima/heartbeat/1.0.0"
)

func NewPeersDummy() *Peers {
	return &Peers{
		peers:           make(map[peer.ID]*Peer),
		onReceiveGossip: func(_ peer.ID, _ []byte) {},
		onReceivePull:   func(_ peer.ID, _ []core.TransactionID) {},
	}
}

func New(cfg *Config, ctx ...context.Context) (*Peers, error) {
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

	var ctx1 context.Context
	var stopFun context.CancelFunc

	if len(ctx) > 0 {
		ctx1, stopFun = context.WithCancel(ctx[0])
	} else {
		ctx1, stopFun = context.WithCancel(context.Background())
	}

	ret := &Peers{
		cfg:             cfg,
		log:             general.NewLogger("[peering]", zap.DebugLevel, nil, ""),
		ctx:             ctx1,
		stopFun:         stopFun,
		host:            lppHost,
		peers:           make(map[peer.ID]*Peer),
		onReceiveGossip: func(_ peer.ID, _ []byte) {},
		onReceivePull:   func(_ peer.ID, _ []core.TransactionID) {},
	}

	for name, maddr := range cfg.KnownPeers {
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			return nil, fmt.Errorf("can't get multiaddress info: %v", err)
		}
		lppHost.Peerstore().AddAddr(info.ID, maddr, peerstore.PermanentAddrTTL)

		ret.peers[info.ID] = &Peer{
			name: name,
		}
	}
	go func() {
		var stopHeartbeat atomic.Bool
		go ret.heartbeatLoop(&stopHeartbeat)

		<-ret.ctx.Done()
		stopHeartbeat.Store(true)
	}()
	return ret, nil
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

func NewPeersFromConfig(ctx context.Context) (*Peers, error) {
	cfg, err := readPeeringConfig()
	if err != nil {
		return nil, err
	}
	return New(cfg, ctx)
}

func (ps *Peers) Run() {
	ps.host.SetStreamHandler(lppProtocolGossip, ps.gossipStreamHandler)
	ps.host.SetStreamHandler(lppProtocolPull, ps.pullStreamHandler)
	ps.host.SetStreamHandler(lppProtocolHeartbeat, ps.heartbeatStreamHandler)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		ps.log.Infof("started on %v with %d configured peers", ps.host.Addrs(), len(ps.cfg.KnownPeers))
		_ = ps.log.Sync()
		wg.Done()

		<-ps.ctx.Done()
		_ = ps.host.Close()
	}()
	wg.Wait()
}

func (ps *Peers) Stop() {
	ps.log.Infof("stopping..")
	_ = ps.log.Sync()
	ps.stopFun()
}

func (ps *Peers) SetTrace(b bool) {
	ps.traceFlag.Store(b)
}

func (ps *Peers) trace(format string, args ...any) {
	if ps.traceFlag.Load() {
		ps.log.Infof("TRACE "+format, util.EvalLazyArgs(args...)...)
	}
}

func (ps *Peers) PullTransactionsFromRandomPeer(txids ...core.TransactionID) bool {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := util.Keys(ps.peers)
	for i := 0; i < len(all); i++ {
		rndID := all[rand.Intn(len(all))]
		if ps.peers[rndID].isAlive() {
			ps.sendPullToPeer(rndID, txids...)
			return true
		}
	}
	return false
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, except ...peer.ID) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for id, p := range ps.peers {
		if len(except) > 0 && id == except[0] {
			continue
		}
		if !p.isAlive() {
			continue
		}
		ps.SendTxBytesToPeer(id, txBytes)
	}
}

func (ps *Peers) OnReceiveTxBytes(fun func(from peer.ID, txBytes []byte)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceiveGossip = fun
}

func (ps *Peers) OnReceivePullRequest(fun func(from peer.ID, txids []core.TransactionID)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceivePull = fun
}

func (ps *Peers) getPeer(id peer.ID) *Peer {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	if ret, ok := ps.peers[id]; ok {
		return ret
	}
	return nil
}

func (ps *Peers) getPeerIDs() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return util.Keys(ps.peers)
}

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		return
	}

	txBytes, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		return
	}

	p.mutex.RLock()
	p.lastActivity = time.Now()
	p.mutex.RUnlock()

	ps.onReceiveGossip(id, txBytes)
}

// SendTxBytesToPeer TODO better keep stream open
func (ps *Peers) SendTxBytesToPeer(id peer.ID, txBytes []byte) bool {
	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolGossip)
	if err != nil {
		return false
	}
	defer stream.Close()

	return writeFrame(stream, txBytes) == nil
}

func (ps *Peers) pullStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		return
	}

	msgData, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		return
	}

	txLst, err := decodePeerMsgPull(msgData)
	if err != nil {
		ps.log.Errorf("error while decoding pull message from peer %s: %v", id.String(), err)
		return
	}

	p.lastActivity = time.Now()
	ps.onReceivePull(id, txLst)
}

// sendPullToPeer TODO better keep stream open
func (ps *Peers) sendPullToPeer(id peer.ID, txLst ...core.TransactionID) {
	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolPull)
	if err != nil {
		return
	}
	defer stream.Close()

	_ = writeFrame(stream, encodePeerMsgPull(txLst...))
}

func (p *Peer) isAlive() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	// peer is alive if its last activity is at least 3 heartbeats old
	return time.Now().Sub(p.lastActivity) < time.Duration(3)*heartbeatRate
}

func (ps *Peers) PeerIsAlive(id peer.ID) bool {
	p := ps.getPeer(id)
	if p == nil {
		return false
	}
	return p.isAlive()
}
