package persist_txbytes

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Glb
		TxBytesStore() global.TxBytesStore
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
	Name           = "txStore"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *PersistTxBytes {
	return &PersistTxBytes{
		Queue:       queue.NewQueueWithBufferSize[Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *PersistTxBytes) Start() {
	d.MarkComponentStarted(Name)
	d.AddOnClosed(func() {
		d.MarkComponentStopped(Name)
	})
	d.Queue.Start(d, d.Environment.Ctx())
}

func (d *PersistTxBytes) Consume(inp Input) {
	txid, err := d.TxBytesStore().PersistTxBytesWithMetadata(inp.TxBytes, inp.Metadata)
	if err != nil {
		d.Environment.Log().Errorf("error while persisting transaction bytes: '%v'", err)
	} else {
		d.Tracef(TraceTag, "persisted tx bytes of %s, metadata: '%s'", txid.StringShort(), inp.Metadata.String())
	}
}
