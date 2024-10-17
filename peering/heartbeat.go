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

func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic, pullTargets int) {
	ps.forEachPeerRLock(func(p *Peer) bool {
		if p._isAlive() {
			if p.isStatic {
				aliveStatic++
			} else {
				aliveDynamic++
			}
		}
		if ps._isPullTarget(p) {
			pullTargets++
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
	id := stream.Conn().RemotePeer()
	remote := stream.Conn().RemoteMultiaddr()

	known, blacklisted, _ := ps.knownPeer(id, func(p *Peer) {
		p.numIncomingHB++
	})
	if blacklisted {
		// ignore
		//_ = stream.Close()
		return
	}
	if !known {
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			//_ = stream.Close()
			return
		}
		ps.Log().Infof("[peering] incoming peer request. Add new dynamic peer %s", id.String())
	}

	go func() {
		defer func() {
			stream.Close()
			ps.Log().Errorf("[peering] hb: streamHandler exit")
		}()
		for {
			var hbInfo heartbeatInfo
			msgData, err := readFrame(stream)
			//_ = stream.Close()
			ps.inMsgCounter.Inc()

			if err != nil {
				ps.Log().Errorf("[peering] hb: error while reading message from peer %s: err='%v'. Ignore", ShortPeerIDString(id), err)
				ps.dropPeer(id, err.Error())
				return
			}
			if hbInfo, err = heartbeatInfoFromBytes(msgData); err != nil {
				// protocol violation
				err = fmt.Errorf("[peering] hb: error while serializing message from peer %s: %v. Reset connection", ShortPeerIDString(id), err)
				ps.Log().Error(err)
				ps.dropPeer(id, err.Error())
				return
			}

			ps.withPeer(id, func(p *Peer) {
				if p == nil {
					addrInfo := &peer.AddrInfo{
						ID:    id,
						Addrs: []multiaddr.Multiaddr{remote},
					}
					p = ps._addPeer(addrInfo, "", false)
				}
				ps._evidenceHeartBeat(p, hbInfo)
			})
		}
	}()
}

func (ps *Peers) _evidenceHeartBeat(p *Peer, hbInfo heartbeatInfo) {
	nowis := time.Now()

	// clock differences
	diff := nowis.Sub(hbInfo.clock)
	p.clockDifferences[p.clockDifferencesIdx] = diff
	p.clockDifferencesIdx = (p.clockDifferencesIdx + 1) % len(p.clockDifferences)
	q := util.Quartiles(p.clockDifferences[:])
	p.clockDifferenceQuartiles = q

	if p.lastHeartbeatReceived.UnixNano() != 0 {
		// differences between heart beats
		diff = nowis.Sub(p.lastHeartbeatReceived)
		p.hbMsgDifferences[p.hbMsgDifferencesIdx] = diff
		p.hbMsgDifferencesIdx = (p.hbMsgDifferencesIdx + 1) % len(p.hbMsgDifferences)
		q = util.Quartiles(p.hbMsgDifferences[:])
		p.hbMsgDifferenceQuartiles = q
	}
	p.lastHeartbeatReceived = nowis

	p.respondsToPullRequests = hbInfo.respondsToPullRequests

	ps.Tracef(TraceTagHeartBeatRecv, ">>>>> received #%d from %s: clock diff: %v, median: %v, responds to pull: %v, alive: %v",
		hbInfo.counter, ShortPeerIDString(p.id), diff, q[1], p.respondsToPullRequests, p._isAlive())
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID, hbCounter uint32) {
	respondsToPull := true
	if ps.cfg.IgnoreAllPullRequests {
		respondsToPull = false
	} else if ps.cfg.AcceptPullRequestsFromStaticPeersOnly {
		respondsToPull = ps.staticPeers.Contains(id)
	}

	msg := &heartbeatInfo{
		// time now will be set in the queue consumer
		respondsToPullRequests: respondsToPull,
		counter:                hbCounter,
		clock:                  time.Now(),
	}
	if err := ps.sendMsgBytesOut(id, ps.lppProtocolHeartbeat, msg.Bytes(), ps.cfg.SendTimeoutHeartbeat); err != nil {
		ps.Tracef(TraceTagHeartBeatSend, ">>>>>>> failed to sent #%d to %s: %v", hbCounter, ShortPeerIDString(id), err)
	} else {
		ps.Tracef(TraceTagHeartBeatSend, ">>>>>>> sent #%d to %s", hbCounter, ShortPeerIDString(id))
	}
}

func (ps *Peers) peerIDsAlive(except ...peer.ID) []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if len(except) > 0 && p.id == except[0] {
			return true
		}
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
		if p.clockDifferenceQuartiles[1] > clockTolerance {
			logLines.Add("%s(%s): %v", ShortPeerIDString(p.id), util.Cond(p.isStatic, "static", "dynamic"), p.clockDifferenceQuartiles)
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
