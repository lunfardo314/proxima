package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
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

func (p *Peer) isAlive() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p._isAlive()
}

func (p *Peer) _isAlive() bool {
	// peer is alive if its last activity is at least some heartbeats old
	return time.Now().Sub(p.lastActivity) < aliveDuration
}

func (p *Peer) HasTxStore() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p.hasTxStore
}

func (p *Peer) evidenceActivity(ps *Peers, srcMsg string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if !p._isAlive() {
		ps.Log().Infof("peering: connected to peer %s (%s) (%s)", ShortPeerIDString(p.id), p.name, srcMsg)
	}
	p.lastActivity = time.Now()
	p.needsLogLostConnection = true
}

func (p *Peer) evidenceTxStore(hasTxStore bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.hasTxStore = hasTxStore
}

func (p *Peer) isCommunicationOpen() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p.blockActivityUntil.Before(time.Now())
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

func checkRemoteClockTolerance(remoteTime time.Time) (bool, bool) {
	nowis := time.Now() // local clock
	var diff time.Duration

	var behind bool
	if nowis.After(remoteTime) {
		diff = nowis.Sub(remoteTime)
		behind = true
	} else {
		diff = remoteTime.Sub(nowis)
		behind = false
	}
	return diff < clockTolerance, behind
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
		// peer not found
		ps.Tracef(TraceTag, "unknown peer %s", id.String())
		_ = stream.Reset()
		return
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
		ps.Log().Errorf("error while reading message from peer %s: %v", id.String(), err)
		ps.dropPeer(p)
		_ = stream.Reset()
		return
	}

	if clockOk, behind := checkRemoteClockTolerance(hbInfo.clock); !clockOk {
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.Log().Warnf("clock of the peer %s is %s of the local clock more than tolerance interval %v", id.String(), b, clockTolerance)
		ps.dropPeer(p)
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

	p.evidenceActivity(ps, "heartbeat")
	p.evidenceTxStore(hbInfo.hasTxStore)

	util.Assertf(p.isAlive(), "isAlive")
}

func (ps *Peers) dropPeer(p *Peer) {
	if p.isPreConfigured {
		ps.blockCommunicationsWithStaticPeer(p)
	} else {
		ps.removeDynamicPeer(p)
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
			ps.Log().Infof("peering: node is connected to %d peer(s). Static: %d, dynamic: %d, pre-configured: %d, max dynamic: %d",
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
