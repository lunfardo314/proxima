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
		connMap:         make(map[string]connEntry),
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
		libp2p.AddrsFactory(FilterAddresses(cfg.AllowLocalIPs, cfg.HostPort, cfg.HostExternalIPs)),
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
		lppProtocolGossip:       protocol.ID(fmt.Sprintf(lppProtocolGossip, rendezvousNumber)),
		lppProtocolPull:         protocol.ID(fmt.Sprintf(lppProtocolPull, rendezvousNumber)),
		lppProtocolConnectivity: protocol.ID(fmt.Sprintf(lppProtocolConnectivity, rendezvousNumber)),
		rendezvousString:        fmt.Sprintf("%d", rendezvousNumber),
		connMap:                 make(map[string]connEntry),
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
	env.Log().Infof("[peering] connectivity-mapping protocol enabled: %v", !cfg.ConnectivityDisabled)

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

	// lppConnectivity network-mapping overlay. When disabled the handler is not
	// registered and the emit loop does not run, so the protocol is simply absent
	// (incoming streams get "protocol not supported"). See connectivity.go.
	if !ps.cfg.ConnectivityDisabled {
		ps.host.SetStreamHandler(ps.lppProtocolConnectivity, ps.connectivityStreamHandler)
		ps.RepeatInBackground("connectivity_emit_loop", connectivityEmitInterval, func() bool {
			ps.emitConnectivity()
			return true
		})
		ps.RepeatInBackground("connectivity_evict_loop", connectivityEntryTTL, func() bool {
			ps.evictStaleConnEntries()
			return true
		})
	}

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
		ps.lppProtocolPull:         ps.newPeerStream(peerID, ps.lppProtocolPull),
		ps.lppProtocolGossip:       ps.newPeerStream(peerID, ps.lppProtocolGossip),
		ps.lppProtocolConnectivity: ps.newPeerStream(peerID, ps.lppProtocolConnectivity),
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
			ps.lppProtocolPull:         ps.newPeerStream(addrInfo.ID, ps.lppProtocolPull),
			ps.lppProtocolGossip:       ps.newPeerStream(addrInfo.ID, ps.lppProtocolGossip),
			ps.lppProtocolConnectivity: ps.newPeerStream(addrInfo.ID, ps.lppProtocolConnectivity),
		}
		ps.peers[addrInfo.ID] = p
		go ps.scheduleStaticReconnect(addrInfo.ID)
		return p
	}

	// Dynamic peer: try to dial; on success register in peers, on failure forget.
	// libp2p's Connect tracks in-flight dials internally; autopeering may
	// rediscover and retry on a future tick if the peer is reachable later.
	// The "added dynamic peer" log is emitted HERE, on dial success — not at
	// discovery — so an unreachable candidate (filtered/down p2p port) that never
	// enters ps.peers is silently retried instead of re-logged on every tick.
	go func() {
		time.Sleep(100 * time.Millisecond)
		err := ps.dialPeer(addrInfo.ID, p)
		if err != nil {
			ps.host.Peerstore().RemovePeer(addrInfo.ID)
			return
		}

		ps.mutex.Lock()
		ps.peers[addrInfo.ID] = p
		ps.mutex.Unlock()
		ps.Log().Infof("[peering] added dynamic peer %s", addrInfo.ID.String())
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
		// signal only — the stream's writer goroutine does the teardown, off this lock
		s.close()
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

	// sendQueueCapacity bounds the outgoing backlog per peer per protocol.
	//
	// Sending is inherently slower than producing: every transaction is relayed to every peer, so
	// gossip upload is roughly (number of peers) times ingest, and libp2p's Write blocks on the
	// stream's flow-control window whenever the link cannot absorb that. Without a bound the excess
	// piles up — as goroutines, and once they were bounded, as goroutines waiting on the stream's
	// write lock. Neither is a queue anyone chose the size of.
	//
	// So the backlog is explicit and shallow. Shallow because a message that has waited longer than
	// a slot is not worth sending: the receiver has long since pulled the transaction by other
	// means. 64 is a burst absorber, not a buffer — on a healthy link it never fills, and on a
	// saturated one it shifts the loss from "whatever the runtime happened to keep" to a counted,
	// deliberate drop.
	sendQueueCapacity = 64
)

// newPeerStream creates an outgoing stream slot and starts its single writer goroutine.
func (ps *Peers) newPeerStream(peerID peer.ID, protocolID protocol.ID) *peerStream {
	s := &peerStream{
		out:  make(chan []byte, sendQueueCapacity),
		done: make(chan struct{}),
	}
	go ps.runStreamWriter(peerID, protocolID, s)
	return s
}

