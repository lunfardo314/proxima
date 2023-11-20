package workflow

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/peering"
)

// RespondTxQueryConsumer:
// - takes pull requests for transaction from peering as inputs
// - looks up for the transaction into the store
// - sends it to the peer if found, otherwise ignores
//
// The reason to have it as a separate consumer is unbounded input queue and intensive DB lookups
// which may become a bottleneck in high TPS, e.g. during node syncing

const RespondTxQueryConsumerName = "txrespond"

type (
	RespondTxQueryInputData struct {
		TxID   core.TransactionID
		PeerID peering.PeerID
	}

	RespondTxQueryConsumer struct {
		*Consumer[RespondTxQueryInputData]
	}
)

func (w *Workflow) initRespondTxQueryConsumer() {
	c := &RespondTxQueryConsumer{
		Consumer: NewConsumer[RespondTxQueryInputData](RespondTxQueryConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.respondTxQueryConsumer = c
}

func (c *RespondTxQueryConsumer) consume(inp RespondTxQueryInputData) {
	if txBytes := c.glb.UTXOTangle().TxBytesStore().GetTxBytes(&inp.TxID); len(txBytes) > 0 {
		c.glb.peers.SendTxBytesToPeer(txBytes, inp.PeerID)
	}
}
