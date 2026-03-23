// seq_attach core module queues sequencer transactions for attachment.
//
// Permanent attacher cap with timestamp-based deadlock prevention:
//   - Track the slot of the latest attached transaction
//   - When attacher count >= cap, only transactions with timestamp strictly
//     before the latest attached pass (dependencies are always older than dependents)
//   - Recursive pull depth is capped separately (MaxAttachmentDepthForPull in virtual_tx.go)
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
		attachFun              AttachFun
		latestAttachedTimestamp atomic.Int64 // TicksSinceGenesis of the latest attached tx
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

const traceTag = "sync"

func (q *SeqAttach) consume(inp *Input) {
	txid := inp.Tx.ID()

	// during snapshot generation, drop all non-pulled transactions to shed load
	if !inp.Pulled && q.IsSnapshotting() {
		q.Tracef(traceTag, "seq_attach DROP %s: snapshotting, non-pulled", txid.StringShort)
		q.IncCounter("seq_drop")
		return
	}

	txTicks := txid.Timestamp().TicksSinceGenesis()

	// attacher cap with deadlock prevention:
	// attacher.NumAttachers() is the authoritative count — incremented synchronously
	// in AttachTransaction before the goroutine starts, decremented when it finishes.
	// When at the cap, only transactions strictly older than the latest pass
	// (dependencies always have earlier timestamps than their dependents).
	nAtt := attacher.NumAttachers()
	if nAtt >= q.MaxConcurrentAttachers() {
		if txTicks >= q.latestAttachedTimestamp.Load() {
			q.Tracef(traceTag, "seq_attach DROP %s: att=%d >= cap=%d, txTicks=%d >= latest=%d, pulled=%v",
				txid.StringShort, nAtt, q.MaxConcurrentAttachers(), txTicks, q.latestAttachedTimestamp.Load(), inp.Pulled)
			q.IncCounter("seq_drop")
			return
		}
		q.Tracef(traceTag, "seq_attach PASS (older) %s: att=%d >= cap=%d, txTicks=%d < latest=%d",
			txid.StringShort, nAtt, q.MaxConcurrentAttachers(), txTicks, q.latestAttachedTimestamp.Load())
	}

	// update latest attached timestamp only for txs that actually get attached
	// (consume is single-goroutine, no concurrent writers)
	if txTicks > q.latestAttachedTimestamp.Load() {
		q.latestAttachedTimestamp.Store(txTicks)
		q.Tracef(traceTag, "seq_attach: latestAttachedTimestamp updated to %s", txid.StringShort)
	}

	q.attachFun(inp.Tx, inp.Opts...)
}
