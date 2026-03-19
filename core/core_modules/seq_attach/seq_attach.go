// seq_attach core module queues sequencer transactions for attachment.
// Non-pulled sequencer transactions block when the number of concurrent attacher
// goroutines exceeds the limit, creating backpressure upstream.
// Pulled transactions always pass immediately (with queue priority).
package seq_attach

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name = "seqAttach"
	// maxConcurrentAttachers limits concurrent sequencer attacher goroutines.
	// Only sequencer transactions spawn attacher goroutines; non-sequencer transactions
	// are just added to the memDAG without an attacher.
	// When reached, non-pulled sequencer transactions block until an attacher finishes.
	maxConcurrentAttachers = 50
	blockPollInterval      = 50 * time.Millisecond
)

type (
	environment interface {
		global.NodeGlobal
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
	if !inp.Pulled {
		for q.Counter("att") >= maxConcurrentAttachers {
			select {
			case <-q.Ctx().Done():
				return
			case <-time.After(blockPollInterval):
			}
		}
	}
	q.attachFun(inp.Tx, inp.Opts...)
}