// close stops the stream's writer goroutine. Idempotent.
func (s *peerStream) close() {
	s.closeOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
	})
}

// runStreamWriter is the only goroutine that writes to this stream. Draining the backlog one
// message at a time is what serializes writes, so no lock is needed for that any more, and a slow
// peer costs one parked goroutine instead of one per queued message.
//
// It also owns tearing the stream down on the way out. That has to happen here rather than on the
// drop path: dropping a peer runs under the global peers lock, and the stream lock can be held by a
// write for as long as the send timeout, which would stall every other peer's sends behind it.
func (ps *Peers) runStreamWriter(peerID peer.ID, protocolID protocol.ID, s *peerStream) {
	defer ps.clearPeerStream(s)

	ctxStop := ps.stoppedCtx
	if ctxStop == nil {
		ctxStop = ps.Ctx()
	}
	for {
		select {
		case <-ctxStop.Done():
			return
		case <-s.done:
			return
		case data := <-s.out:
			// the peer may have been dropped while this message sat in the backlog; spending the
			// write timeout on a peer that is gone only delays the writer's exit
			select {
			case <-s.done:
				return
			default:
			}
			ps.writeMsgBytes(peerID, protocolID, s, data)
		}
	}
}

// sendMsgBytesOut queues one message for the peer. It never blocks: when the backlog is full the
// message is dropped and counted, which is the load shedding this path is expected to do — gossip
// and pull requests are both re-derivable (the transaction is pulled again, the pull is retried).
// Returns whether the message was accepted; an accepted message is written unless the connection
// fails under it.
func (ps *Peers) sendMsgBytesOut(peerID peer.ID, protocolID protocol.ID, data []byte) bool {
	var ps_ *peerStream
	ps.withPeer(peerID, func(p *Peer) {
		if p != nil {
			ps_ = p.streams[protocolID]
		}
	})
	if ps_ == nil || ps_.out == nil {
		return false
	}
	select {
	case ps_.out <- data:
		return true
	default:
		ps.outMsgDroppedCounter.Inc()
		return false
	}
}

// writeMsgBytes performs the actual write. Called only from the stream's writer goroutine.
func (ps *Peers) writeMsgBytes(peerID peer.ID, protocolID protocol.ID, s *peerStream, data []byte) bool {
	// Transient stream failures (QUIC idle timeout, peer restart, one-sided reset) are
	// expected; redial once and retry before returning failure. This avoids the drop-peer
	// cycle triggered whenever a stream reset occurs on an otherwise-healthy peer.
	for attempt := 0; attempt < sendMsgMaxTries; attempt++ {
		if err := ps.ensurePeerStream(peerID, protocolID, s); err != nil {
			return false
		}
		if ps.writeFrameToPeerStream(s, data) {
			ps.outMsgCounter.Inc()
			return true
		}
		// write failed — clear the cached stream so the next iteration redials
		ps.clearPeerStream(s)
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
	// Capture the stream under the lock: clearPeerStream may nil s.stream concurrently, and a
	// concurrently-closed stream yields a write error rather than a nil deref.
	stream := s.stream
	if stream == nil {
		return false
	}
	// The write deadline is what actually bounds the call. libp2p's Write blocks on the stream's
	// flow-control window, so a peer that stops reading parks the writer for as long as it likes.
	// A timeout that only abandons a helper goroutine does not stop such a write: the goroutine stays
	// parked, and under gossip load (one send per alive peer per transaction) they accumulate without
	// bound. A failure to arm the deadline means the stream is already unusable.
	if err := stream.SetWriteDeadline(time.Now().Add(sendMsgTimeout)); err != nil {
		return false
	}
	if err := writeFrame(stream, data); err != nil {
		// Reset, not Close: the deadline can cut a frame in half, leaving the peer's reader waiting for
		// a body that will never come, and Close only shuts down the write side. The caller clears the
		// cached stream and redials.
		_ = stream.Reset()
		return false
	}
	return true
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

// sendMsgBytesOutMulti queues the message for every target. Each peer has its own backlog and its
// own writer, so the peers are still independent — a stalled one no longer holds up the others, and
// no longer costs a goroutine per message it cannot take.
func (ps *Peers) sendMsgBytesOutMulti(peerIDs []peer.ID, protocolID protocol.ID, data []byte) {
	for _, id := range peerIDs {
		ps.sendMsgBytesOut(id, protocolID, data)
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
