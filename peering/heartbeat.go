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
	clock        time.Time
	ledgerIDHash [32]byte // TODO include ledger identity into the hb message. To reject peers with wrong ledger identity
	hasTxStore   bool
}

func heartbeatInfoFromBytes(data []byte) (heartbeatInfo, error) {
	if len(data) != 8+1+32 {
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
	copy(ret.ledgerIDHash[:], data[9:9+32])
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
	buf.Write(hi.ledgerIDHash[:])
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
		ps.Log().Infof("libp2p host %s (self) connected to peer %s (%s) (%s)",
			ShortPeerIDString(ps.host.ID()), ShortPeerIDString(p.id), p.name, srcMsg)
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

	return p.postponeActivityUntil.Before(time.Now())
}

func (p *Peer) blockComms() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.postponeActivityUntil = time.Now().Add(commBlockDuration)
}

func (ps *Peers) blockCommsWithPeer(p *Peer) {
	p.blockComms()
	ps.Log().Warnf("blocked communications with peer %s (%s) for %v", ShortPeerIDString(p.id), p.name, commBlockDuration)
}

func (ps *Peers) NumPeers() (alive, configured int) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		if p.isAlive() {
			alive++
		}
	}
	configured = len(ps.peers)
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
		ps.Log().Warnf("unknown peer %s", id.String())
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
		ps.blockCommsWithPeer(p)
		_ = stream.Reset()
		return
	}

	if hbInfo.ledgerIDHash != ps.ledgerIDHash {
		ps.Log().Warnf("incompatible ledger ID hash from %s (%s)", id.String(), p.name)
		ps.blockCommsWithPeer(p)
		_ = stream.Reset()
		return

	}
	if clockOk, behind := checkRemoteClockTolerance(hbInfo.clock); !clockOk {
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.Log().Warnf("clock of the peer %s is %s of the local clock more than tolerance interval %v", id.String(), b, clockTolerance)
		ps.blockCommsWithPeer(p)
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

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	if traceHeartbeat {
		ps.Tracef(TraceTag, "sendHeartbeatToPeer from %s to %s", ps.host.ID().String, id.String)
	}

	stream, err := ps.host.NewStream(ps.Ctx(), id, lppProtocolHeartbeat)
	if err != nil {
		return
	}
	defer stream.Close()

	hbInfo := heartbeatInfo{
		clock:        time.Now(),
		ledgerIDHash: ps.ledgerIDHash,
		hasTxStore:   true,
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
			alive, configured := ps.NumPeers()
			ps.Log().Infof("node is connected to %d peer(s) out of %d configured", alive, configured)
			logNumPeersDeadline = nowis.Add(logNumPeersPeriod)
		}
		for _, id := range ps.getPeerIDsWithOpenComms() {
			ps.logInactivityIfNeeded(id)
			ps.sendHeartbeatToPeer(id)
		}
		select {
		case <-ps.stopHeartbeatChan:
			return
		case <-time.After(heartbeatRate):
		}
	}
}
