package gossip

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Glb
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Tx           *transaction.Transaction
		ReceivedFrom *peer.ID
		Metadata     txmetadata.TransactionMetadata
	}

	Gossip struct {
		*queue.Queue[*Input]
		Environment
	}
)

const chanBufferSize = 10

func New(env Environment) *Gossip {
	return &Gossip{
		Queue:       queue.NewQueueWithBufferSize[*Input]("gossip", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *Gossip) Start(ctx context.Context) {
	d.MarkStarted()
	d.AddOnClosed(func() {
		d.MarkStopped()
	})
	d.Queue.Start(d, ctx)
}

func (d *Gossip) Consume(inp *Input) {
	if inp.ReceivedFrom == nil {
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata)
	} else {
		d.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata, *inp.ReceivedFrom)
	}
}
