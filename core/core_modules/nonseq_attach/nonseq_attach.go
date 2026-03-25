// nonseq_attach core module queues non-sequencer transactions for attachment.
// Non-pulled transactions are dropped when the attacher cap is reached,
// or when memDAG/queue limits are exceeded.
// Dropped transactions remain in the txstore and can be pulled later if needed.
// Pulled transactions always pass immediately (with queue priority).
package nonseq_attach

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name = "nonSeqAttach"
	// maxNonSeqVertices is the memDAG non-sequencer vertex count threshold.
	// When exceeded, non-pulled non-sequencer transactions are dropped.
	maxNonSeqVertices = 5000
	// maxNonSeqVerticesAccessNode is the threshold for access nodes (no local sequencer).
	// Access nodes don't filter by sequencer target, so all non-seq txs are attached.
	// Lower threshold prevents goroutine/memory accumulation under heavy non-seq load.
	maxNonSeqVerticesAccessNode = 500
	// maxQueueLen is the maximum queue length before non-pulled transactions are dropped.
	// Prevents unbounded queue growth when transactions arrive faster than they can be attached.
	maxQueueLen = 1000
)

type (
	environment interface {
		global.NodeGlobal
		MaxConcurrentAttachers() int
		GetOwnSequencerID() *base.ChainID
	}

	// AttachFun performs the actual attachment (workflow._attach)
	AttachFun func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)

	Input struct {
		Tx     *transaction.Transaction
		Opts   []attacher.AttachTxOption
		Pulled bool
	}

	NonSeqAttach struct {
		environment
		*core_modules.CoreModule[*Input]
		attachFun      AttachFun
		vertexLimit    int // cached: maxNonSeqVertices or maxNonSeqVerticesAccessNode
	}
)

func New(env environment, attachFun AttachFun) *NonSeqAttach {
	limit := maxNonSeqVertices
	if env.GetOwnSequencerID() == nil {
		// access node: no sequencer target filter, so all non-seq txs pass.
		// Use lower vertex limit to bound resource usage.
		limit = maxNonSeqVerticesAccessNode
		env.Log().Infof("[%s] access node mode: non-seq vertex limit = %d", Name, limit)
	}
	ret := &NonSeqAttach{
		environment: env,
		attachFun:   attachFun,
		vertexLimit: limit,
	}
	ret.CoreModule = core_modules.New(env, Name, ret.consume)
	ret.CoreModule.Start()
	ret.Queue.OnLenChange(func(n int) {
		ret.SetCounter("nonseq_attach_q", n)
	})
	return ret
}

// PushNonSeqTransaction decides whether to accept or drop a non-sequencer transaction
// before it enters the queue. Dropped transactions remain in the txstore and can be
// pulled later if needed for solidification.
func (q *NonSeqAttach) PushNonSeqTransaction(inp *Input) {
	if !inp.Pulled {
		if q.Queue.Len() >= maxQueueLen || q.Counter("nonseq") >= q.vertexLimit {
			q.IncCounter("nonseq_drop")
			return
		}
	}
	q.Queue.Push(inp, inp.Pulled)
}

func (q *NonSeqAttach) consume(inp *Input) {
	if !inp.Pulled {
		// drop non-pulled non-seq transactions when resources are constrained or during snapshot
		if q.IsSnapshotting() ||
			q.Counter("att") >= q.MaxConcurrentAttachers() ||
			q.Counter("nonseq") >= q.vertexLimit {
			q.IncCounter("nonseq_drop")
			return
		}
		// drop non-pulled non-seq transactions that don't target the local sequencer.
		// When seqID is nil (no local sequencer or test environment), the filter is disabled.
		// Dropped txs remain in txstore and can be pulled later during solidification.
		if seqID := q.GetOwnSequencerID(); seqID != nil && !inp.Tx.HasOutputForSequencer(*seqID) {
			q.IncCounter("nonseq_drop")
			return
		}
	}
	q.attachFun(inp.Tx, inp.Opts...)
}
