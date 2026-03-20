// nonseq_attach core module queues non-sequencer transactions for attachment.
// Non-pulled transactions are dropped when the memDAG non-sequencer vertex count
// exceeds the limit, preventing memory exhaustion under heavy transaction load.
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
		IsSyncing() bool
		SyncFrontierSlot() uint32
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
	// during sync: pass only pulled transactions with timestamps at or before the sync frontier
	if q.IsSyncing() {
		frontier := q.SyncFrontierSlot()
		txid := inp.Tx.ID()
		if !inp.Pulled || txid.Slot() > frontier {
			q.IncCounter("nonseq_drop")
			return
		}
	}

	// TODO only drop non-seq transactions when number of non-solid of them exceeds limit (not total number)
	//  reason: solid (validated) non-seq transaction do not consume CPU, only memory
	if !inp.Pulled && (q.Counter("nonseq") >= maxNonSeqVertices || q.Queue.Len() >= maxQueueLen) {
		q.IncCounter("nonseq_drop")
		return
	}
	q.attachFun(inp.Tx, inp.Opts...)
}
