package peering

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	p2putil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	p2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	reuse "github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/exp/maps"
)

func NewPeersDummy() *Peers {
	ret := &Peers{
		peers:           make(map[peer.ID]*Peer),
		reconnecting:    set.New[peer.ID](),
		onReceiveTx:     func(_ peer.ID, _ []byte, _ base.TransactionID) {},
		onReceivePullTx: func(_ peer.ID, _ base.TransactionID) {},
	}
	//ret.registerMetrics()
	return ret
}

func New(env environment, cfg *Config) (*Peers, error) {
	hostIDPrivateKey, err := p2pcrypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("wrong private key: %w", err)
	}

	// Watermarks count TOTAL libp2p connections (statics + dynamics). Static
	// peers are Protect-ed (see addStaticPeer) so trims target only dynamics;
	// shifting the watermarks by numStatic ensures the cap on dynamics is
	// exactly cfg.MaxDynamicPeers after a trim down to lo.
	numStatic := len(cfg.PreConfiguredPeers)
	connManager, err := connmgr.NewConnManager(
		numStatic+cfg.MaxDynamicPeers,   // lo,
		numStatic+cfg.MaxDynamicPeers+5, // hi,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create ConnManager: %w", err)
	}

	options := []libp2p.Option{
		libp2p.Identity(hostIDPrivateKey),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.HostPort)),
		libp2p.Transport(p2pquic.NewTransport),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
		libp2p.AddrsFactory(FilterAddresses(cfg.AllowLocalIPs)),
		libp2p.ConnectionManager(connManager),
	}

	if !cfg.DisableQuicreuse {
		options = append(options, libp2p.QUICReuse(reuse.NewConnManager))
	}

	lppHost, err := libp2p.New(options...)

	if err != nil {
		return nil, fmt.Errorf("unable create libp2p host: %w", err)
	}

	// Fixed rendezvous: always use genesis (slot 0) library hash.
	// Network isolation for upgrades is handled by TxVersion validation
	// in transaction parsing, not by peering separation.
	ledgerLibraryHash := ledger.L(0).LibraryHash()
	rendezvousNumber := binary.BigEndian.Uint64(ledgerLibraryHash[:8])

	stoppedCtx, stop := context.WithCancel(env.Ctx())
	ret := &Peers{
		environment:       env,
		cfg:               cfg,
		host:              lppHost,
		stoppedCtx:        stoppedCtx,
		stop:              stop,
		peers:             make(map[peer.ID]*Peer),
		staticPeers:       make(map[peer.ID]multiaddr.Multiaddr),
		reconnecting:      set.New[peer.ID](),
		onReceiveTx:       func(_ peer.ID, _ []byte, _ base.TransactionID) {},
		onReceivePullTx:   func(_ peer.ID, _ base.TransactionID) {},
		lppProtocolGossip: protocol.ID(fmt.Sprintf(lppProtocolGossip, rendezvousNumber)),
		lppProtocolPull:   protocol.ID(fmt.Sprintf(lppProtocolPull, rendezvousNumber)),
		rendezvousString:  fmt.Sprintf("%d", rendezvousNumber),
	}

	// register the Notifiee for connection-level events. CONNECTED/LOST CONNECTION
	// log lines and static-peer reconnect scheduling are driven from here.
	ret.host.Network().Notify(&peeringNotifiee{ps: ret})

	// register the libp2p ping protocol on the host (so we both respond to
	// peers' pings and can measure outgoing RTT to them).
	ret.pingService = ping.NewPingService(ret.host)

	env.Log().Infof("[peering] rendezvous number is %d", rendezvousNumber)
	for name, maddr := range cfg.PreConfiguredPeers {
		if err = ret.addStaticPeer(maddr.Multiaddr, name); err != nil {
			return nil, err
		}
	}
	env.Log().Infof("[peering] number of statically pre-configured peers (manual peering): %d", len(cfg.PreConfiguredPeers))

	if ret.isAutopeeringEnabled() {
		// autopeering enabled. The node also acts as a bootstrap node
		bootstrapPeers := peerstore.AddrInfos(ret.host.Peerstore(), maps.Keys(ret.peers))
		ret.kademliaDHT, err = dht.New(env.Ctx(), lppHost,
			dht.Mode(dht.ModeAutoServer),
			dht.RoutingTableRefreshPeriod(5*time.Second),
			dht.BootstrapPeers(bootstrapPeers...),
		)
		if err != nil {
			return nil, err
		}

		if err = ret.kademliaDHT.Bootstrap(env.Ctx()); err != nil {
			return nil, err
		}
		ret.routingDiscovery = routing.NewRoutingDiscovery(ret.kademliaDHT)
		p2putil.Advertise(env.Ctx(), ret.routingDiscovery, ret.rendezvousString)

		env.Log().Infof("[peering] autopeering is enabled with max dynamic peers = %d", cfg.MaxDynamicPeers)

	} else {
		env.Log().Infof("[peering] autopeering is disabled")
	}
	env.Log().Infof("[peering] ignore all pull requests: %v", cfg.IgnoreAllPullRequests)
	env.Log().Infof("[peering] only pull requests from static peers are accepted: %v", cfg.AcceptPullRequestsFromStaticPeersOnly)

	ret.registerMetrics()

	// log once per state transition (connected <-> disconnected). "Disconnected
	// from the network" means no incoming gossip/pull traffic for at least one
	// slot — at steady state every node should see ≥1 inbound tx per slot, so
	// silence longer than that means the node is effectively isolated. Pure
	// inbound-traffic liveness signal, independent of per-peer connection state.
	disconnLogThreshold := ledger.SlotDuration()
	disconnected := false
	ret.RepeatInBackground("disconn_log_loop", disconnLogThreshold, func() bool {
		d := ret.DurationSinceLastMessageFromPeer()
		switch {
		case d > disconnLogThreshold && !disconnected:
			ret.Log().Warnf("[peering] node is DISCONNECTED from the network (no incoming message for %v)", d)
			disconnected = true
		case d <= disconnLogThreshold && disconnected:
			ret.Log().Infof("[peering] node RECONNECTED to the network")
			disconnected = false
		}
		return true
	})

	env.Log().Infof("[peering] initialized successfully")

	return ret, nil
}

