package peering

import (
	"crypto/ed25519"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/multiformats/go-multiaddr"
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
		host             host.Host
		kademliaDHT      *dht.IpfsDHT // not nil if autopeering is enabled
		routingDiscovery *routing.RoutingDiscovery
		peers            map[peer.ID]*Peer // except self/host
		staticPeers      set.Set[peer.ID]
		blacklist        map[peer.ID]time.Time
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

	peersStats struct {
		peersAll         int
		peersStatic      int
		peersDead        int
		peersAlive       int
		peersPullTargets int
	}

	Peer struct {
		id                     peer.ID
		name                   string
		isStatic               bool // statically pre-configured (manual peering)
		respondsToPullRequests bool // from hb info
		whenAdded              time.Time
		lastHeartbeatReceived  time.Time
		lastLoggedConnected    bool // toggle
		// ring buffer with last clock differences
		clockDifferences      [10]time.Duration
		clockDifferencesIdx   int
		clockDifferenceMedian time.Duration
		// ranks
		rankByLastHBReceived  int
		rankByClockDifference int
	}

	outMsgData struct {
		msg      outMessageWrapper
		peerID   peer.ID
		protocol protocol.ID
	}

	// outMessageWrapper is needed for the outQueue. In order to avoid timing problems
	outMessageWrapper interface {
		Bytes() []byte
		SetNow() // specifically for heartbeat
		Counter() uint32
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
	// gracePeriodAfterAdded period of time peer is considered not dead after added even if messages are not coming
	gracePeriodAfterAdded = 15 * heartbeatRate
	logPeersEvery         = 5 * time.Second
)
