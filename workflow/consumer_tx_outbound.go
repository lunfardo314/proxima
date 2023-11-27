package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// TxGossipOutConsumer is forwarding the transaction to peering which didn't see it yet

const TxOutboundConsumerName = "gossip"

type (
	TxGossipOutInputData struct {
		*PrimaryTransactionData
		ReceivedFrom peer.ID
	}

	TxGossipOutConsumer struct {
		*Consumer[TxGossipOutInputData]
	}
)

func (w *Workflow) initGossipOutConsumer() {
	c := &TxGossipOutConsumer{
		Consumer: NewConsumer[TxGossipOutInputData](TxOutboundConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.txGossipOutConsumer = c
}

func (c *TxGossipOutConsumer) consume(inp TxGossipOutInputData) {
	if inp.SourceType == TransactionSourceTypePeer {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.ReceivedFrom)
	} else {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes())
	}
}