func NewPeersFromConfig(env environment) (*Peers, error) {
	cfg, err := readPeeringConfig()
	if err != nil {
		return nil, err
	}

	return New(env, cfg)
}

func (ps *Peers) SelfPeerID() peer.ID {
	return ps.host.ID()
}

func (ps *Peers) Host() host.Host {
	return ps.host
}

func (ps *Peers) Run() {
	ps.environment.MarkWorkProcessStarted(Name)

	ps.host.SetStreamHandler(ps.lppProtocolGossip, ps.gossipStreamHandler)
	ps.host.SetStreamHandler(ps.lppProtocolPull, ps.pullStreamHandler)

	ps.RepeatInBackground("peering_log_peers_loop", logPeersEvery, func() bool {
		aliveStatic, aliveDynamic := ps.NumAlive()

		ps.Log().Infof("[peering] node is connected to %d peer(s). Static: %d/%d, dynamic %d/%d",
			aliveStatic+aliveDynamic, aliveStatic, len(ps.cfg.PreConfiguredPeers),
			aliveDynamic, ps.cfg.MaxDynamicPeers)
		return true
	}, true)

	if ps.isAutopeeringEnabled() {
		ps.RepeatInBackground("autopeering_loop", checkPeersEvery, func() bool {
			ps.discoverPeersIfNeeded()
			return true
		}, true)
	}

	ps.RepeatInBackground(Name+"_update_peer_metrics", 2*time.Second, func() bool {
		ps.updatePeerMetrics(ps.peerStats())
		return true
	})

	ps.RepeatInBackground("peer_rtt_loop", peerRTTInterval, func() bool {
		ps.measurePeerRTTs()
		return true
	})

	ps.Log().Infof("[peering] libp2p host %s (self) started on %v with %d pre-configured peers, maximum dynamic peers: %d, autopeering enabled: %v",
		ShortPeerIDString(ps.host.ID()), ps.host.Addrs(), len(ps.cfg.PreConfiguredPeers), ps.cfg.MaxDynamicPeers, ps.isAutopeeringEnabled())
	_ = ps.Log().Sync()
}

