package workflow_old

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/txmetadata"
	"github.com/lunfardo314/unitrie/common"
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
	if txBytes := c.glb.txBytesStore.GetTxBytes(&inp.TxID); len(txBytes) > 0 {
		var root common.VCommitment
		if inp.TxID.IsBranchTransaction() {
			if rr, found := multistate.FetchRootRecord(c.glb.utxoTangle.StateStore(), inp.TxID); found {
				root = rr.Root
			}
		}
		c.glb.peers.SendTxBytesToPeer(inp.PeerID, txBytes, &txmetadata.TransactionMetadata{
			SendType:  txmetadata.SendTypeResponseToPull,
			StateRoot: root,
		})
		c.tracePull("-> FOUND %s", func() any { return inp.TxID.StringShort() })
	} else {
		// not found -> ignore
		c.tracePull("-> NOT FOUND %s", func() any { return inp.TxID.StringShort() })
	}
}
