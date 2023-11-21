package peering

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/atomic"
)

const traceHeartbeat = false

// clockTolerance is how big the difference between local and remote clocks is tolerated
const clockTolerance = 5 * time.Second // for testing only

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

// heartbeat protocol is used to monitor if peer is alive and to ensure clocks are synced within tolerance interval

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	defer stream.Close()

	id := stream.Conn().RemotePeer()
	if traceHeartbeat {
		ps.trace("heartbeatStreamHandler invoked in %s from %s", ps.host.ID().String(), id.String())
	}

	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		return
	}

	msgData, err := readFrame(stream)
	if err != nil || len(msgData) != 8 {
		if err == nil {
			err = errors.New("exactly 8 bytes expected")
		}
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		return
	}

	remoteClock := time.Unix(0, int64(binary.BigEndian.Uint64(msgData)))
	if clockOk, behind := checkRemoteClockTolerance(remoteClock); !clockOk {
		b := "ahead"
		if behind {
			b = "behind"
		}
		ps.log.Warnf("clock of the peer %s is %s of the local clock more than tolerance interval %v", id.String(), b, clockTolerance)
		// TODO do something with remote peer with unsynced clock
		// for example mark unworkable and then retry after 1 min or so
		return
	}

	p.mutex.Lock()
	p.lastActivity = time.Now()
	p.mutex.Unlock()
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	if traceHeartbeat {
		ps.trace("sendHeartbeatToPeer from %s to %s", ps.host.ID().String(), id.String())
	}

	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolHeartbeat)
	if err != nil {
		return
	}
	defer stream.Close()

	var timeBuf [8]byte
	binary.BigEndian.PutUint64(timeBuf[:], uint64(time.Now().UnixNano()))

	_ = writeFrame(stream, timeBuf[:])
}

const heartbeatRate = time.Second

func (ps *Peers) heartbeatLoop(exit *atomic.Bool) {
	for !exit.Load() {
		for _, id := range ps.getPeerIDs() {
			ps.sendHeartbeatToPeer(id)
		}
		time.Sleep(heartbeatRate)
	}
}
