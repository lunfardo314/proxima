package workflow

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
)

// PullRespondConsumer:
// - takes pull requests for transaction from peering as inputs
// - looks up for the transaction into the store
// - sends it to the peer if found, otherwise ignores
//
// The reason to have it as a separate consumer is unbounded input queue and intensive DB lookups
// which may become a bottleneck in high TPS, e.g. during node syncing

const PullRespondConsumerName = "pullRequest"

type (
	PullRespondData struct {
		TxID   core.TransactionID
		PeerID peer.ID
	}

	PullRespondConsumer struct {
		*Consumer[PullRespondData]
	}
)

func (w *Workflow) initRespondTxQueryConsumer() {
	ret := &PullRespondConsumer{
		Consumer: NewConsumer[PullRespondData](PullRespondConsumerName, w),
	}
	ret.AddOnConsume(ret.consume)
	w.pullRequestConsumer = ret
}

func (c *PullRespondConsumer) consume(inp PullRespondData) {
	if txBytes := c.glb.UTXOTangle().TxBytesStore().GetTxBytes(&inp.TxID); len(txBytes) > 0 {
		c.glb.peers.SendTxBytesToPeer(inp.PeerID, txBytes)
		fmt.Printf(">>>>>>>>>>>>>>> respond FOUND %s\n", inp.TxID.StringShort())
	} else {
		fmt.Printf(">>>>>>>>>>>>>>> respond NOT FOUND %s\n", inp.TxID.StringShort())
	}
}
