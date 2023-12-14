package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// TxGossipSendConsumer is forwarding the transaction to peering which didn't see it yet

const TxGossipConsumerName = "gossip"

type (
	TxGossipSendInputData struct {
		*PrimaryTransactionData
		ReceivedFrom peer.ID
	}

	TxGossipSendConsumer struct {
		*Consumer[TxGossipSendInputData]
	}
)

func (w *Workflow) initGossipSendConsumer() {
	ret := &TxGossipSendConsumer{
		Consumer: NewConsumer[TxGossipSendInputData](TxGossipConsumerName, w),
	}
	ret.AddOnConsume(ret.consume)

	w.txGossipOutConsumer = ret
}

func (c *TxGossipSendConsumer) consume(inp TxGossipSendInputData) {
	if inp.source == TransactionSourcePeer {
		c.glb.peers.GossipTxBytesToPeers(inp.tx.Bytes(), nil, inp.ReceivedFrom)
	} else {
		c.glb.peers.GossipTxBytesToPeers(inp.tx.Bytes(), nil)
	}
}