func (ps *Peers) isAutopeeringEnabled() bool {
	return ps.cfg.MaxDynamicPeers > 0
}

func (ps *Peers) Stop() {
	ps.stopOnce.Do(func() {
		ps.environment.MarkWorkProcessStopped(Name)

		ps.Log().Infof("[peering] stopping libp2p host %s (self)..", ShortPeerIDString(ps.host.ID()))
		_ = ps.Log().Sync()
		// cancel the per-Peers context first so background goroutines (e.g.
		// scheduleStaticReconnect) bail out before we tear down libp2p.
		if ps.stop != nil {
			ps.stop()
		}
		if ps.kademliaDHT != nil {
			_ = ps.kademliaDHT.Close()
		}
		_ = ps.host.Close()
		ps.Log().Infof("[peering] libp2p host %s (self) has been stopped", ShortPeerIDString(ps.host.ID()))
	})
}

// _findMultiaddr has been introduced because libp2p multiaddr.Multiaddr is no longer comparable ans slices.Index cannot be used
func _findMultiaddr(lst []multiaddr.Multiaddr, maddr multiaddr.Multiaddr) int {
	for i := range lst {
		if maddr.Equal(lst[i]) {
			return i
		}
	}
	return -1
}

// addStaticPeer adds preconfigured peer to the list. It will never be deleted
func (ps *Peers) addStaticPeer(maddr multiaddr.Multiaddr, name string) error {
	if _findMultiaddr(ps.host.Addrs(), maddr) > 0 {
		ps.Log().Warnf("[peering] ignore static peer with the multiaddress of the host")
		return nil
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("can't get multiaddress info: %v", err)
	}
	ps.Log().Infof("[peering] added pre-configured peer %s as '%s'", maddr.String(), name)
	ps.addPeer(info, name, true)
	// tell ConnManager not to trim this connection when it hits the high watermark.
	ps.host.ConnManager().Protect(info.ID, "static")
	if _, found := ps.staticPeers[info.ID]; !found {
		ps.staticPeers[info.ID] = maddr
	}
	return nil
}

func (ps *Peers) addPeer(addrInfo *peer.AddrInfo, name string, static bool) (success bool) {
	if addrInfo.ID == ps.host.ID() {
		return false
	}
	ps.withPeer(addrInfo.ID, func(p *Peer) {
		if p == nil {
			ps._addPeer(addrInfo, name, static)
			success = true
		}
	})
	return
}

func (ps *Peers) NewStream(peerID peer.ID, pID protocol.ID, timeout time.Duration) (network.Stream, error) {
	ctx, cancel := context.WithTimeout(ps.Ctx(), timeout)
	defer cancel()
	stream, err := ps.host.NewStream(ctx, peerID, pID)
	if err == nil {
		// force the start of the streamHandler on the peer to avoid the stream reset error
		err = writeFrame(stream, []byte("Start"))
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
	}

	return stream, err
}

// dialPeer establishes the libp2p connection to the peer and initialises the
// peerStream map with an empty entry per application protocol. The actual
// protocol streams are opened lazily on first send via ensurePeerStream — the
// same redial path that handles transient stream resets. This avoids paying
// 3x multistream-select negotiation up front (one RTT per stream per new
// peer) when only one protocol is likely to be used first, and unifies
// "initial open" with "reopen after reset" in a single code path.
//
// peerstore addresses are registered by _addPeer before this goroutine runs,
// so passing AddrInfo with ID only is sufficient — libp2p resolves the addrs
// from the peerstore.
func (ps *Peers) dialPeer(peerID peer.ID, p *Peer) error {
	timeout := 15 * time.Second
	ctx, cancel := context.WithTimeout(ps.Ctx(), timeout)
	defer cancel()

	if err := ps.host.Connect(ctx, peer.AddrInfo{ID: peerID}); err != nil {
		return err
	}
	p.streams = map[protocol.ID]*peerStream{
		ps.lppProtocolPull:   {},
		ps.lppProtocolGossip: {},
	}
	return nil
}

