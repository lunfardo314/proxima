package gossip

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/transaction/txmetadata"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap/zapcore"
)

type (
	Environment interface {
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Tx           *transaction.Transaction
		ReceivedFrom *peer.ID
		Metadata     *txmetadata.TransactionMetadata
	}

	Gossip struct {
		*queue.Queue[*Input]
		env Environment
	}
)

const chanBufferSize = 10

func New(env Environment, lvl zapcore.Level) *Gossip {
	return &Gossip{
		Queue: queue.NewQueueWithBufferSize[*Input]("gossip", chanBufferSize, lvl, nil),
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
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.Metadata)
	} else {
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.Metadata, *inp.ReceivedFrom)
	}
}
