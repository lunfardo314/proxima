package gossip

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txmetadata"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap"
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

	Queue struct {
		*queue.Queue[*Input]
		env Environment
	}
)

const chanBufferSize = 10

func Start(env Environment, ctx context.Context) *Queue {
	ret := &Queue{
		Queue: queue.NewConsumerWithBufferSize[*Input]("gossip", chanBufferSize, zap.InfoLevel, nil),
		env:   env,
	}
	ret.AddOnConsume(ret.consume)
	go func() {
		ret.Log().Infof("starting..")
		ret.Run()
	}()
	go func() {
		<-ctx.Done()
		ret.Queue.Stop()
	}()
	return ret
}

func (q *Queue) consume(inp *Input) {
	if inp.ReceivedFrom == nil {
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.Metadata)
	} else {
		q.env.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.Metadata, *inp.ReceivedFrom)
	}
}
