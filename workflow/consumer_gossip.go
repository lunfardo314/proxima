package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/transaction/txmetadata"
)

// TxGossipSendConsumer is forwarding the transaction to peering which didn't see it yet

const TxGossipConsumerName = "gossip"

type (
	TxGossipSendInputData struct {
		*PrimaryTransactionData
		ReceivedFrom peer.ID
		Metadata     *txmetadata.TransactionMetadata
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
		c.glb.peers.GossipTxBytesToPeers(inp.tx.Bytes(), inp.Metadata, inp.ReceivedFrom)
	} else {
		c.glb.peers.GossipTxBytesToPeers(inp.tx.Bytes(), inp.Metadata)
	}
}
