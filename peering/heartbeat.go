package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/exp/maps"
)

type heartbeatInfo struct {
	clock                  time.Time
	respondsToPullRequests bool
}

// flags of the heartbeat message. Information for the peer about the node
const (
	// flagRespondsToPullRequests if false, node ignores all pull requests from the message target
	flagRespondsToPullRequests = byte(0b00000001)
)

func heartbeatInfoFromBytes(data []byte) (heartbeatInfo, error) {
	if len(data) != 8+1 {
		return heartbeatInfo{}, fmt.Errorf("heartbeatInfoFromBytes: wrong data len")
	}
	ret := heartbeatInfo{
		clock: time.Unix(0, int64(binary.BigEndian.Uint64(data[:8]))),
	}
	ret.setFromFlags(data[8])
	return ret, nil
}

func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic int) {
	ps.forEachPeerRLock(func(p *Peer) bool {
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
				util.Cond(p.isStatic, "static", "dynamic"), ShortPeerIDString(id), p.name, ShortPeerIDString(ps.host.ID()))
			p.lastLoggedConnected = false
			return
		}

		if p._isAlive() && !p.lastLoggedConnected {
			ps.Log().Infof("[peering] CONNECTED to %s peer %s ('%s'). Host (self): %s",
				util.Cond(p.isStatic, "static", "dynamic"), ShortPeerIDString(id), p.name, ShortPeerIDString(ps.host.ID()))
			p.lastLoggedConnected = true
		}

	})
}

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	// received heartbeat message from peer
	ps.inMsgCounter.Inc()
	id := stream.Conn().RemotePeer()

	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	if ps._isInBlacklist(id) {
		// ignore
		_ = stream.Close()
		return
	}
	p := ps._getPeer(id)
	if p == nil {
		// unknown peer, peering request
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Tracef(TraceTag, "autopeering disabled: unknown peer %s", id.String)
			_ = stream.Close()
			return
		}
		// add new peer
		remote := stream.Conn().RemoteMultiaddr()
		addrInfo := &peer.AddrInfo{
			ID:    id,
			Addrs: []multiaddr.Multiaddr{remote},
		}
		ps.Log().Infof("[peering] incoming peer request. Add new dynamic peer %s", id.String())
		p = ps._addPeer(addrInfo, "", false)
	}
	util.Assertf(p != nil, "p != nil")

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err != nil {
		ps.Log().Errorf("[peering] hb: error while reading message from peer %s: err='%v'. Ignore", ShortPeerIDString(id), err)
		_ = stream.Close()
		return
	}

	if hbInfo, err = heartbeatInfoFromBytes(msgData); err != nil {
		// protocol violation
		err = fmt.Errorf("[peering] hb: error while serializing message from peer %s: %v. Reset connection", ShortPeerIDString(id), err)
		ps.Log().Error(err)
		ps._dropPeer(p, err.Error())
		_ = stream.Reset()
		return
	}
	_ = stream.Close()
	p._evidenceHeartBeat(hbInfo)
}

func (p *Peer) _evidenceHeartBeat(hbInfo heartbeatInfo) {
	nowis := time.Now()
	p.lastHeartbeatReceived = nowis
	p.clockDifferences[p.clockDifferencesIdx] = nowis.Sub(hbInfo.clock)
	p.clockDifferencesIdx = (p.clockDifferencesIdx + 1) % len(p.clockDifferences)
	p.clockDifferenceMedian = util.Median(p.clockDifferences[:])
	p.respondsToPullRequests = hbInfo.respondsToPullRequests
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	respondsToPull := false
	if !ps.cfg.IgnoreAllPullRequests {
		if ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
			respondsToPull = ps.staticPeers.Contains(id)
		}
	}
	ps.sendMsgOutQueued(&heartbeatInfo{
		// time now will be set in the queue consumer
		respondsToPullRequests: respondsToPull,
	}, id, ps.lppProtocolHeartbeat)
}

func (ps *Peers) peerIDsAlive() []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
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

// startHeartbeat periodically sends HB message to each known peer
func (ps *Peers) startHeartbeat() {
	var logNumPeersDeadline time.Time

	ps.RepeatInBackground("peering_heartbeat_loop", heartbeatRate, func() bool {
		nowis := time.Now()
		peerIDs := ps.peerIDs()

		for _, id := range peerIDs {
			ps.logConnectionStatusIfNeeded(id)
			ps.sendHeartbeatToPeer(id)
		}

		if nowis.After(logNumPeersDeadline) {
			aliveStatic, aliveDynamic := ps.NumAlive()

			ps.Log().Infof("[peering] node is connected to %d peer(s). Static: %d/%d, dynamic %d/%d) (took %v)",
				aliveStatic+aliveDynamic, aliveStatic, len(ps.cfg.PreConfiguredPeers),
				aliveDynamic, ps.cfg.MaxDynamicPeers, time.Since(nowis))

			logNumPeersDeadline = nowis.Add(logPeersEvery)
		}

		return true
	}, true)
}

func (ps *Peers) logBigClockDiffs() {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	logLines := lines.New()
	warn := false
	for _, p := range ps.peers {
		if p.clockDifferenceMedian > clockTolerance {
			logLines.Add("%s(%s): %v", ShortPeerIDString(p.id), util.Cond(p.isStatic, "static", "dynamic"), p.clockDifferenceMedian)
			warn = true
		}
	}
	if warn {
		ps.Log().Warnf("peers with clock difference median > tolerance (%d): {%s}", clockTolerance, logLines.Join(", "))
	}
}

func (hi *heartbeatInfo) flags() (ret byte) {
	if hi.respondsToPullRequests {
		ret |= flagRespondsToPullRequests
	}
	return
}

func (hi *heartbeatInfo) setFromFlags(fl byte) {
	hi.respondsToPullRequests = (fl & flagRespondsToPullRequests) != 0
}

func (hi *heartbeatInfo) Bytes() []byte {
	var buf bytes.Buffer
	var timeNanoBin [8]byte

	binary.BigEndian.PutUint64(timeNanoBin[:], uint64(hi.clock.UnixNano()))
	buf.Write(timeNanoBin[:])
	buf.WriteByte(hi.flags())
	return buf.Bytes()
}

func (hi *heartbeatInfo) SetNow() {
	hi.clock = time.Now()
}
