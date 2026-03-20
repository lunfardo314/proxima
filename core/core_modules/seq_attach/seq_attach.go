// seq_attach core module queues sequencer transactions for attachment.
// Non-pulled sequencer transactions are dropped when the attacher limit is reached.
// During sync, only pulled transactions with timestamps at or before the sync frontier pass.
package seq_attach

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name = "seqAttach"
	// DefaultMaxConcurrentAttachers limits concurrent sequencer attacher goroutines.
	// Only sequencer transactions spawn attacher goroutines; non-sequencer transactions
	// are just added to the memDAG without an attacher.
	// When reached, non-pulled sequencer transactions are dropped.
	// Pulled transactions (needed for solidification/syncing) always pass.
	DefaultMaxConcurrentAttachers = 20
)

type (
	environment interface {
		global.NodeGlobal
		IsSynced() bool
		IsSyncing() bool
		SyncFrontierSlot() uint32
		MaxConcurrentAttachers() int
	}

	// AttachFun performs the actual attachment (workflow._attach)
	AttachFun func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)

	Input struct {
		Tx     *transaction.Transaction
		Opts   []attacher.AttachTxOption
		Pulled bool
	}

	SeqAttach struct {
		environment
		*core_modules.CoreModule[*Input]
		attachFun AttachFun
	}
)

func New(env environment, attachFun AttachFun) *SeqAttach {
	ret := &SeqAttach{
		environment: env,
		attachFun:   attachFun,
	}
	ret.CoreModule = core_modules.New(env, Name, ret.consume)
	ret.CoreModule.Start()
	ret.Queue.OnLenChange(func(n int) {
		ret.SetCounter("seq_attach_q", n)
	})
	return ret
}

func (q *SeqAttach) consume(inp *Input) {
	if q.IsSyncing() {
		frontier := q.SyncFrontierSlot()
		txid := inp.Tx.ID()
		// during sync: drop non-pulled and transactions beyond the sync frontier
		if !inp.Pulled || txid.Slot() > frontier {
			q.IncCounter("seq_drop")
			return
		}
		// during sync: enforce attacher limit even for pulled transactions
		// to prevent recursive pull explosion in deep past cones
		if q.Counter("att") >= q.MaxConcurrentAttachers() {
			return
		}
	} else {
		// normal operation: drop non-pulled when attacher limit is reached
		if !inp.Pulled && q.Counter("att") >= q.MaxConcurrentAttachers() {
			q.IncCounter("seq_drop")
			return
		}
	}
	q.attachFun(inp.Tx, inp.Opts...)
}
