package workflow

import (
	"github.com/lunfardo314/proxima/peering"
)

// TxOutboundConsumer is forwarding the transaction to peers which didn't see it yet

const TxOutboundConsumerName = "outbound"

type (
	TxOutboundConsumerData struct {
		*PrimaryInputConsumerData
		ReceivedFrom peering.PeerID
	}

	TxOutboundConsumer struct {
		*Consumer[TxOutboundConsumerData]
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
	var targetPeers []peering.Peer
	if inp.SourceType == TransactionSourceTypePeer {
		targetPeers = c.glb.peers.OutboundGossipPeers(inp.ReceivedFrom)
	} else {
		targetPeers = c.glb.peers.OutboundGossipPeers()
	}
	txBytesMsg := peering.EncodePeerMessageTypeTxBytes(inp.Tx.Bytes())
	for _, p := range targetPeers {
		p.SendMsgBytes(txBytesMsg)
	}
}