func (ps *Peers) _addPeer(addrInfo *peer.AddrInfo, name string, static bool) *Peer {
	p := &Peer{
		id:        addrInfo.ID,
		name:      name,
		isStatic:  static,
		whenAdded: time.Now(),
	}

	for _, a := range addrInfo.Addrs {
		ps.host.Peerstore().AddAddr(addrInfo.ID, a, peerstore.PermanentAddrTTL)
	}

	if static {
		// Track static peers immediately so scheduleStaticReconnect (which checks
		// ps.peers[id]) can drive the dial loop with backoff. Initialize the
		// per-protocol stream map up front — sendMsgBytesOut requires
		// p.streams[protocolID] to exist (the cached stream itself is opened
		// lazily by ensurePeerStream on first send). scheduleStaticReconnect's
		// host.Connect establishes the underlying libp2p connection; the first
		// gossip / pull then opens the actual stream over it.
		p.streams = map[protocol.ID]*peerStream{
			ps.lppProtocolPull:   {},
			ps.lppProtocolGossip: {},
		}
		ps.peers[addrInfo.ID] = p
		go ps.scheduleStaticReconnect(addrInfo.ID)
		return p
	}

	// Dynamic peer: try to dial; on success register in peers, on failure forget.
	// libp2p's Connect tracks in-flight dials internally; autopeering may
	// rediscover and retry on a future tick if the peer is reachable later.
	go func() {
		time.Sleep(100 * time.Millisecond)
		err := ps.dialPeer(addrInfo.ID, p)
		if err != nil {
			ps.host.Peerstore().RemovePeer(addrInfo.ID)
			return
		}

		ps.mutex.Lock()
		defer ps.mutex.Unlock()
		ps.peers[addrInfo.ID] = p
	}()

	return p
}

// dropPeer terminates a peer's connection and removes it from local tracking.
// For static peers, dropping is a no-op other than logging — static peers are
// trusted by configuration; reconnection is handled by scheduleStaticReconnect
// when libp2p reports the connection lost. Closing a static peer's connection
// here would just trigger an immediate reconnect, masking the underlying
// problem (config / version mismatch / malformed gossip from a misconfigured
// trusted node) which the operator should see in the logs and address.
func (ps *Peers) dropPeer(id peer.ID, reason string) {
	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			ps._dropPeer(p, reason)
		}
	})
}

func (ps *Peers) _dropPeer(p *Peer, reason string) {
	why := ""
	if len(reason) > 0 {
		why = fmt.Sprintf(". Drop reason: '%s'", reason)
	}

	if p.isStatic {
		ps.Log().Warnf("[peering] static peer %s ('%s') triggered drop%s — keeping it (static peers are not dropped)",
			ShortPeerIDString(p.id), p.name, why)
		return
	}

	for _, s := range p.streams {
		if s.stream != nil {
			_ = s.stream.Close()
		}
	}
	ps.host.Peerstore().RemovePeer(p.id)
	if ps.kademliaDHT != nil {
		ps.kademliaDHT.RoutingTable().RemovePeer(p.id)
	}
	_ = ps.host.Network().ClosePeer(p.id)
	delete(ps.peers, p.id)

	ps.Log().Infof("[peering] dropped dynamic peer %s - %s%s", ShortPeerIDString(p.id), p.name, why)
}

func (ps *Peers) OnReceiveTxBytes(fun func(from peer.ID, txBytes []byte, txIDPrefix base.TransactionID)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceiveTx = fun
}

func (ps *Peers) OnReceivePullTxRequest(fun func(from peer.ID, txid base.TransactionID)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceivePullTx = fun
}

func (ps *Peers) _getPeer(id peer.ID) *Peer {
	if ret, ok := ps.peers[id]; ok {
		return ret
	}
	return nil
}

func (ps *Peers) getPeer(id peer.ID) *Peer {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return ps._getPeer(id)
}

