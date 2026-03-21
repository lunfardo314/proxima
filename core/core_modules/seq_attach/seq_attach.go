// seq_attach core module queues sequencer transactions for attachment.
//
// Permanent attacher cap with timestamp-based deadlock prevention:
//   - Track the slot of the latest attached transaction
//   - When attacher count >= cap, only transactions with timestamp strictly
//     before the latest attached pass (dependencies are always older than dependents)
//   - Recursive pull depth is capped separately (maxAttachmentDepthForPull in virtual_tx.go)
//   - Forward-sync fills in deep dependencies that recursive pull can't reach
package seq_attach

import (
	"sync/atomic"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name = "seqAttach"
	// DefaultMaxConcurrentAttachers is the attacher cap.
	// When reached, only transactions older than the latest attached pass.
	DefaultMaxConcurrentAttachers = 20
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

	SeqAttach struct {
		environment
		*core_modules.CoreModule[*Input]
		attachFun          AttachFun
		latestAttachedSlot atomic.Uint32
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
	txid := inp.Tx.ID()
	txSlot := txid.Slot()

	// track the latest attached slot (atomic max) BEFORE the cap check
	// so that subsequent transactions at the same slot are correctly filtered
	for {
		cur := q.latestAttachedSlot.Load()
		if txSlot <= cur {
			break
		}
		if q.latestAttachedSlot.CompareAndSwap(cur, txSlot) {
			break
		}
	}

	// attacher cap with deadlock prevention:
	// Use pending+att as effective count. 'pending' is incremented here (synchronous,
	// single consume goroutine) and decremented when the attacher goroutine starts and
	// increments 'att'. This prevents leaking attachers between the cap check and
	// the async goroutine start.
	effectiveAtt := q.Counter("att") + q.Counter("pending")
	if effectiveAtt >= q.MaxConcurrentAttachers() {
		if txSlot >= q.latestAttachedSlot.Load() {
			q.IncCounter("seq_drop")
			return
		}
	}

	q.IncCounter("pending")
	q.attachFun(inp.Tx, inp.Opts...)
}
