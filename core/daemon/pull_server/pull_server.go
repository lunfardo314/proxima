package pull_server

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.Glb
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

const TraceTag = "pull_server"
const chanBufferSize = 10

func New(env Environment) *PullServer {
	return &PullServer{
		Queue:       queue.NewQueueWithBufferSize[*Input]("pullServer", chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *PullServer) Start(ctx context.Context) {
	d.MarkStarted()
	d.AddOnClosed(func() {
		d.MarkStopped()
	})
	d.Queue.Start(d, ctx)
}

func (d *PullServer) Consume(inp *Input) {
	if txBytesWithMetadata := d.GetTxBytesWithMetadata(&inp.TxID); len(txBytesWithMetadata) > 0 {
		txBytes, metadataBytes, err := txmetadata.SplitBytesWithMetadata(txBytesWithMetadata)
		util.AssertNoError(err)
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		util.AssertNoError(err)
		if metadata == nil {
			metadata = &txmetadata.TransactionMetadata{}
		}
		// setting persistent 'response to pull' flag in metadata
		metadata.IsResponseToPull = true

		d.SendTxBytesWithMetadataToPeer(inp.PeerID, txBytes, metadata)
		d.Tracef(TraceTag, "-> FOUND %s", inp.TxID.StringShort)
	} else {
		// not found -> ignore
		d.Tracef(TraceTag, "-> NOT FOUND %s", inp.TxID.StringShort)
	}
}
