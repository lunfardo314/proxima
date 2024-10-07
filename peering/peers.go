package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	p2putil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	p2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"
	"golang.org/x/exp/maps"
)

func NewPeersDummy() *Peers {
	ret := &Peers{
		peers:           make(map[peer.ID]*Peer),
		blacklist:       make(map[peer.ID]_deadlineWithReason),
		onReceiveTx:     func(_ peer.ID, _ []byte, _ *txmetadata.TransactionMetadata) {},
		onReceivePullTx: func(_ peer.ID, _ ledger.TransactionID) {},
	}
	//ret.registerMetrics()
	return ret
}

func New(env environment, cfg *Config) (*Peers, error) {
	hostIDPrivateKey, err := p2pcrypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("wrong private key: %w", err)
	}
	lppHost, err := libp2p.New(
		libp2p.Identity(hostIDPrivateKey),

		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.HostPort)),
		libp2p.Transport(p2pquic.NewTransport),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
		libp2p.AddrsFactory(FilterAddresses(cfg.AllowLocalIPs)),
	)
	if err != nil {
		return nil, fmt.Errorf("unable create libp2p host: %w", err)
	}

	ledgerLibraryHash := ledger.L().Library.LibraryHash()
	rendezvousNumber := binary.BigEndian.Uint64(ledgerLibraryHash[:8])

	ret := &Peers{
		environment:          env,
		cfg:                  cfg,
		host:                 lppHost,
		peers:                make(map[peer.ID]*Peer),
		staticPeers:          set.New[peer.ID](),
		blacklist:            make(map[peer.ID]_deadlineWithReason),
		onReceiveTx:          func(_ peer.ID, _ []byte, _ *txmetadata.TransactionMetadata) {},
		onReceivePullTx:      func(_ peer.ID, _ ledger.TransactionID) {},
		lppProtocolGossip:    protocol.ID(fmt.Sprintf(lppProtocolGossip, rendezvousNumber)),
		lppProtocolPull:      protocol.ID(fmt.Sprintf(lppProtocolPull, rendezvousNumber)),
		lppProtocolHeartbeat: protocol.ID(fmt.Sprintf(lppProtocolHeartbeat, rendezvousNumber)),
		rendezvousString:     fmt.Sprintf("%d", rendezvousNumber),
	}

	env.Log().Infof("[peering] rendezvous number is %d", rendezvousNumber)
	for name, maddr := range cfg.PreConfiguredPeers {
		if err = ret.addStaticPeer(maddr.Multiaddr, name, maddr.addrString); err != nil {
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
		env.Tracef(TraceTagAutopeering, "autopeering is enabled")

	} else {
		env.Log().Infof("[peering] autopeering is disabled")
	}
	env.Log().Infof("[peering] ignore all pull requests: %v", cfg.IgnoreAllPullRequests)
	env.Log().Infof("[peering] only pull requests from static peers are accepted: %v", cfg.AcceptPullRequestsFromStaticPeersOnly)

	ret.registerMetrics()

	env.Log().Infof("[peering] initialized successfully")
	return ret, nil
}

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
	return cfg, nil
}

func NewPeersFromConfig(env environment) (*Peers, error) {
	cfg, err := readPeeringConfig()
	if err != nil {
		return nil, err
	}

	return New(env, cfg)
}

func (ps *Peers) SelfID() peer.ID {
	return ps.host.ID()
}

func (ps *Peers) Host() host.Host {
	return ps.host
}

const TraceTagPullTargets = "peering_pull_targets"

