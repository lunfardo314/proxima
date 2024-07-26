package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	ps.forEachPeer(func(p *Peer) bool {
		if p._isAlive() {
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

func (ps *Peers) logConnectionStatusIfNeeded(id peer.ID) {
	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
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

	})
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
// - to ensure clocks difference is within tolerance interval. Clock difference is
// sum of difference between local clocks plus communication delay
// Clock difference is perceived differently by two connected peers. If one of them
// goes out of tolerance interval, connection is dropped from one, then from the other side

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()

	if ps.isInBlacklist(id) {
		ps.Tracef(TraceTag, "heartbeatStreamHandler %s: %s is in blacklist", ps.host.ID().String, id.String)
		_ = stream.Reset()
		return
	}

	exit := false

	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			return
		}
		// incoming heartbeat from new peer
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Tracef(TraceTag, "autopeering disabled: unknown peer %s", id.String)
			_ = stream.Reset()
			exit = true
			return
		}
		// Add new incoming dynamic peer and then let the autopeering handle if too many

		// does not work -> addrInfo, err := peer.AddrInfoFromP2pAddr(remote)
		// for some reason peer.AddrInfoFromP2pAddr does not work -> compose AddrInfo from parts

		remote := stream.Conn().RemoteMultiaddr()
		addrInfo := &peer.AddrInfo{
			ID:    id,
			Addrs: []multiaddr.Multiaddr{remote},
		}
		ps.Log().Infof("[peering] incoming peer request from %s. Add new dynamic peer", ShortPeerIDString(id))
		ps._addPeer(addrInfo, "", false)
	})
	if exit {
		return
	}

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err == nil {
		hbInfo, err = heartbeatInfoFromBytes(msgData)
	}
	if err != nil {
		// protocol violation
		_ = stream.Reset()
		ps.Log().Errorf("[peering] error while reading message from peer %s: %v", ShortPeerIDString(id), err)
		ps.dropPeer(id, "read error")
		return
	}

	clockDiff, clockOk, behind := checkRemoteClockTolerance(hbInfo.clock)
	if !clockOk {
		_ = stream.Reset()
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.Log().Warnf("[peering] clock of the peer %s is %s of the local clock for %v > tolerance interval %v",
			ShortPeerIDString(id), b, clockDiff, clockTolerance)
		ps.dropPeer(id, "exceeded clock sync tolerance")
		return
	}
	defer func() { _ = stream.Close() }()

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
		p._evidenceActivity("hb")
		p.hasTxStore = hbInfo.hasTxStore
		p._evidenceClockDifference(clockDiff)
	})
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

func (ps *Peers) peerIDsAlive() []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeer(func(p *Peer) bool {
		if p._isAlive() {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
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

	for {
		nowis := time.Now()

		if nowis.After(logNumPeersDeadline) {
			aliveStatic, aliveDynamic := ps.NumAlive()

			ps.Log().Infof("[peering] node is connected to %d peer(s). Static: %d/%d, dynamic %d/%d)",
				aliveStatic+aliveDynamic, aliveStatic, len(ps.cfg.PreConfiguredPeers), aliveDynamic, ps.cfg.MaxDynamicPeers)

			logNumPeersDeadline = nowis.Add(logPeersEvery)
		}

		peerIDs := ps.peerIDs()

		for _, id := range peerIDs {
			ps.logConnectionStatusIfNeeded(id)
			ps.sendHeartbeatToPeer(id)
		}
		select {
		case <-ps.Environment.Ctx().Done():
			ps.Log().Infof("[peering] heartbeat loop stopped")
			return
		case <-time.After(heartbeatRate):
		}
	}
}
