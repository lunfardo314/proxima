package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
)

// PullRequestConsumer:
// - takes pull requests for transaction from peering as inputs
// - looks up for the transaction into the store
// - sends it to the peer if found, otherwise ignores
//
// The reason to have it as a separate consumer is unbounded input queue and intensive DB lookups
// which may become a bottleneck in high TPS, e.g. during node syncing

const PullRequestConsumerName = "pullRequest"

type (
	PullRequestData struct {
		TxID   core.TransactionID
		PeerID peer.ID
	}

	PullRequestConsumer struct {
		*Consumer[PullRequestData]
	}
)

func (w *Workflow) initRespondTxQueryConsumer() {
	c := &PullRequestConsumer{
		Consumer: NewConsumer[PullRequestData](PullRequestConsumerName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})
	w.pullRequestConsumer = c
}

func (c *PullRequestConsumer) consume(inp PullRequestData) {
	if txBytes := c.glb.UTXOTangle().TxBytesStore().GetTxBytes(&inp.TxID); len(txBytes) > 0 {
		c.glb.peers.SendTxBytesToPeer(inp.PeerID, txBytes)
	}
}