func (ps *Peers) Run() {
	ps.environment.MarkWorkProcessStarted(Name)

	ps.host.SetStreamHandler(ps.lppProtocolGossip, ps.gossipStreamHandler)
	ps.host.SetStreamHandler(ps.lppProtocolPull, ps.pullStreamHandler)
	ps.host.SetStreamHandler(ps.lppProtocolHeartbeat, ps.heartbeatStreamHandler)

	//ps.startHeartbeat()
	var logNumPeersDeadline time.Time
	hbCounter := uint32(0)

	ps.RepeatInBackground("peering_heartbeat_loop", heartbeatRate, func() bool {
		nowis := time.Now()
		peerIDs := ps.peerIDs()

		for _, id := range peerIDs {
			ps.logConnectionStatusIfNeeded(id)

			idCopy := id
			hbCounterCopy := hbCounter
			go ps.sendHeartbeatToPeer(idCopy, hbCounterCopy)

			hbCounter++
		}

		if nowis.After(logNumPeersDeadline) {
			aliveStatic, aliveDynamic, pullTargets := ps.NumAlive()

			ps.Log().Infof("[peering] node is connected to %d peer(s). Static: %d/%d, dynamic %d/%d, pull targets: %d (%v)",
				aliveStatic+aliveDynamic, aliveStatic, len(ps.cfg.PreConfiguredPeers),
				aliveDynamic, ps.cfg.MaxDynamicPeers, pullTargets, time.Since(nowis))

			logNumPeersDeadline = nowis.Add(logPeersEvery)
			{
				ps.Tracef(TraceTagPullTargets, "pull targets: {%s}", func() string { return ps.pullTargetsByRankDescLines().Join(", ") })
			}
		}

		return true
	}, true)

	if ps.isAutopeeringEnabled() {
		ps.RepeatInBackground("autopeering_loop", checkPeersEvery, func() bool {
			ps.discoverPeersIfNeeded()
			ps.dropExcessPeersIfNeeded() // dropping excess dynamic peers one-by-one
			return true
		}, true)
	}

	ps.RepeatInBackground(Name+"_blacklist_cleanup", 2*time.Second, func() bool {
		ps.cleanBlacklist()
		return true
	})

	ps.RepeatInBackground(Name+"_update_peer_metrics", 2*time.Second, func() bool {
		ps.updatePeerMetrics(ps.peerStats())
		return true
	})

	ps.RepeatInBackground(Name+"_adjust_ranks", 500*time.Millisecond, func() bool {
		ps.adjustRanks()
		return true
	})

	ps.RepeatInBackground("peering_clock_tolerance_loop", 2*clockTolerance, func() bool {
		ps.logBigClockDiffs()
		return true
	}, true)

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
		_ = ps.host.Close()
		ps.Log().Infof("[peering] libp2p host %s (self) has been stopped", ShortPeerIDString(ps.host.ID()))
	})
}

// addStaticPeer adds preconfigured peer to the list. It will never be deleted
func (ps *Peers) addStaticPeer(maddr multiaddr.Multiaddr, name, addrString string) error {
	if slices.Index(ps.host.Addrs(), maddr) > 0 {
		ps.Log().Warnf("[peering] ignore static peer with the multiaddress of the host")
		return nil
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("can't get multiaddress info: %v", err)
	}
	ps.Log().Infof("[peering] added pre-configured peer %s as '%s'", addrString, name)
	ps.addPeer(info, name, true)
	ps.staticPeers.Insert(info.ID)
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

func (ps *Peers) _addPeer(addrInfo *peer.AddrInfo, name string, static bool) *Peer {
	p := &Peer{
		id:        addrInfo.ID,
		name:      name,
		isStatic:  static,
		whenAdded: time.Now(),
	}
	ps.peers[addrInfo.ID] = p
	for _, a := range addrInfo.Addrs {
		ps.host.Peerstore().AddAddr(addrInfo.ID, a, peerstore.PermanentAddrTTL)
	}
	return p
}

// dropPeer removes dynamic peer and blacklists for 1 min. Ignores otherwise
func (ps *Peers) dropPeer(id peer.ID, reason string) {
	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			ps._dropPeer(p, reason)
		}
	})
}

func (ps *Peers) _dropPeer(p *Peer, reason string) {
	if p.isStatic {
		ps._addToBlacklist(p.id, reason)
		return
	}

	why := ""
	if len(reason) > 0 {
		why = fmt.Sprintf(". Drop reason: '%s'", reason)
	}

	ps.host.Peerstore().RemovePeer(p.id)
	ps.kademliaDHT.RoutingTable().RemovePeer(p.id)
	_ = ps.host.Network().ClosePeer(p.id)
	delete(ps.peers, p.id)

	ps._addToBlacklist(p.id, reason)

	ps.Log().Infof("[peering] dropped dynamic peer %s - %s%s", ShortPeerIDString(p.id), p.name, why)
}

func (ps *Peers) _addToBlacklist(id peer.ID, reason string) {
	ps.blacklist[id] = _deadlineWithReason{
		Time:   time.Now().Add(blacklistTTL),
		reason: reason,
	}
}

func (ps *Peers) _isInBlacklist(id peer.ID) bool {
	_, yes := ps.blacklist[id]
	return yes
}

