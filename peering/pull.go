package peering

import (
	"math/rand"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
)

func (ps *Peers) pullStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		_ = stream.Reset()
		return
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	msgData, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	defer stream.Close()

	txLst, err := decodePeerMsgPull(msgData)
	if err != nil {
		ps.log.Errorf("error while decoding pull message from peer %s: %v", id.String(), err)
		return
	}

	p.evidenceActivity(ps, "pull")
	ps.onReceivePull(id, txLst)
}

func (ps *Peers) sendPullToPeer(id peer.ID, txLst ...core.TransactionID) {
	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolPull)
	if err != nil {
		return
	}
	defer stream.Close()

	_ = writeFrame(stream, encodePeerMsgPull(txLst...))
}

// PullTransactionsFromRandomPeer sends pull request to the random peer which has txStore
func (ps *Peers) PullTransactionsFromRandomPeer(txids ...core.TransactionID) bool {
	if len(txids) == 0 {
		return false
	}
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := util.Keys(ps.peers)
	for _, idx := range rand.Perm(len(all)) {
		rndID := all[idx]
		p := ps.peers[rndID]
		if p.isCommunicationOpen() && p.isAlive() && p.HasTxStore() {
			global.TracePull(ps.log, "pull from random peer %s: %s",
				func() any { return ShortPeerIDString(rndID) },
				func() any { return _txidLst(txids...) },
			)

			ps.sendPullToPeer(rndID, txids...)
			return true
		}
	}
	return false
}

func _txidLst(txids ...core.TransactionID) string {
	ret := make([]string, len(txids))
	for i := range ret {
		ret[i] = txids[i].StringShort()
	}
	return strings.Join(ret, ",")
}
