package pull_server

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction/txmetadata"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/unitrie/common"
)

type (
	Environment interface {
		global.Logging
		TxBytesStore() global.TxBytesStore
		StateStore() global.StateStore
		SendTxBytesToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool
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
	})
	q.Queue.Start(q, ctx)
}

func (q *PullServer) Consume(inp *Input) {
	if txBytes := q.TxBytesStore().GetTxBytes(&inp.TxID); len(txBytes) > 0 {
		var root common.VCommitment
		if inp.TxID.IsBranchTransaction() {
			if rr, found := multistate.FetchRootRecord(q.StateStore(), inp.TxID); found {
				root = rr.Root
			}
		}
		q.SendTxBytesToPeer(inp.PeerID, txBytes, &txmetadata.TransactionMetadata{
			SendType:  txmetadata.SendTypeResponseToPull,
			StateRoot: root,
		})
		q.Tracef("pull", "-> FOUND %s", inp.TxID.StringShort)
	} else {
		// not found -> ignore
		q.Tracef("pull", "-> NOT FOUND %s", inp.TxID.StringShort)
	}
}
