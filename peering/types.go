package peering

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sync"
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
	"github.com/lunfardo314/proxima/ledger"
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
		// used for testing only. Otherwise, remote peer sets the pull flags
		ForcePullFromAllPeers bool
		// timeout for heartbeat. If not set, used special defaultSendHeartbeatTimeout
		SendTimeoutHeartbeat time.Duration

		BlacklistTTL   int
		CooloffListTTL int

		// disable Quicreuse
		DisableQuicreuse bool
	}

	_multiaddr struct {
		addrString string
		multiaddr.Multiaddr
	}

	staticPeerInfo struct {
		maddr      multiaddr.Multiaddr
		name       string
		addrString string
	}

	Peers struct {
		environment

		mutex            sync.RWMutex
		cfg              *Config
		stopOnce         sync.Once
		host             host.Host
		kademliaDHT      *dht.IpfsDHT // not nil if autopeering is enabled
		routingDiscovery *routing.RoutingDiscovery
		peers            map[peer.ID]*Peer // except self/host
		staticPeers      map[peer.ID]*staticPeerInfo
		blacklist        map[peer.ID]_deadlineWithReason
		cooloffList      map[peer.ID]time.Time
		connectList      set.Set[peer.ID]

		// on receive handlers
		onReceiveTx     func(from peer.ID, txBytes []byte, mdata *txmetadata.TransactionMetadata)
		onReceivePullTx func(from peer.ID, txid ledger.TransactionID)
		// lpp protocol names
		lppProtocolGossip    protocol.ID
		lppProtocolPull      protocol.ID
		lppProtocolHeartbeat protocol.ID
		rendezvousString     string
		metrics
	}

	_deadlineWithReason struct {
		time.Time
		reason string
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
		id                     peer.ID
		name                   string
		streams                map[protocol.ID]*peerStream
		isStatic               bool // statically pre-configured (manual peering)
		respondsToPullRequests bool // from hb info
		whenAdded              time.Time
		lastHeartbeatReceived  time.Time
		lastLoggedConnected    bool // toggle
		// ring buffer with last clock differences
		clockDifferences         [10]time.Duration
		clockDifferencesIdx      int
		clockDifferenceQuartiles [3]time.Duration
		// ring buffer with durations between subsequent HB messages
		hbMsgDifferences         [10]time.Duration
		hbMsgDifferencesIdx      int
		hbMsgDifferenceQuartiles [3]time.Duration
		// ranks
		rankByLastHBReceived  int
		rankByClockDifference int
		// msg counters
		numIncomingHB   int
		numIncomingPull int
		numIncomingTx   int

		numHBSendErr int
	}
)

const (
	Name     = "peers"
	TraceTag = Name
)

const (
	// protocol name templates. Last component is first 8 bytes of ledger constraint library hash, interpreted as bigendian uint64
	// Peering is only possible between same versions of the ledger.
	// Nodes with different versions of the ledger constraints will just ignore each other
	lppProtocolGossip    = "/proxima/gossip/%d"
	lppProtocolPull      = "/proxima/pull/%d"
	lppProtocolHeartbeat = "/proxima/heartbeat/%d"

	// clockTolerance is how big the difference between local and remote clocks is tolerated.
	// The difference includes difference between local clocks (positive or negative) plus
	// positive heartbeat message latency between peers
	// In any case nodes has interest to sync their clocks with global reference.
	// This constant indicates when to drop the peer
	clockTolerance = 4 * time.Second

	// if the node is bootstrap, and it has configured less than numMaxDynamicPeersForBootNodeAtLeast
	// of dynamic peer cap, use this instead
	//numMaxDynamicPeersForBootNodeAtLeast = 10

	// heartbeatRate heartbeat issued every period
	heartbeatRate      = 2 * time.Second
	aliveNumHeartbeats = 10 // if no hb over this period, it means not-alive -> dynamic peer will be dropped
	aliveDuration      = time.Duration(aliveNumHeartbeats) * heartbeatRate
	blacklistTTL       = 2 * time.Minute
	cooloffTTL         = 10 * time.Second
	// gracePeriodAfterAdded period of time peer is considered not dead after added even if messages are not coming
	gracePeriodAfterAdded = 15 * heartbeatRate
	logPeersEvery         = 5 * time.Second

	/*
	   ChatGPT:
	   For Quick UDP Internet Connections (QUIC), the dial timeout can vary depending on the network environment and application requirements.
	   Typically, the recommended dial timeout is in the range of 10 to 60 seconds. A shorter timeout (e.g., 10-15 seconds) is
	   common for applications where responsiveness is critical, while longer timeouts (e.g., 30-60 seconds) may be suitable
	   for more stable or less time-sensitive environments.

	   In some scenarios, particularly when using QUIC in environments like Cloudflare Tunnels,
	   the timeout for failed connections is reported to be around 60 seconds (GitHub). For OPC UA (which isn't QUIC but a
	   similar protocol for different purposes), various timeouts are set between 10-60 seconds depending on the
	   operation being performed (OPC Labs Knowledge Base). This suggests a reasonable ballpark range for QUIC timeouts too.

	   However, the ideal timeout depends on how tolerant the system is to network latency and connection delays.
	*/

	// default timeouts for QUIC
	// HB must dial usually
	defaultSendHeartbeatTimeout = 10 * time.Second
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
		return nil, fmt.Errorf("can't decode host ID: %v", err)
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

	cfg.SendTimeoutHeartbeat = time.Duration(viper.GetInt("peering.send_timeout_hb_millis")) * time.Millisecond
	if cfg.SendTimeoutHeartbeat == 0 {
		cfg.SendTimeoutHeartbeat = defaultSendHeartbeatTimeout
	}
	cfg.BlacklistTTL = viper.GetInt("blacklist_ttl")
	if cfg.BlacklistTTL == 0 {
		cfg.BlacklistTTL = int(blacklistTTL)
	}
	cfg.CooloffListTTL = viper.GetInt("coolofflist_ttl")
	if cfg.CooloffListTTL == 0 {
		cfg.CooloffListTTL = int(cooloffTTL)
	}
	cfg.DisableQuicreuse = viper.GetBool("peering.disable_quicreuse")
	return cfg, nil
}