func (ps *Peers) knownPeer(id peer.ID, ifExists func(p *Peer)) (known, static bool) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	var p *Peer
	if p, known = ps.peers[id]; known {
		static = p.isStatic
		if ifExists != nil {
			ifExists(p)
		}
	}
	return
}

func (ps *Peers) withPeer(id peer.ID, fun func(p *Peer)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	fun(ps._getPeer(id))
}

func (ps *Peers) forEachPeerRLock(fun func(p *Peer) bool) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		if !fun(p) {
			return
		}
	}
}

func (ps *Peers) getPeerIDs() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return maps.Keys(ps.peers)
}

func (ps *Peers) PeerName(id peer.ID) string {
	p := ps.getPeer(id)
	if p == nil {
		return "(unknown peer)"
	}
	return p.name
}

// _isAlive reports whether libp2p currently considers the peer connected.
// Single source of truth — no local mirror, no timing thresholds.
func (ps *Peers) _isAlive(p *Peer) bool {
	return ps.host.Network().Connectedness(p.id) == network.Connected
}

// _isDead is the negation of _isAlive for dynamic peers; static peers are never
// considered dead (we keep retrying via scheduleStaticReconnect).
func (ps *Peers) _isDead(p *Peer) bool {
	return !p.isStatic && !ps._isAlive(p)
}

func (ps *Peers) IsAlive(id peer.ID) (isAlive bool) {
	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			isAlive = ps._isAlive(p)
		}
	})
	return
}

const (
	sendMsgTimeout  = 4 * time.Second
	redialTimeout   = 2 * time.Second
	sendMsgMaxTries = 2 // one initial + one retry after redial
)

func (ps *Peers) sendMsgBytesOut(peerID peer.ID, protocolID protocol.ID, data []byte) bool {
	var ps_ *peerStream
	ps.withPeer(peerID, func(p *Peer) {
		if p != nil {
			ps_ = p.streams[protocolID]
		}
	})
	if ps_ == nil {
		return false
	}

	// Transient stream failures (QUIC idle timeout, peer restart, one-sided reset) are
	// expected; redial once and retry before returning failure. This avoids the drop-peer
	// cycle triggered whenever a stream reset occurs on an otherwise-healthy peer.
	for attempt := 0; attempt < sendMsgMaxTries; attempt++ {
		if err := ps.ensurePeerStream(peerID, protocolID, ps_); err != nil {
			return false
		}
		if ps.writeFrameToPeerStream(ps_, data) {
			ps.outMsgCounter.Inc()
			return true
		}
		// write failed — clear the cached stream so the next iteration redials
		ps.clearPeerStream(ps_)
	}
	return false
}

// ensurePeerStream opens a new stream only when the cached one is absent.
func (ps *Peers) ensurePeerStream(peerID peer.ID, protocolID protocol.ID, s *peerStream) error {
	s.mutex.RLock()
	present := s.stream != nil
	s.mutex.RUnlock()
	if present {
		return nil
	}
	newStream, err := ps.NewStream(peerID, protocolID, redialTimeout)
	if err != nil {
		return err
	}
	s.mutex.Lock()
	old := s.stream
	s.stream = newStream
	s.mutex.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// writeFrameToPeerStream writes one framed message to the given peerStream under its write lock,
// with a wall-clock timeout. Serializing per-stream writes avoids interleaving when multiple
// goroutines target the same peer+protocol.
func (ps *Peers) writeFrameToPeerStream(s *peerStream, data []byte) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.stream == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendMsgTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- writeFrame(s.stream, data)
	}()
	select {
	case <-ctx.Done():
		return false
	case err := <-done:
		return err == nil
	}
}

