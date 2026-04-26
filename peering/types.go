package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"
)

type (
	environment interface {
		global.NodeGlobal
	}

	Config struct {
		HostIDPrivateKey   ed25519.PrivateKey
		HostID             peer.ID
		HostPort           int
		PreConfiguredPeers map[string]_multiaddr // name -> PeerAddr. Static peers used also for bootstrap
		// MaxDynamicPeers if MaxDynamicPeers <= len(PreConfiguredPeers), autopeering is disabled, otherwise up to
		// MaxDynamicPeers - len(PreConfiguredPeers) will be auto-peered
		MaxDynamicPeers int
		// Node info
		IgnoreAllPullRequests                 bool
		AcceptPullRequestsFromStaticPeersOnly bool
		// AllowLocalIPs defines if local IPs are allowed to be used for autopeering.
		AllowLocalIPs bool `default:"false" usage:"allow local IPs to be used for autopeering"`

		// disable Quicreuse
		DisableQuicreuse bool
	}

	_multiaddr struct {
		addrString string
		multiaddr.Multiaddr
	}

	Peers struct {
		environment

		mutex            sync.RWMutex
		cfg              *Config
		stopOnce         sync.Once
		stoppedCtx       context.Context    // child of env.Ctx(); cancelled by Stop().
		stop             context.CancelFunc // cancels stoppedCtx; used by background goroutines (e.g. scheduleStaticReconnect) to bail when peering is shutting down even if env's ctx is still alive (e.g. across tests).
		host             host.Host
		kademliaDHT      *dht.IpfsDHT // not nil if autopeering is enabled
		routingDiscovery *routing.RoutingDiscovery
		peers            map[peer.ID]*Peer // except self/host
		// staticPeers maps preconfigured peer IDs to their multiaddr — used to
		// distinguish "static" from "dynamic" and (eventually) for re-bootstrap
		// after restart. The Peer struct in `peers` is the source of truth for
		// connection state; this map is metadata only.
		staticPeers     map[peer.ID]multiaddr.Multiaddr
		lastMsgReceived atomic.Int64
		// reconnecting tracks in-flight static-peer reconnect goroutines so duplicate
		// dial attempts (e.g. simultaneous Notifiee.Disconnected + initial-dial
		// failure) don't spawn parallel dials for the same peer. Guarded by mutex.
		reconnecting set.Set[peer.ID]

		// on receive handlers
		onReceiveTx     func(from peer.ID, txBytes []byte, mdata *txmetadata.TransactionMetadata, txIDPrefix base.TransactionID)
		onReceivePullTx func(from peer.ID, txid base.TransactionID)
		// lpp protocol names
		lppProtocolGossip protocol.ID
		lppProtocolPull   protocol.ID
		rendezvousString  string
		metrics
	}
	peersStats struct {
		peersAll         int
		peersStatic      int
		peersDead        int
		peersAlive       int
		peersPullTargets int
	}

	peerStream struct {
		mutex  sync.RWMutex
		stream network.Stream
	}

	Peer struct {
		id                  peer.ID
		name                string
		streams             map[protocol.ID]*peerStream
		isStatic            bool // statically pre-configured (manual peering)
		whenAdded           time.Time
		lastLoggedConnected bool // dedups CONNECTED/LOST CONNECTION log lines
		// msg counters
		numIncomingPull int
		numIncomingTx   int
	}
)

const Name = "peers"

const (
	// protocol name templates. Last component is first 8 bytes of ledger constraint library hash, interpreted as bigendian uint64
	// Peering is only possible between same versions of the ledger.
	// Nodes with different versions of the ledger constraints will just ignore each other
	lppProtocolGossip = "/proxima/gossip/%d"
	lppProtocolPull   = "/proxima/pull/%d"

	logPeersEvery = 10 * time.Second
)

func readPeeringConfig() (*Config, error) {
	cfg := &Config{
		PreConfiguredPeers: make(map[string]_multiaddr),
	}
	cfg.HostPort = viper.GetInt("peering.host.port")
	if cfg.HostPort == 0 {
		return nil, fmt.Errorf("peering.host.port: wrong port")
	}
	pkStr := viper.GetString("peering.host.id_private_key")
	pkBin, err := hex.DecodeString(pkStr)
	if err != nil {
		return nil, fmt.Errorf("host.id_private_key: wrong id private key: %v", err)
	}
	if len(pkBin) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("host.private_key: wrong host id private key size")
	}
	cfg.HostIDPrivateKey = pkBin

	encodedHostID := viper.GetString("peering.host.id")
	cfg.HostID, err = peer.Decode(encodedHostID)
	if err != nil {
		return nil, fmt.Errorf("can't decode host id: %v", err)
	}
	privKey, err := p2pcrypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("UnmarshalEd25519PrivateKey: %v", err)
	}

	if !cfg.HostID.MatchesPrivateKey(privKey) {
		return nil, fmt.Errorf("config: host private key does not match hostID")
	}

	peerNames := util.KeysSorted(viper.GetStringMap("peering.peers"), func(k1, k2 string) bool {
		return k1 < k2
	})

	for _, peerName := range peerNames {
		addrString := viper.GetString("peering.peers." + peerName)
		maddr, err := multiaddr.NewMultiaddr(addrString)
		if err != nil {
			return nil, fmt.Errorf("can't parse multiaddress: %w", err)
		}
		cfg.PreConfiguredPeers[peerName] = _multiaddr{
			addrString: addrString,
			Multiaddr:  maddr,
		}
	}

	cfg.MaxDynamicPeers = viper.GetInt("peering.max_dynamic_peers")
	if cfg.MaxDynamicPeers < 0 {
		cfg.MaxDynamicPeers = 0
	}

	cfg.IgnoreAllPullRequests = viper.GetBool("peering.ignore_pull_requests")
	cfg.AcceptPullRequestsFromStaticPeersOnly = viper.GetBool("peering.pull_requests_from_static_peers_only")
	cfg.AllowLocalIPs = viper.GetBool("peering.allow_local_ips")

	cfg.DisableQuicreuse = viper.GetBool("peering.disable_quicreuse")
	return cfg, nil
}
