package workflow

import (
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/transaction"
)

// TxOutboundConsumer is forwarding the transaction to peers which didn't see it yet

const TxOutboundConsumerName = "outbound"

type (
	TxOutboundConsumerData struct {
		Tx           *transaction.Transaction
		ReceivedFrom peering.PeerID
	}

	TxOutboundConsumer struct {
		*Consumer[TxOutboundConsumerData]
		peers peering.Peers
	}
)

func (w *Workflow) initTxOutboundConsumer() {
	c := &TxOutboundConsumer{
		Consumer: NewConsumer[TxOutboundConsumerData](TxOutboundConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.txOutboundConsumer = c
}

func (c *TxOutboundConsumer) consume(inp TxOutboundConsumerData) {
	targetPeers := c.peers.OutboundGossipPeers(inp.ReceivedFrom)
	txBytesMsg := peering.EncodePeerMessageTypeTxBytes(inp.Tx.Bytes())
	for _, p := range targetPeers {
		p.SendMsgBytes(txBytesMsg)
	}
}
