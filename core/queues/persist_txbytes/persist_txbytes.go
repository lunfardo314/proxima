package persist_txbytes

import (
	"context"
	"sync"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Logging
		global.TxBytesPersist
	}

	Input struct {
		TxBytes  []byte
		Metadata *txmetadata.TransactionMetadata
	}

	PersistTxBytes struct {
		*queue.Queue[Input]
		Environment
	}
)

const chanBufferSize = 10

func New(env Environment) *PersistTxBytes {
	return &PersistTxBytes{
		Queue:       queue.NewQueueWithBufferSize[Input]("txStore", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (q *PersistTxBytes) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	q.AddOnClosed(func() {
		doneOnClose.Done()
	})
	q.Queue.Start(q, ctx)
}

func (q *PersistTxBytes) Consume(inp Input) {
	txid, err := q.PersistTxBytesWithMetadata(inp.TxBytes, inp.Metadata)
	if err != nil {
		q.Environment.Log().Errorf("error while persisting transaction bytes: '%v'", err)
	} else {
		q.Tracef("persist_txbytes", "persisted tx bytes of %s, metadata: '%s'", txid.StringShort(), inp.Metadata.String())
	}
}
