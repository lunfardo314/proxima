package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
)

const traceHeartbeat = false

type heartbeatInfo struct {
	clock      time.Time
	hasTxStore bool
}

func heartbeatInfoFromBytes(data []byte) (heartbeatInfo, error) {
	if len(data) != 8+1 {
		return heartbeatInfo{}, fmt.Errorf("heartbeatInfoFromBytes: wrong data len")
	}
	nano := int64(binary.BigEndian.Uint64(data[:8]))
	var hasTxStore bool
	if data[8] != 0 {
		if data[8] == 0xff {
			hasTxStore = true
		} else {
			return heartbeatInfo{}, fmt.Errorf("heartbeatInfoFromBytes: wrong data")
		}
	}
	ret := heartbeatInfo{
		clock:      time.Unix(0, nano),
		hasTxStore: hasTxStore,
	}
	return ret, nil
}

func (hi *heartbeatInfo) Bytes() []byte {
	var buf bytes.Buffer
	var timeNanoBin [8]byte

	binary.BigEndian.PutUint64(timeNanoBin[:], uint64(hi.clock.UnixNano()))
	buf.Write(timeNanoBin[:])
	var boolBin byte
	if hi.hasTxStore {
		boolBin = 0xff
	}
	buf.WriteByte(boolBin)
	return buf.Bytes()
}

func (ps *Peers) blockCommunicationsWithStaticPeer(p *Peer) {
	util.Assertf(p.isPreConfigured, "p.isPreConfigured")

	p.mutex.Lock()
	p.blockActivityUntil = time.Now().Add(commBlockDuration)
	p.mutex.Unlock()

	ps.Log().Warnf("blocked communications with peer %s (%s) for %v", ShortPeerIDString(p.id), p.name, commBlockDuration)
}

func (ps *Peers) MaxPeers() (preConfigured int, maxDynamicPeers int) {
	return len(ps.cfg.PreConfiguredPeers), ps.cfg.MaxDynamicPeers
}

func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic int) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		if p.isPreConfigured {
			if p.isAlive() {
				aliveStatic++
			}
		} else {
			if p.isAlive() {
				aliveDynamic++
			}
		}
	}
	return
}

func (ps *Peers) logInactivityIfNeeded(id peer.ID) {
	p := ps.getPeer(id)
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if !p._isAlive() && p.needsLogLostConnection {
		ps.Log().Infof("host %s (self) lost connection with peer %s (%s)", ShortPeerIDString(ps.host.ID()), ShortPeerIDString(id), ps.PeerName(id))
		p.needsLogLostConnection = false
	}
}

func checkRemoteClockTolerance(remoteTime time.Time) (time.Duration, bool, bool) {
	nowis := time.Now() // local clock
	var clockDiff time.Duration

	var behind bool
	if nowis.After(remoteTime) {
		clockDiff = nowis.Sub(remoteTime)
		behind = true
	} else {
		clockDiff = remoteTime.Sub(nowis)
		behind = false
	}
	return clockDiff, clockDiff < clockTolerance, behind
}

