package pull_server

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Logging
		global.TxBytesGet
		StateStore() global.StateStore
		SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool
	}

	Input struct {
		TxID   ledger.TransactionID
		PeerID peer.ID
	}

	PullServer struct {
		*queue.Queue[*Input]
		Environment
	}
)

const TraceTag = "pull-server"
const chanBufferSize = 10

func New(env Environment) *PullServer {
	return &PullServer{
		Queue:       queue.NewQueueWithBufferSize[*Input]("pullServer", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (q *PullServer) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	q.AddOnClosed(func() {
		doneOnClose.Done()
		q.Queue.Log().Debugf("on close done")
	})
	q.Queue.Start(q, ctx)
}

func (q *PullServer) Consume(inp *Input) {
	if txBytesWithMetadata := q.GetTxBytesWithMetadata(&inp.TxID); len(txBytesWithMetadata) > 0 {
		txBytes, metadataBytes, err := txmetadata.SplitBytesWithMetadata(txBytesWithMetadata)
		util.AssertNoError(err)
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		util.AssertNoError(err)
		if metadata == nil {
			metadata = &txmetadata.TransactionMetadata{}
		}
		// setting persistent 'response to pull' flag in metadata
		metadata.IsResponseToPull = true

		q.SendTxBytesWithMetadataToPeer(inp.PeerID, txBytes, metadata)
		q.Tracef(TraceTag, "-> FOUND %s", inp.TxID.StringShort)
	} else {
		// not found -> ignore
		q.Tracef(TraceTag, "-> NOT FOUND %s", inp.TxID.StringShort)
	}
}
