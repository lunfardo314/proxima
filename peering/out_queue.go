package peering

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

func (ps *Peers) Consume(inp outMsgData) {
	stream, err := ps.host.NewStream(ps.Ctx(), inp.peerID, inp.protocol)
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()

	if err = writeFrame(stream, inp.msg); err != nil {
		ps.Log().Errorf("[peering] error while sending message to peer %s", ShortPeerIDString(inp.peerID))
	}
}

func (ps *Peers) sendMsgAsync(msg []byte, id peer.ID, protocolID protocol.ID) {
	// pushing heartbeat with priority
	ps.outQueue.Push(outMsgData{
		msg:      msg,
		peerID:   id,
		protocol: protocolID,
	}, protocolID == ps.lppProtocolHeartbeat)
}