// heartbeat protocol is used to monitor
// - if peer is alive and
// - to ensure clocks are synced within tolerance interval
// - to ensure that ledger genesis is the same

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()

	if traceHeartbeat {
		ps.Tracef(TraceTag, "heartbeatStreamHandler invoked in %s from %s", ps.host.ID().String, id.String)
	}

	p := ps.getPeer(id)
	if p == nil {
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Tracef(TraceTag, "autopeering disabled: unknown peer %s", id.String)
			_ = stream.Reset()
			return
		}
		// peer not found. Add new incoming dynamic peer and then let the autopeering handle if too many
		remote := stream.Conn().RemoteMultiaddr()
		addrInfo := &peer.AddrInfo{
			ID:    id,
			Addrs: []multiaddr.Multiaddr{remote},
		}
		// for some reason peer.AddrInfoFromP2pAddr does not work -> compose AddrInfo from parts

		//remoteStr := "<nil>"
		//if remote != nil {
		//	remoteStr = remote.String()
		//}
		//addrInfo, err := peer.AddrInfoFromP2pAddr(remote)
		//if err != nil {
		//	ps.Log().Errorf("peering: AddrInfoFromP2pAddr: '%v'. Multiaddr remote: %s, peer id: %s", err, remoteStr, id.String())
		//	_ = stream.Reset()
		//	return
		//} else {
		//	ps.Log().Infof("peering: AddrInfoFromP2pAddr: OK. Remote multiaddr: %s", remoteStr)
		//}
		ps.Log().Infof("incoming peer request from %s. Add new dynamic peer", ShortPeerIDString(id))
		p = ps.addPeer(addrInfo, "", false)
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err == nil {
		hbInfo, err = heartbeatInfoFromBytes(msgData)
	}
	if err != nil {
		ps.Log().Errorf("error while reading message from peer %s: %v", ShortPeerIDString(id), err)
		ps.dropPeer(p, "read error")
		_ = stream.Reset()
		return
	}

	clockDiff, clockOk, behind := checkRemoteClockTolerance(hbInfo.clock)
	if !clockOk {
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.Log().Warnf("clock of the peer %s is %s of the local clock for %v > tolerance interval %v",
			ShortPeerIDString(id), b, clockDiff, clockTolerance)
		ps.dropPeer(p, "over clock tolerance")
		_ = stream.Reset()
		return
	}
	defer stream.Close()

	if traceHeartbeat {
		ps.Tracef(TraceTag, "peer %s is alive: %v, has txStore: %v",
			func() string { return ShortPeerIDString(id) },
			func() any { return p.isAlive() },
			hbInfo.hasTxStore,
		)
	}

	p.evidence(
		evidenceAndLogActivity(ps, "heartbeat"),
		evidenceTxStore(hbInfo.hasTxStore),
		evidenceClockDifference(clockDiff),
	)
	util.Assertf(p.isAlive(), "isAlive")
}

func (ps *Peers) dropPeer(p *Peer, reason ...string) {
	if p.isPreConfigured {
		ps.blockCommunicationsWithStaticPeer(p)
	} else {
		ps.removeDynamicPeer(p, reason...)
	}
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	if traceHeartbeat {
		ps.Tracef(TraceTag, "sendHeartbeatToPeer from %s to %s", ps.host.ID().String, id.String)
	}

	stream, err := ps.host.NewStream(ps.Ctx(), id, ps.lppProtocolHeartbeat)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()

	hbInfo := heartbeatInfo{
		clock:      time.Now(),
		hasTxStore: true,
	}
	_ = writeFrame(stream, hbInfo.Bytes())
}

const (
	heartbeatRate      = time.Second
	aliveNumHeartbeats = 2
	aliveDuration      = time.Duration(aliveNumHeartbeats) * heartbeatRate
	logNumPeersPeriod  = 10 * time.Second
)

func (ps *Peers) heartbeatLoop() {
	var logNumPeersDeadline time.Time

	for {
		nowis := time.Now()
		if nowis.After(logNumPeersDeadline) {
			aliveStatic, aliveDynamic := ps.NumAlive()
			ps.Log().Infof("peering: node is connected to %d (%d + %d) peer(s). Pre-configured static: %d, max dynamic: %d",
				aliveStatic+aliveDynamic, aliveStatic, aliveDynamic, len(ps.cfg.PreConfiguredPeers), ps.cfg.MaxDynamicPeers)

			logNumPeersDeadline = nowis.Add(logNumPeersPeriod)
		}
		for _, id := range ps.getPeerIDsWithOpenComms() {
			ps.logInactivityIfNeeded(id)
			ps.sendHeartbeatToPeer(id)
		}
		select {
		case <-ps.Environment.Ctx().Done():
			ps.Log().Infof("peering: heartbeet loop stopped")
			return
		case <-time.After(heartbeatRate):
		}
	}
}
