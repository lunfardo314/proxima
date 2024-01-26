package gossip

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Logging
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Tx           *transaction.Transaction
		ReceivedFrom *peer.ID
		Metadata     txmetadata.TransactionMetadata
	}

	Gossip struct {
		*queue.Queue[*Input]
		env Environment
	}
)

const chanBufferSize = 10

func New(env Environment) *Gossip {
	return &Gossip{
		Queue: queue.NewQueueWithBufferSize[*Input]("gossip", chanBufferSize, env.Log().Level(), nil),
		env:   env,
	}
}

func (q *Gossip) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	q.AddOnClosed(func() {
		doneOnClose.Done()
	})
	q.Queue.Start(q, ctx)
}

func (q *Gossip) Consume(inp *Input) {
	if inp.ReceivedFrom == nil {
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata)
	} else {
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), &inp.Metadata, *inp.ReceivedFrom)
	}
}
