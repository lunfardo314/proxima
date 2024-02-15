package persist_txbytes

import (
	"context"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Glb
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

const (
	TraceTag       = "persist-txbytes"
	chanBufferSize = 10
)

func New(env Environment) *PersistTxBytes {
	return &PersistTxBytes{
		Queue:       queue.NewQueueWithBufferSize[Input]("txStore", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *PersistTxBytes) Start(ctx context.Context) {
	d.MarkStarted()
	d.AddOnClosed(func() {
		d.MarkStopped()
	})
	d.Queue.Start(d, ctx)
}

func (d *PersistTxBytes) Consume(inp Input) {
	txid, err := d.PersistTxBytesWithMetadata(inp.TxBytes, inp.Metadata)
	if err != nil {
		d.Environment.Log().Errorf("error while persisting transaction bytes: '%v'", err)
	} else {
		d.Tracef(TraceTag, "persisted tx bytes of %s, metadata: '%s'", txid.StringShort(), inp.Metadata.String())
	}
}