// clearPeerStream nils the cached stream and closes the old one. Called on write failure
// so that the next send attempt will open a fresh stream.
func (ps *Peers) clearPeerStream(s *peerStream) {
	s.mutex.Lock()
	old := s.stream
	s.stream = nil
	s.mutex.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// sendMsgBytesOutMulti send to multiple peers in parallel
func (ps *Peers) sendMsgBytesOutMulti(peerIDs []peer.ID, protocolID protocol.ID, data []byte) {
	for _, id := range peerIDs {
		idCopy := id
		go ps.sendMsgBytesOut(idCopy, protocolID, data)
	}
}

func (ps *Peers) GetPeersInfo() *api.PeersInfo {
	ret := &api.PeersInfo{
		HostID: ps.host.ID().String(),
		Peers:  make([]api.PeerInfo, 0),
	}

	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		pi := api.PeerInfo{
			ID:              p.id.String(),
			IsStatic:        p.isStatic,
			IsAlive:         ps._isAlive(p),
			WhenAdded:       p.whenAdded.UnixNano(),
			NumIncomingPull: p.numIncomingPull,
			NumIncomingTx:   p.numIncomingTx,
		}
		if rtt := p.lastRTTNs.Load(); rtt > 0 {
			pi.RTTMs = float64(rtt) / float64(time.Millisecond)
		}
		pi.MultiAddresses = make([]string, 0)
		for _, ma := range ps.host.Peerstore().Addrs(p.id) {
			pi.MultiAddresses = append(pi.MultiAddresses, ma.String())
		}
		ret.Peers = append(ret.Peers, pi)
	}

	return ret
}

// NumAlive returns counts of alive static / dynamic peers and pull targets.
// "Alive" is libp2p-Connectedness-driven (see _isAlive).
func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic int) {
	ps.forEachPeerRLock(func(p *Peer) bool {
		if ps._isAlive(p) {
			if p.isStatic {
				aliveStatic++
			} else {
				aliveDynamic++
			}
		}
		return true
	})
	return
}

// peerIDsAlive returns IDs of peers libp2p reports as connected. Used by gossip
// to pick recipients (a peer that just disconnected won't be in the list).
func (ps *Peers) peerIDsAlive(except ...peer.ID) []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if len(except) > 0 && p.id == except[0] {
			return true
		}
		if ps._isAlive(p) {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
}

// evidenceMessage stamps the per-Peers "last incoming message" timestamp.
// Called by gossip and pull receive paths; drives the disconn_log_loop's
// "node is DISCONNECTED from network" warning.
func (ps *Peers) evidenceMessage() {
	ps.lastMsgReceived.Store(time.Now().UnixNano())
}

const (
	peerRTTInterval = 5 * time.Second
	peerRTTTimeout  = 4 * time.Second // bounded so a slow/dead peer doesn't stall the cycle
)

// measurePeerRTTs takes one ping RTT sample from every alive peer in parallel
// and stores it on the Peer struct (atomic, lock-free reads). Failures leave
// the previous sample untouched.
func (ps *Peers) measurePeerRTTs() {
	alive := ps.peerIDsAlive()
	if len(alive) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, id := range alive {
		wg.Add(1)
		go func(pid peer.ID) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ps.stoppedCtx, peerRTTTimeout)
			defer cancel()
			select {
			case res, ok := <-ps.pingService.Ping(ctx, pid):
				if !ok || res.Error != nil {
					return
				}
				ps.mutex.RLock()
				p, found := ps.peers[pid]
				ps.mutex.RUnlock()
				if found {
					p.lastRTTNs.Store(int64(res.RTT))
				}
			case <-ctx.Done():
			}
		}(id)
	}
	wg.Wait()
}

func (ps *Peers) DurationSinceLastMessageFromPeer() time.Duration {
	if ps.lastMsgReceived.Load() == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ps.lastMsgReceived.Load()))
}

// IsConnectedToNetwork returns true if libp2p reports at least one peer
// currently connected. After the heartbeat protocol was removed, this is the
// authoritative "node has a working connection to the network" signal — the
// previous traffic-timestamp proxy is too quiet on low-load networks (e.g. a
// pair of just-restarted nodes with no transactions to gossip).
func (ps *Peers) IsConnectedToNetwork() bool {
	connected := false
	ps.forEachPeerRLock(func(p *Peer) bool {
		if ps._isAlive(p) {
			connected = true
			return false
		}
		return true
	})
	return connected
}
