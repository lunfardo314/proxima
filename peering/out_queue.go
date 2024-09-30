package peering

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// sendMsgOut is the output queue consumer callback
func (ps *Peers) sendMsgOut(inp outMsgData) {
	// message is wrapped into the interface specifically to set right time in heartbeat messages
	inp.msg.SetNow()

	stream, err := ps.host.NewStream(ps.Ctx(), inp.peerID, inp.protocol)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()

	if err = writeFrame(stream, inp.msg.Bytes()); err != nil {
		ps.Log().Errorf("[peering] error while sending message to peer %s", ShortPeerIDString(inp.peerID))
	}
	if inp.protocol == ps.lppProtocolHeartbeat {
		ps.Tracef(TraceTagHeartBeatSend, ">>>>> sent #%d to %s", inp.msg.Counter(), ShortPeerIDString(inp.peerID))
	}
	ps.outMsgCounter.Inc()
}

func (ps *Peers) sendMsgOutQueued(msg outMessageWrapper, id peer.ID, prot protocol.ID) {
	// heartbeat messages goes with priority
	ps.outQueue.Push(outMsgData{
		msg:      msg,
		peerID:   id,
		protocol: prot,
	}, prot == ps.lppProtocolHeartbeat)
}
