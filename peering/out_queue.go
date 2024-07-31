package peering

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (ps *Peers) Consume(inp outMsgData) {
	// message is wrapped into the interface specifically to set wight time in heartbeat messages
	inp.msg.SetTime(time.Now())

	stream, err := ps.host.NewStream(ps.Ctx(), inp.peerID, inp.msg.ProtocolID())
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()

	if err = writeFrame(stream, inp.msg.Bytes()); err != nil {
		ps.Log().Errorf("[peering] error while sending message to peer %s", ShortPeerIDString(inp.peerID))
	}
}

func (ps *Peers) sendMsgOutQueued(msg outMessageWrapper, id peer.ID, priority bool) {
	ps.outQueue.Push(outMsgData{
		msg:    msg,
		peerID: id,
	}, priority)
}