func (ps *Peers) OnReceiveTxBytes(fun func(from peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceiveTx = fun
}

func (ps *Peers) OnReceivePullTxRequest(fun func(from peer.ID, txid ledger.TransactionID)) {
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

func (ps *Peers) knownPeer(id peer.ID, ifExists func(p *Peer)) (known, blacklisted, static bool) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	_, blacklisted = ps.blacklist[id]
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

func (ps *Peers) cleanBlacklist() {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	toDelete := make([]peer.ID, 0, len(ps.blacklist))
	nowis := time.Now()
	for id, deadline := range ps.blacklist {
		if deadline.Before(nowis) {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		delete(ps.blacklist, id)
	}
}

func (p *Peer) _isDead() bool {
	return !p._isAlive() && time.Since(p.whenAdded) > gracePeriodAfterAdded
}

func (ps *Peers) IsAlive(id peer.ID) (isAlive bool) {
	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			isAlive = p._isAlive()
		}
	})
	return
}

func (p *Peer) _isAlive() bool {
	return time.Since(p.lastHeartbeatReceived) < aliveDuration
}

// for QUIC timeout 'NewStream' is necessary, otherwise it may hang if peer is unavailable

const defaultSendTimeout = time.Second /// = 500 * time.Millisecond

//const TraceTagSendMsg = "sendMsg"

func (ps *Peers) sendMsgBytesOut(peerID peer.ID, protocolID protocol.ID, data []byte, timeout ...time.Duration) bool {
	to := defaultSendTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}

	ctx, cancel := context.WithTimeoutCause(ps.Ctx(), to, context.DeadlineExceeded)
	defer cancel()

	// the NewStream waits until context is done
	stream, err := ps.host.NewStream(ctx, peerID, protocolID)
	if err != nil {
		return false
	}

	if ctx.Err() != nil {
		return false
	}
	util.Assertf(stream != nil, "stream != nil")
	defer func() { _ = stream.Close() }()

	if err = writeFrame(stream, data); err != nil {
		ps.Log().Errorf("[peering] error while sending message to peer %s", ShortPeerIDString(peerID))
	}
	ps.outMsgCounter.Inc()
	return err == nil
}

// sendMsgBytesOutMulti send to multiple peers in parallel
func (ps *Peers) sendMsgBytesOutMulti(peerIDs []peer.ID, protocolID protocol.ID, data []byte, timeout ...time.Duration) {
	for _, id := range peerIDs {
		idCopy := id
		go ps.sendMsgBytesOut(idCopy, protocolID, data, timeout...)
	}
}

func (ps *Peers) GetPeersInfo() *api.PeersInfo {
	ret := &api.PeersInfo{
		HostID:    ps.host.ID().String(),
		Blacklist: make(map[string]string),
		Peers:     make([]api.PeerInfo, 0),
	}

	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	var qClock, qHB [3]int64

	for _, p := range ps.peers {
		qClock[0] = p.clockDifferenceQuartiles[0].Nanoseconds()
		qClock[1] = p.clockDifferenceQuartiles[1].Nanoseconds()
		qClock[2] = p.clockDifferenceQuartiles[2].Nanoseconds()

		qHB[0] = p.hbMsgDifferenceQuartiles[0].Nanoseconds()
		qHB[1] = p.hbMsgDifferenceQuartiles[1].Nanoseconds()
		qHB[2] = p.hbMsgDifferenceQuartiles[2].Nanoseconds()

		pi := api.PeerInfo{
			ID:                        p.id.String(),
			IsStatic:                  p.isStatic,
			RespondsToPull:            p.respondsToPullRequests,
			IsAlive:                   p._isAlive(),
			WhenAdded:                 p.whenAdded.UnixNano(),
			LastHeartbeatReceived:     p.lastHeartbeatReceived.UnixNano(),
			ClockDifferencesQuartiles: qClock,
			HBMsgDifferencesQuartiles: qHB,
			NumIncomingHB:             p.numIncomingHB,
			NumIncomingPull:           p.numIncomingPull,
			NumIncomingTx:             p.numIncomingTx,
		}
		pi.MultiAddresses = make([]string, 0)
		for _, ma := range ps.host.Peerstore().Addrs(p.id) {
			pi.MultiAddresses = append(pi.MultiAddresses, ma.String())
		}
		ret.Peers = append(ret.Peers, pi)
	}

	for id, r := range ps.blacklist {
		ret.Blacklist[id.String()] = r.reason
	}
	return ret
}
