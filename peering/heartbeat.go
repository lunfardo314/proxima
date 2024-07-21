package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/checkpoints"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/exp/maps"
)

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

func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic int) {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	for _, p := range ps.peers {
		if p.isStatic {
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

func (ps *Peers) logConnectionStatusIfNeeded(id peer.ID) {
	p := ps.getPeer(id)
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p._isDead() && p.lastLoggedConnected {
		ps.Log().Infof("[peering] LOST CONNECTION with %s peer %s ('%s'). Host (self): %s",
			p.staticOrDynamic(), ShortPeerIDString(id), p.name, ShortPeerIDString(ps.host.ID()))
		p.lastLoggedConnected = false
		return
	}

	if p._isAlive() && !p.lastLoggedConnected {
		ps.Log().Infof("[peering] CONNECTED to %s peer %s ('%s'), msg src '%s'. Host (self): %s",
			p.staticOrDynamic(), ShortPeerIDString(id), p.name, p.lastMsgReceivedFrom, ShortPeerIDString(ps.host.ID()))
		p.lastLoggedConnected = true
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

	if ps.isInBlacklist(id) {
		ps.Tracef(TraceTag, "heartbeatStreamHandler %s: %s is in blacklist", ps.host.ID().String, id.String)
		_ = stream.Reset()
		return
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

		// does not work -> addrInfo, err := peer.AddrInfoFromP2pAddr(remote)
		// for some reason peer.AddrInfoFromP2pAddr does not work -> compose AddrInfo from parts

		remote := stream.Conn().RemoteMultiaddr()
		addrInfo := &peer.AddrInfo{
			ID:    id,
			Addrs: []multiaddr.Multiaddr{remote},
		}
		ps.Log().Infof("[peering] incoming peer request from %s. Add new dynamic peer", ShortPeerIDString(id))
		p = ps.addPeer(addrInfo, "", false)
	}

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err == nil {
		hbInfo, err = heartbeatInfoFromBytes(msgData)
	}
	if err != nil {
		// protocol violation
		ps.Log().Errorf("[peering] error while reading message from peer %s: %v", ShortPeerIDString(id), err)
		ps.dropPeer(id, "read error")
		_ = stream.Reset()
		return
	}

	clockDiff, clockOk, behind := checkRemoteClockTolerance(hbInfo.clock)
	if !clockOk {
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.Log().Warnf("[peering] clock of the peer %s is %s of the local clock for %v > tolerance interval %v",
			ShortPeerIDString(id), b, clockDiff, clockTolerance)
		ps.dropPeer(id, "over clock tolerance")
		_ = stream.Reset()
		return
	}
	defer func() { _ = stream.Close() }()

	p.evidence(
		_evidenceActivity("hb"),
		_evidenceTxStore(hbInfo.hasTxStore),
		_evidenceClockDifference(clockDiff),
	)
	util.Assertf(p.isAlive(), "isAlive")
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	stream, err := ps.host.NewStream(ps.Ctx(), id, ps.lppProtocolHeartbeat)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()

	hbInfo := heartbeatInfo{
		clock:      time.Now(),
		hasTxStore: true,
	}
	err = writeFrame(stream, hbInfo.Bytes())
	if err != nil {
		ps.Log().Errorf("[peering] sendHeartbeatToPeer: %w", err)
	}
}

func (ps *Peers) peerIDs() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return maps.Keys(ps.peers)
}

// heartbeatLoop periodically sends HB message to each known peer
func (ps *Peers) heartbeatLoop() {
	var logNumPeersDeadline time.Time

	ps.Log().Infof("[peering] start heartbeat loop")

	// TODO debugging loop stop

	check := checkpoints.New(ps.Ctx(), ps.Log())

	const checkPeriod = heartbeatRate * 10

	prevTime := time.Now()
	count := 0
	for {
		nowis := time.Now()
		ps.Infof0(">>>> HB %d. Nowis: %s, diff: %v", count, nowis.Format("15:04:05,000"), nowis.Sub(prevTime))
		prevTime = nowis

		if nowis.After(logNumPeersDeadline) {
			check.Check("NumAlive", checkPeriod)
			aliveStatic, aliveDynamic := ps.NumAlive()
			check.Check("NumAlive")

			ps.Log().Infof("[peering] node is connected to %d (%d + %d) peer(s). Pre-configured static: %d, max dynamic: %d",
				aliveStatic+aliveDynamic, aliveStatic, aliveDynamic, len(ps.cfg.PreConfiguredPeers), ps.cfg.MaxDynamicPeers)

			logNumPeersDeadline = nowis.Add(logPeersEvery)
		}

		check.Check("peerIDs", checkPeriod)
		peerIDs := ps.peerIDs()
		check.Check("peerIDs")

		for _, id := range peerIDs {
			check.Check("logConnectionStatusIfNeeded", checkPeriod)
			ps.logConnectionStatusIfNeeded(id)
			check.Check("logConnectionStatusIfNeeded")

			check.Check("sendHeartbeatToPeer", checkPeriod)
			ps.sendHeartbeatToPeer(id)
			check.Check("sendHeartbeatToPeer")
		}
		select {
		case <-ps.Environment.Ctx().Done():
			ps.Log().Infof("[peering] heartbeat loop stopped")
			return
		case <-time.After(heartbeatRate):
		}
	}
}
