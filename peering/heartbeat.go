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
	counter                uint32
	respondsToPullRequests bool
}

// flags of the heartbeat message. Information for the peer about the node
const (
	// flagRespondsToPullRequests if false, node ignores all pull requests from the message target
	flagRespondsToPullRequests = byte(0b00000001)
)

const (
	TraceTagHeartBeatRecv = "peering_hb_recv"
	TraceTagHeartBeatSend = "peering_hb_send"
)

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
	ps._evidenceHeartBeat(p, hbInfo)
}

func (ps *Peers) _evidenceHeartBeat(p *Peer, hbInfo heartbeatInfo) {
	nowis := time.Now()
	p.lastHeartbeatReceived = nowis
	diff := nowis.Sub(hbInfo.clock)
	p.clockDifferences[p.clockDifferencesIdx] = diff
	p.clockDifferencesIdx = (p.clockDifferencesIdx + 1) % len(p.clockDifferences)
	m := util.Median(p.clockDifferences[:])
	p.clockDifferenceMedian = m
	p.respondsToPullRequests = hbInfo.respondsToPullRequests

	ps.Tracef(TraceTagHeartBeatRecv, ">>>>> received #%d from %s: clock diff: %v, median: %v, alive: %v",
		hbInfo.counter, ShortPeerIDString(p.id), diff, m, p._isAlive())
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID, hbCounter uint32) {
	respondsToPull := false
	if !ps.cfg.IgnoreAllPullRequests {
		if ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
			respondsToPull = ps.staticPeers.Contains(id)
		}
	}
	msg := &heartbeatInfo{
		// time now will be set in the queue consumer
		respondsToPullRequests: respondsToPull,
		counter:                hbCounter,
		clock:                  time.Now(),
	}
	if ps.sendMsgBytesOut(id, ps.lppProtocolHeartbeat, msg.Bytes()) {
		ps.Tracef(TraceTagHeartBeatSend, ">>>>>>> sent #%d to %s", hbCounter, ShortPeerIDString(id))
	}
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

	buf.WriteByte(hi.flags())
	_ = binary.Write(&buf, binary.BigEndian, uint64(hi.clock.UnixNano()))
	_ = binary.Write(&buf, binary.BigEndian, hi.counter)
	return buf.Bytes()
}

func (hi *heartbeatInfo) SetNow() {
	hi.clock = time.Now()
}

func (hi *heartbeatInfo) Counter() uint32 {
	return hi.counter
}

func heartbeatInfoFromBytes(data []byte) (heartbeatInfo, error) {
	if len(data) != 1+8+4 {
		return heartbeatInfo{}, fmt.Errorf("heartbeatInfoFromBytes: wrong data len")
	}
	ret := heartbeatInfo{
		clock:   time.Unix(0, int64(binary.BigEndian.Uint64(data[1:9]))),
		counter: binary.BigEndian.Uint32(data[9 : 9+4]),
	}
	ret.setFromFlags(data[0])
	return ret, nil
}
