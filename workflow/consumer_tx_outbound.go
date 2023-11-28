package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// TxGossipSendConsumer is forwarding the transaction to peering which didn't see it yet

const TxOutboundConsumerName = "gossip"

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
	c := &TxGossipSendConsumer{
		Consumer: NewConsumer[TxGossipSendInputData](TxOutboundConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.txGossipOutConsumer = c
}

func (c *TxGossipSendConsumer) consume(inp TxGossipSendInputData) {
	if inp.SourceType == TransactionSourceTypePeer {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.ReceivedFrom)
	} else {
		c.glb.peers.GossipTxBytesToPeers(inp.Tx.Bytes())
	}
}
