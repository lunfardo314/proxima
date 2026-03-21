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
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name = "nonSeqAttach"
	// maxNonSeqVertices is the memDAG non-sequencer vertex count threshold.
	// When exceeded, non-pulled non-sequencer transactions are dropped.
	maxNonSeqVertices = 5000
	// maxQueueLen is the maximum queue length before non-pulled transactions are dropped.
	// Prevents unbounded queue growth when transactions arrive faster than they can be attached.
	maxQueueLen = 1000
)

type (
	environment interface {
		global.NodeGlobal
		MaxConcurrentAttachers() int
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
		attachFun AttachFun
	}
)

func New(env environment, attachFun AttachFun) *NonSeqAttach {
	ret := &NonSeqAttach{
		environment: env,
		attachFun:   attachFun,
	}
	ret.CoreModule = core_modules.New(env, Name, ret.consume)
	ret.CoreModule.Start()
	ret.Queue.OnLenChange(func(n int) {
		ret.SetCounter("nonseq_attach_q", n)
	})
	return ret
}

func (q *NonSeqAttach) consume(inp *Input) {
	// drop non-pulled non-seq transactions when resources are constrained
	if !inp.Pulled && (q.Counter("att") >= q.MaxConcurrentAttachers() ||
		q.Counter("nonseq") >= maxNonSeqVertices ||
		q.Queue.Len() >= maxQueueLen) {
		q.IncCounter("nonseq_drop")
		return
	}
	q.attachFun(inp.Tx, inp.Opts...)
}
