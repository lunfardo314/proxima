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
	clock               time.Time
	ignoresPullRequests bool
}

// flags of the heartbeat message. Information for the peer about the node
const (
	// flagIgnoresPullRequests node ignores all pull requests from the message target
	flagIgnoresPullRequests = byte(0b00000001)
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

// heartbeat protocol is used to monitor
// - if peer is alive and
// - to ensure clocks difference is within tolerance interval. Clock difference is
// sum of difference between local clocks plus communication delay
// Clock difference is perceived differently by two connected peers. If one of them
// goes out of tolerance interval, connection is dropped from one, then from the other side

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	ps.inMsgCounter.Inc()

	id := stream.Conn().RemotePeer()

	if ps.isInBlacklist(id) {
		// ignore
		//_ = stream.Reset()
		_ = stream.Close()
		return
	}

	exit := false

	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			// known peer, static or dynamic
			return
		}
		// incoming heartbeat from new peer
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Tracef(TraceTag, "autopeering disabled: unknown peer %s", id.String)

			//  do not be harsh, just ignore
			//_ = stream.Reset()
			_ = stream.Close()
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
		ps.Log().Infof("[peering] incoming peer request. Add new dynamic peer %s", id.String())
		ps._addPeer(addrInfo, "", false)
	})
	if exit {
		return
	}

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err != nil {
		ps.Log().Errorf("[peering] hb: error while reading message from peer %s: err='%v'. Ignore", ShortPeerIDString(id), err)
		// ignore
		ps.withPeer(id, func(p *Peer) {
			if p != nil {
				p.errorCounter++
			}
		})
		_ = stream.Close()
		return
	}

	if hbInfo, err = heartbeatInfoFromBytes(msgData); err != nil {
		// protocol violation
		err = fmt.Errorf("[peering] hb: error while serializing message from peer %s: %v. Reset stream", ShortPeerIDString(id), err)
		ps.Log().Error(err)
		ps.dropPeer(id, err.Error())
		_ = stream.Reset()
		return
	}

	_ = stream.Close()

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
		p.lastHeartbeatReceived = time.Now()
		p.ignoresPullRequests = hbInfo.ignoresPullRequests
		p._evidenceClockDifference(time.Since(hbInfo.clock))
	})
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	ignore := (ps.cfg.AcceptPullRequestsFromStaticPeersOnly && !ps.staticPeers.Contains(id)) || ps.cfg.IgnoreAllPullRequests
	ps.sendMsgOutQueued(&heartbeatInfo{
		// time now will be set in the queue consumer
		ignoresPullRequests: ignore,
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
	if hi.ignoresPullRequests {
		ret |= flagIgnoresPullRequests
	}
	return
}

func (hi *heartbeatInfo) setFromFlags(fl byte) {
	hi.ignoresPullRequests = (fl & flagIgnoresPullRequests) != 0
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
